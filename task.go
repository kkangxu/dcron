package dcron

import (
	"sort"
	"time"

	"github.com/robfig/cron/v3"
)

// addJob is the internal API for adding a task. It checks for deleted tasks before proceeding.
func (dc *dcron) addJob(taskName, cronFormat string, tasker Tasker, oneShot bool) error {

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	return dc.addJobLocked(taskName, cronFormat, tasker, oneShot)
}

func (dc *dcron) addJobLocked(taskName, cronFormat string, tasker Tasker, oneShot bool) error {

	if _, ok := dc.deletedTasks[taskName]; ok {
		return ErrTaskDeleted
	}

	if _, ok := dc.allTasks[taskName]; ok {
		return ErrTaskExisted
	}

	// Create a new task instance and add it to allTasks
	t := &task{
		name:   taskName,
		tasker: tasker,
		metadata: &TaskMeta{
			Name:       taskName,
			CronFormat: cronFormat,
			OneShot:    oneShot,
		},
		dc: dc,
	}

	dc.allTasks[taskName] = t

	return nil

}

// watchTaskEvent continuously monitors task events from the registry
// This triggers task rebalancing when tasks are added, updated, or deleted
func (dc *dcron) watchTaskEvent() {

	ticker := time.NewTicker(reloadTasksPeriod)
	defer ticker.Stop()

	eventChan, err := dc.registry.WatchTaskEvent(dc.ctx)
	if err != nil {
		logger.Infof("Task monitoring encountered an error while watching for task changes: %v", err)
		return
	}

	for {
		select {
		case event := <-eventChan:
			dc.handleTaskEvent(event)
			dc.rebalancedChan <- struct{}{}
		case <-ticker.C:
			dc.reloadTasks()
			dc.rebalancedChan <- struct{}{}
		case <-dc.ctx.Done():
			return
		}
	}
}

// handleTaskEvent processes task events from the registry
// This updates the local task cache and triggers rebalancing as needed
func (dc *dcron) handleTaskEvent(event TaskMetaEvent) {

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	switch event.Type {
	case TaskEventTypePut:
		// get existing task (if any)
		existingTask, exists := dc.allTasks[event.Task.Name]

		// need to reschedule the task if it exists and the cron format has changed
		needReschedule := exists && existingTask.id != 0 && existingTask.metadata.CronFormat != event.Task.CronFormat

		// if task need to reschedule, remove the old task.
		if needReschedule {
			dc.cr.Remove(existingTask.id)
			delete(dc.assignedTasks, event.Task.Name)
			existingTask.id = 0
		}

		// create/update task
		newTask := &task{
			name:     event.Task.Name,
			metadata: &event.Task,
			dc:       dc,
			id:       existingTask.id,
		}

		// set task runner
		newTask.tasker = Runner(func() error {
			if dc.taskRunFunc == nil {
				logger.Errorf("Dynamic task '%s' execution skipped: no handler has been registered for dynamic tasks", newTask.name)
				return nil
			}
			return dc.taskRunFunc(newTask.metadata)
		})

		// update task mapping
		dc.allTasks[event.Task.Name] = newTask

	case TaskEventTypeDelete:
		if t, ok := dc.allTasks[event.Task.Name]; ok {
			delete(dc.allTasks, event.Task.Name)
			delete(dc.assignedTasks, event.Task.Name)
			if t.id != 0 {
				dc.cr.Remove(t.id)
			}
		}
	}
}

