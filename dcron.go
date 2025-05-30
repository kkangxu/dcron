package dcron

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	heartbeatInterval        = 5 * time.Second  // Interval for sending heartbeat signals to registry to maintain node liveness
	nodeTimeout              = 15 * time.Second // Timeout threshold for considering a node as expired if no heartbeat received
	reloadDeletedTasksPeriod = 30 * time.Second // Period for reloading deleted tasks information from registry
	reloadTasksPeriod        = 20 * time.Second // Period for checking and updating dynamic task metadata from registry
	reloadNodesPeriod        = 15 * time.Second // Period for checking node information and status from registry
	virtualNodes             = 200              // Number of virtual nodes in consistent hash ring for task distribution
	virtualHashSlot          = 16384            // Number of virtual hash slots for consistent hashing algorithm
)

var (
	ErrDcronAlreadyRunning = errors.New("dcron is already running, please stop it first")
	ErrDcronAlreadyStopped = errors.New("dcron is already stopped, please start it first")
	ErrTaskExisted         = errors.New("the task already exists, please do not add the task again")                                              // Error when task already exists in registry
	ErrTaskDeleted         = errors.New("the task has been completed before and marked as deleted. please do not add it again. or use Force API") // Error when task is marked as deleted in registry
)

// Tasker is the interface for scheduled task execution
// Any type implementing this interface can be scheduled as a cron task
type Tasker interface {
	Runner() error // Method to execute the task and return any error that occurs
}

// Runner is a simple wrapper for a function that implements the Tasker interface
// This allows using regular functions as cron tasks without creating custom types
type Runner func() error

// Runner implements the Tasker interface by executing the function itself
func (f Runner) Runner() error {
	return f() // Execute the function and return its result
}

var _ Tasker = (*Runner)(nil) // Verify that Runner implements Tasker interface

// TaskRunFunc is a handler for dynamic tasks.
// If a task is added via TaskMeta and does not implement the Tasker interface,
// it will be executed by this handler with the task metadata as input.
type TaskRunFunc func(*TaskMeta) error // Handler for dynamic tasks

// ErrHandler is called when a cron job returns an error.
// It allows custom error handling logic for task execution failures.
type ErrHandler func(*TaskMeta, error) // Handler for task execution errors

// Dcron is the main interface for distributed cron scheduling
type Dcron interface {
	// Regarding the Addxxx API, when the service is started or restarted,
	// the Addxxx API only stores the task locally or in the registry, but also performs local duplication verification.
	// After the rebalance is started, the runnability verification will be performed.
	// However, when registering a task during operation, it will check whether the task has been deleted and whether the task has been registered.
	AddTask(taskName, cronFormat string, tasker Tasker) error        // Add a regular task with custom Tasker
	AddFunc(taskName, cronFormat string, runner Runner) error        // Add a regular task with function
	AddOneShotFunc(taskName, cronFormat string, runner Runner) error // Add a one-time task with function
	AddOneShotTask(taskName, cronFormat string, tasker Tasker) error // Add a one-time task with custom Tasker
	AddTaskMeta(taskMeta TaskMeta) error                             // Add a task using TaskMeta structure

	// Regarding the Forcexxx API, any time it is run, all data of the task in the registry will be completely deleted.
	// Add tasks in an overwritten manner.
	ForceAddTask(taskName, cronFormat string, tasker Tasker) error        // Force add a regular task, overwriting existing
	ForceAddFunc(taskName, cronFormat string, runner Runner) error        // Force add a regular task with function
	ForceAddOneShotTask(taskName, cronFormat string, tasker Tasker) error // Force add a one-time task
	ForceAddOneShotFunc(taskName, cronFormat string, runner Runner) error // Force add a one-time task with function
	ForceAddTaskMeta(taskMeta TaskMeta) error                             // Force add a task using TaskMeta structure

	ReuseDeletedTask(taskName string) error // Reuse a task that was previously deleted
	MarkTaskDeleted(taskName string) error  // Mark a task as deleted in registry
	CleanupTask(taskName string) error      // Completely remove a task's data from registry

	Start() error                                   // Start the dcron service
	Stop() error                                    // Stop the dcron service
	GetNodeID() string                              // Get current node's ID
	GetAllTasks() []string                          // Get names of all registered tasks
	GetMyselfRunningTasks() []string                // Get tasks running on current node
	ForceCleanupAllTasks(ctx context.Context) error // Force cleanup all tasks (dangerous, for testing only)
}

// TaskMeta defines the metadata of a task. It is used to store task information in the registry.
// External exported fields are used to add task information to cron cluster.
type TaskMeta struct {
	Name                   string `json:"name"`                                // Unique task identifier
	CronFormat             string `json:"cron_format"`                         // Cron expression format
	OneShot                bool   `json:"one_shot,omitempty"`                  // Whether task runs only once
	ExecutedAndMarkDeleted bool   `json:"executed_and_mark_deleted,omitempty"` // Mark task as deleted after execution
	ExecutedAndCleanup     bool   `json:"executed_and_cleanup,omitempty"`      // Completely remove task after execution
	Payload                string `json:"payload,omitempty"`                   // Optional task payload data
}

// task is the internal representation of a scheduled job.
type task struct {
	name     string       // Task name/identifier
	tasker   Tasker       // Task execution handler
	id       cron.EntryID // ID in the cron scheduler
	metadata *TaskMeta    // Task metadata
	dc       *dcron       // Reference to parent dcron instance
}

// Run is the entry point for cron tasks. !!important!!
// 1. Strictly prevent duplicate scheduling when nodes change:
//    When a node change has just occurred, all nodes must globally check the most recent execution time of the task.
//    Only tasks that have not been executed by other nodes will actually be executed.
//
// 2. Improve performance when nodes are stable:
//    When nodes have not changed for a long time, global checks can be appropriately reduced to improve scheduling performance.
//
// 3. Balance consistency and efficiency:
//    This approach ensures distributed idempotency while avoiding the performance overhead of reading from the registry on every scheduling cycle.

func (t *task) Run() {

	defer func() {
		// Recover from any panics during task execution to prevent the entire scheduler from crashing
		if err := recover(); err != nil {
			logger.Errorf("Task '%s' execution failed with error: %v", t.name, err)
		}
	}()

	now := time.Now()

	// Skip execution if task is marked as deleted in registry
	// This prevents execution of tasks that were already removed
	if t.dc.isDeletedTask(t.name) {
		return
	}

	// Check if this node should run the task based on distributed coordination
	// CanRunTask ensures only one node executes the task at a given time
	canRun, err := t.dc.registry.CanRunTask(t.dc.ctx, t.name, now)
	if err != nil {
		logger.Errorf("CanRunTask check for task '%s' failed with error: %v", t.name, err)
		return
	}
	if !canRun {
		logger.Infof("Task '%s' (cronID: %d) cannot be executed at this time due to concurrency control", t.name, t.id)
		return
	}

	// Run the task (this is the task handler entry)
	// Execute the task's Runner method and handle any errors
	if err := t.tasker.Runner(); err != nil {
		if t.dc.errHandler != nil {
			t.dc.errHandler(t.metadata, err)
		}
	}

	// Handle post-execution actions based on task metadata settings
	// For one-shot tasks or tasks with cleanup flags, perform necessary actions
	if t.metadata.OneShot || t.metadata.ExecutedAndMarkDeleted {
		// Mark task as deleted after execution according to its configuration
		if err := t.dc.MarkTaskDeleted(t.name); err != nil {
			if t.dc.errHandler != nil {
				t.dc.errHandler(t.metadata, err)
			}
		}
	} else if t.metadata.ExecutedAndCleanup {
		// Completely remove task data after execution according to its configuration
		if err := t.dc.CleanupTask(t.name); err != nil {
			if t.dc.errHandler != nil {
				t.dc.errHandler(t.metadata, err)
			}
		}
	}

}