// reloadTasks fetches the latest tasks from the registry and updates the local cache
// This ensures that tasks are up-to-date and triggers rebalancing as needed
func (dc *dcron) reloadTasks() {
	tasks, err := dc.registry.GetAllTasks(dc.ctx)
	if err != nil {
		logger.Errorf("Failed to retrieve task metadata from registry - task scheduling may be using outdated information: %v", err)
		return
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()
	for _, dt := range tasks {
		t, exists := dc.allTasks[dt.Name]
		if !exists {
			t = &task{
				name:     dt.Name,
				metadata: &dt,
				dc:       dc,
			}
			t.tasker = Runner(func() error {
				if dc.taskRunFunc == nil {
					logger.Errorf("Dynamic task '%s' execution skipped: no handler has been registered for dynamic tasks", t.name)
					return nil
				}
				return dc.taskRunFunc(t.metadata)
			})

			dc.allTasks[dt.Name] = t
		}
	}
}

// watchDeletedTaskEvent continuously monitors deleted task events from the registry
// This triggers task rebalancing when tasks are deleted
func (dc *dcron) watchDeletedTaskEvent() {
	ticker := time.NewTicker(reloadDeletedTasksPeriod)
	defer ticker.Stop()

	eventChan, err := dc.registry.WatchDeletedTaskEvent(dc.ctx)
	if err != nil {
		logger.Errorf("Task deletion monitoring encountered an error while watching for deletion events: %v", err)
		return
	}

	for {
		select {
		case <-eventChan:
			dc.reloadDeletedTasks()
			dc.rebalancedChan <- struct{}{}
		case <-ticker.C:
			dc.reloadDeletedTasks()
			dc.rebalancedChan <- struct{}{}
		case <-dc.ctx.Done():
			return
		}
	}
}

// reloadDeletedTasks fetches the latest deleted tasks from the registry and updates the local cache
// This ensures that deleted tasks are not re-added or executed
func (dc *dcron) reloadDeletedTasks() {

	deletedTasks := map[string]struct{}{}
	for _, taskName := range dc.registry.GetDeletedTaskNames(dc.ctx) {
		deletedTasks[taskName] = struct{}{}
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()
	dc.deletedTasks = deletedTasks
}

// rebalanced continuously monitors the rebalanced channel for task rebalancing signals
// This triggers task rebalancing when the channel is signaled
func (dc *dcron) rebalanced() {

	for {
		select {
		case <-dc.rebalancedChan:
			dc.doRebalanced()
		case <-dc.ctx.Done():
			return
		}
	}

}

// doRebalanced performs task rebalancing by re-assigning tasks to nodes
// This ensures that tasks are distributed evenly across nodes
func (dc *dcron) doRebalanced() {

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	if len(dc.nodes) == 0 {
		logger.Info("No working nodes available in the cluster - tasks cannot be assigned until at least one node becomes available")
		return
	}

	dc.assigner.UpdateNodes(dc.nodes)

	// collect all task names and sort them for consistency
	var taskNames []string
	for name := range dc.allTasks {
		taskNames = append(taskNames, name)
	}
	sort.Strings(taskNames)
	var counter, assigned, unassigned int

	// visit all tasks and re-assign them
	for _, taskName := range taskNames {

		task := dc.allTasks[taskName]
		shouldRun := false

		if _, deleted := dc.deletedTasks[taskName]; !deleted {
			shouldRun = dc.shouldRunTask(taskName)
		}

		// task has been existed in cron
		var existed bool
		if task.id != 0 {
			existed = dc.existedInCron(task.id)
		}

		// debug log
		// logger.Infof("handle task %s (cronID: %d), shouldRun[%v], existed[%v]", taskName, task.id, shouldRun, existed)

		if shouldRun {
			counter++
			if !existed {
				// task has not been assigned to current node
				newID, err := dc.cr.AddJob(task.metadata.CronFormat, cron.FuncJob(task.Run))
				if err != nil {
					logger.Errorf("Failed to re-add task '%s' to scheduler after node change - task execution may be delayed: %v", taskName, err)
					continue
				}
				task.id = newID
				dc.assignedTasks[taskName] = task
				assigned++
				logger.Infof("Task '%s' (ID: %d) has been assigned to this node and scheduled for execution", taskName, newID)
			}
		} else {
			if existed {
				// task has been assigned to current node
				// remove task from cron
				dc.cr.Remove(task.id)
				delete(dc.assignedTasks, taskName)
				unassigned++
				logger.Infof("Task '%s' (ID: %d) has been unassigned from this node and removed from execution schedule", taskName, task.id)
			}
		}
	}

	// record rebalanced result
	logger.Infof("Task rebalance completed | Cluster: %d nodes | Tasks: %d total, %d deleted | Local node: %d tasks, %d assigned, %d unassigned",
		len(dc.nodes), len(taskNames), len(dc.deletedTasks), counter, assigned, unassigned)
}

// shouldRunTask determines if a task should be run on the current node
// This is based on the task assignment policy
func (dc *dcron) shouldRunTask(taskName string) bool {
	if dc.assigner == nil {
		return false
	}
	return dc.assigner.ShouldRun(taskName, dc.nodeID)
}

// existedInCron checks if a task with the given ID is still scheduled in the cron engine
// This prevents attempting to remove a task that's already been removed
func (dc *dcron) existedInCron(id cron.EntryID) bool {
	entries := dc.cr.Entries()
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// isDeletedTask checks if a task has been marked as deleted
// This prevents execution of tasks that have been marked for deletion
func (dc *dcron) isDeletedTask(taskName string) bool {
	dc.tasksRWMux.RLock()
	defer dc.tasksRWMux.RUnlock()
	_, ok := dc.deletedTasks[taskName]
	return ok
}