type dcron struct {
	registry       Registry            // Registry implementation for distributed node and task management
	cr             *cron.Cron          // Underlying cron scheduler for local task execution
	cOptions       []cron.Option       // Options for configuring the cron scheduler
	nodeID         string              // Unique identifier for this node in the cluster
	nodeIP         string              // IP address of this node for network communication
	ctx            context.Context     // Context for controlling goroutines and cancellation
	cancel         context.CancelFunc  // Function to cancel the context and stop all operations
	taskRunFunc    TaskRunFunc         // Handler for executing dynamic tasks with metadata
	errHandler     ErrHandler          // Handler for processing task execution errors
	running        bool                // Flag to indicate if dcron service is currently running
	runMux         sync.Mutex          // Mutex to protect concurrent start and stop operations
	nodes          []Node              // List of all nodes in the cluster
	deletedTasks   map[string]struct{} // Set of task names that have been marked as deleted
	allTasks       map[string]*task    // Map of all registered tasks by name
	assignedTasks  map[string]*task    // Map of tasks assigned to this specific node
	tasksRWMux     sync.RWMutex        // Mutex for protecting concurrent access to task maps
	rebalancedChan chan struct{}       // Channel for signaling when task rebalancing is complete
	shutdownChan   chan struct{}       // Channel for signaling service shutdown
	assigner       Assigner            // Task assigner implementation for distributing tasks across nodes
	weight         int                 // Node weight value, used for weighted task assignment
}

// NewDcron creates a new Dcron instance with the specified registry and options.
// The registry parameter determines the backend used for distributed coordination.
// Options can be provided to customize the behavior of the Dcron instance.
func NewDcron(registry Registry, opts ...Option) Dcron {
	ctx, cancel := context.WithCancel(context.Background())

	dc := &dcron{
		registry:       registry,
		allTasks:       make(map[string]*task),
		assignedTasks:  make(map[string]*task),
		deletedTasks:   make(map[string]struct{}),
		ctx:            ctx,
		cancel:         cancel,
		shutdownChan:   make(chan struct{}),
		rebalancedChan: make(chan struct{}, RebalancedChanelSize),
	}

	// Apply options to the dcron instance
	for _, opt := range opts {
		opt(dc)
	}

	// Create a new cron instance
	dc.cr = cron.New(dc.cOptions...)

	// Init default assigner
	if dc.assigner == nil {
		dc.assigner = NewAssigner(StrategyConsistent)
	}
	dc.assigner.SetNodeID(dc.nodeID)

	// Set default node ID and IP
	if len(dc.nodeIP) == 0 {
		dc.nodeIP = getLocalIP()
	}
	dc.nodeID = generateNodeID(dc.nodeIP)

	// Set default logger
	if logger == nil {
		logger = newStdLogger(LevelInfo)
	}

	return dc
}

// AddTask adds a new static task. The taskName must be unique.
func (dc *dcron) AddTask(taskName, cronFormat string, tasker Tasker) error {
	return dc.addJob(taskName, cronFormat, tasker, false, false) // Add a regular task
}

// AddFunc adds a new static task using a function. The taskName must be unique.
func (dc *dcron) AddFunc(taskName, cronFormat string, runner Runner) error {
	return dc.addJob(taskName, cronFormat, runner, false, false) // Add a regular task with function
}

// AddOneShotTask adds a new one-shot task. The taskName must be unique.
func (dc *dcron) AddOneShotTask(taskName, cronFormat string, tasker Tasker) error {
	return dc.addJob(taskName, cronFormat, tasker, true, false) // Add a one-shot task
}

// AddOneShotFunc adds a new one-shot task using a function. The taskName must be unique.
func (dc *dcron) AddOneShotFunc(taskName, cronFormat string, runner Runner) error {
	return dc.addJob(taskName, cronFormat, runner, true, false) // Add a one-shot task with function
}

// AddTaskMeta adds a task using TaskMeta structure.
func (dc *dcron) AddTaskMeta(tm TaskMeta) error {
	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()
	if _, ok := dc.deletedTasks[tm.Name]; ok {
		return ErrTaskDeleted // Return error if task is marked as deleted
	}

	if _, ok := dc.allTasks[tm.Name]; ok {
		return ErrTaskExisted // Return error if task already exists
	}

	if err := dc.registry.PutTask(dc.ctx, tm); err != nil {
		return err // Store task metadata in registry
	}

	t := &task{
		name:     tm.Name,
		metadata: &tm,
		dc:       dc,
	}
	t.tasker = Runner(func() error {
		if dc.taskRunFunc == nil {
			logger.Errorf("Dynamic task '%s' execution skipped: no handler registered for dynamic tasks", t.name) // Log if no handler is set
			return nil
		}
		return dc.taskRunFunc(t.metadata) // Execute the dynamic task handler
	})

	dc.allTasks[tm.Name] = t // Add task to all tasks map

	logger.Infof("Task '%s' successfully added to registry and available for scheduling", tm.Name) // Log task addition
	return nil
}

// ForceAddTask forcefully adds a task, ignoring existing and deleted restrictions.
func (dc *dcron) ForceAddTask(taskName, cronFormat string, tasker Tasker) error {
	err := dc.registry.ForceCleanupTask(dc.ctx, taskName) // Force cleanup task in registry
	if err != nil {
		return fmt.Errorf("task '%s' cleanup operation failed during forced addition, retry may be required: %v", taskName, err)
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	delete(dc.deletedTasks, taskName)
	if t, ok := dc.assignedTasks[taskName]; ok {
		if t.id != 0 {
			dc.cr.Remove(t.id)
		}
		delete(dc.assignedTasks, taskName)
	}

	t := &task{
		name:   taskName,
		tasker: tasker,
		metadata: &TaskMeta{
			Name:       taskName,
			CronFormat: cronFormat,
		},
		dc: dc,
	}
	dc.allTasks[taskName] = t                                                                                    // Add task to all tasks map
	logger.Infof("Task '%s' successfully force-added to registry, overriding any previous task state", taskName) // Log force addition
	return nil
}

// ForceAddFunc forcefully adds a task using a function.
func (dc *dcron) ForceAddFunc(taskName, cronFormat string, runner Runner) error {
	return dc.ForceAddTask(taskName, cronFormat, runner) // Force add task with function
}

// ForceAddOneShotTask forcefully adds a one-shot task.
func (dc *dcron) ForceAddOneShotTask(taskName, cronFormat string, tasker Tasker) error {
	err := dc.registry.ForceCleanupTask(dc.ctx, taskName) // Force cleanup task in registry
	if err != nil {
		return fmt.Errorf("forced cleanup of task '%s' failed during one-shot task creation: %v", taskName, err)
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	delete(dc.deletedTasks, taskName)
	if t, ok := dc.assignedTasks[taskName]; ok {
		if t.id != 0 {
			dc.cr.Remove(t.id)
		}
		delete(dc.assignedTasks, taskName)
	}
	t := &task{
		name:   taskName,
		tasker: tasker,
		metadata: &TaskMeta{
			Name:       taskName,
			CronFormat: cronFormat,
			OneShot:    true, // Mark as one-shot task
		},
		dc: dc,
	}
	dc.allTasks[taskName] = t                                                                              // Add task to all tasks map
	logger.Infof("One-shot task '%s' successfully force-added to registry for single execution", taskName) // Log force addition
	return nil
}

// ForceAddOneShotFunc forcefully adds a one-shot task using a function.
func (dc *dcron) ForceAddOneShotFunc(taskName, cronFormat string, runner Runner) error {
	return dc.ForceAddOneShotTask(taskName, cronFormat, runner) // Force add one-shot task with function
}

// ForceAddTaskMeta forcefully adds a task using TaskMeta structure.
func (dc *dcron) ForceAddTaskMeta(tm TaskMeta) error {
	if err := dc.registry.ForceCleanupTask(dc.ctx, tm.Name); err != nil {
		return err // Force cleanup task in registry
	}

	if err := dc.registry.PutTask(dc.ctx, tm); err != nil {
		return err // Store task metadata in registry
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	delete(dc.deletedTasks, tm.Name)
	if t, ok := dc.assignedTasks[tm.Name]; ok {
		if t.id != 0 {
			dc.cr.Remove(t.id)
		}
		delete(dc.assignedTasks, tm.Name)
	}

	t := &task{
		name:     tm.Name,
		metadata: &tm,
		dc:       dc,
	}
	t.tasker = Runner(func() error {
		if dc.taskRunFunc == nil {
			logger.Errorf("Dynamic task '%s' execution skipped: no handler registered for dynamic tasks", t.name) // Log if no handler is set
			return nil
		}
		return dc.taskRunFunc(t.metadata) // Execute the dynamic task handler
	})

	dc.allTasks[tm.Name] = t // Add task to all tasks map

	logger.Infof("Task '%s' successfully added to registry via TaskMeta configuration", tm.Name) // Log task addition
	return nil
}

// ReuseDeletedTask restores a previously deleted task.
func (dc *dcron) ReuseDeletedTask(taskName string) error {

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()
	if _, ok := dc.deletedTasks[taskName]; !ok {
		return fmt.Errorf("task '%s' is not currently marked as deleted, cannot restore a non-deleted task", taskName)
	}
	if _, ok := dc.allTasks[taskName]; !ok {
		return fmt.Errorf("task '%s' is not found in deleted tasks registry, it may have been completely cleaned up", taskName)
	}

	delete(dc.deletedTasks, taskName)

	if err := dc.registry.RemoveTaskDeleted(dc.ctx, taskName); err != nil {
		return err
	}

	return nil
}

// MarkTaskDeleted marks a task as deleted in the registry.
func (dc *dcron) MarkTaskDeleted(taskName string) error {
	if err := dc.registry.MarkTaskDeleted(dc.ctx, taskName); err != nil {
		return err // Mark task as deleted in registry
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	t, ok := dc.allTasks[taskName]
	if !ok {
		return fmt.Errorf("task '%s' not found in active task registry, cannot mark as deleted", taskName) // Return error if task not found
	}

	// If the task is in assignedTasks, it is also removed from cron
	if _, assigned := dc.assignedTasks[taskName]; assigned {
		dc.cr.Remove(t.id)                 // Remove task from cron scheduler
		delete(dc.assignedTasks, taskName) // Remove from assigned tasks
	}

	dc.deletedTasks[taskName] = struct{}{} // Mark task as deleted

	logger.Infof("Task '%s' marked as deleted and will no longer be scheduled for execution", taskName) // Log task deletion
	return nil
}

// CleanupTask completely removes a task's data from the registry.
func (dc *dcron) CleanupTask(taskName string) error {
	if err := dc.registry.ForceCleanupTask(dc.ctx, taskName); err != nil {
		return err // Force cleanup task in registry
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	if t, assigned := dc.assignedTasks[taskName]; assigned {
		dc.cr.Remove(t.id)
	}

	delete(dc.allTasks, taskName)      // Remove from all tasks
	delete(dc.assignedTasks, taskName) // Remove from assigned tasks
	delete(dc.deletedTasks, taskName)  // Remove from deleted tasks

	logger.Infof("Task '%s' completely removed from registry and all associated data cleaned up", taskName) // Log task cleanup
	return nil
}

// Start initializes and starts the dcron service.
func (dc *dcron) Start() error {
	dc.runMux.Lock()
	if dc.running {
		dc.runMux.Unlock()
		return ErrDcronAlreadyRunning
	}
	dc.running = true
	dc.runMux.Unlock()

	// Register node
	if err := dc.registry.Register(dc.ctx, Node{
		ID:         dc.nodeID,
		IP:         dc.nodeIP,
		LastAlive:  time.Now(),
		CreateTime: time.Now(),
		Status:     NodeStatusStarting,
		Weight:     dc.weight,
	}); err != nil {
		return fmt.Errorf("node registration failed in distributed registry, cannot start dcron service: %v", err)
	}

	dc.prepareWork() // Prepare work for the dcron service
	dc.asyncWork()   // Start asynchronous work

	// Start the cron scheduler
	dc.cr.Start()

	// Update node status to working
	if err := dc.registry.UpdateStatus(dc.ctx, dc.nodeID, NodeStatusWorking); err != nil {
		return fmt.Errorf("status update for node '%s' to 'working' failed, service started but may be in inconsistent state: %v", dc.nodeID, err)
	}

	logger.Infof("Dcron service started successfully | Strategy: %s | Node ID: %s | Status: Working", dc.assigner.Name(), dc.nodeID) // Log service start

	// Wait for shutdown signal
	dc.waitForShutdown()

	return nil
}

// Stop stops the dcron service.
func (dc *dcron) Stop() error {
	dc.runMux.Lock()
	if !dc.running {
		dc.runMux.Unlock()
		return ErrDcronAlreadyStopped
	}
	dc.running = false
	dc.runMux.Unlock()
	close(dc.shutdownChan) // Close the shutdown channel
	return nil
}

// GetNodeID returns the current node's ID.
func (dc *dcron) GetNodeID() string {
	return dc.nodeID
}

// GetNodes retrieves all nodes from the registry.
func (dc *dcron) GetNodes() ([]Node, error) {
	return dc.registry.GetNodes(dc.ctx)
}

// GetWorkingNodes retrieves all nodes that are currently working.
func (dc *dcron) GetWorkingNodes() ([]Node, error) {
	return dc.registry.GetWorkingNodes(dc.ctx)
}

// GetAllTasks retrieves the names of all registered tasks.
func (dc *dcron) GetAllTasks() []string {
	dc.tasksRWMux.RLock()
	defer dc.tasksRWMux.RUnlock()

	var tasks []string
	for name := range dc.allTasks {
		tasks = append(tasks, name)
	}
	return tasks
}

// GetRunningTasks retrieves the names of all tasks currently running.
func (dc *dcron) GetRunningTasks() []string {
	dc.tasksRWMux.RLock()
	defer dc.tasksRWMux.RUnlock()

	var tasks []string
	for name := range dc.assignedTasks {
		if _, ok := dc.deletedTasks[name]; ok {
			continue
		}
		tasks = append(tasks, name)
	}
	return tasks
}

// GetMyselfRunningTasks retrieves the tasks running on the current node.
func (dc *dcron) GetMyselfRunningTasks() []string {
	dc.tasksRWMux.RLock()
	defer dc.tasksRWMux.RUnlock()

	var tasks []string
	for name := range dc.assignedTasks {
		tasks = append(tasks, name)
	}
	return tasks
}

// ForceCleanupAllTasks forcefully cleans up all tasks and their metadata.
func (dc *dcron) ForceCleanupAllTasks(ctx context.Context) error {
	if err := dc.registry.ForceCleanupAllTasks(ctx); err != nil {
		return err
	}
	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	for _, task := range dc.assignedTasks {
		if task.id != 0 {
			dc.cr.Remove(task.id) // Remove task from cron scheduler
		}
	}

	dc.allTasks = make(map[string]*task)        // Clear all tasks
	dc.assignedTasks = make(map[string]*task)   // Clear assigned tasks
	dc.deletedTasks = make(map[string]struct{}) // Clear deleted tasks
	return nil
}
