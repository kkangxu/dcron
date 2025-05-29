package dcron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/go-zookeeper/zk"
)

// ZooKeeper path constants for storing dcron data
// These paths define the hierarchy structure for storing distributed cron information
const (
	zkPrefix       = "/distributed_cron"               // Root path for all dcron data in ZooKeeper
	zkNodePrefix   = "/distributed_cron/nodes"         // Path for storing node information
	zkTasksPrefix  = "/distributed_cron/tasks"         // Path for storing task metadata
	zkDeletedTasks = "/distributed_cron/deleted_tasks" // Path for tracking deleted tasks
	zkTaskLastExec = "/distributed_cron/last_exec"     // Path for recording last execution time of tasks
)

// Registry interface implementation check
var _ Registry = (*zookeeperRegistry)(nil)

// zookeeperRegistry implements the Registry interface using ZooKeeper as the backend
// ZooKeeper provides strong consistency guarantees for distributed coordination
type zookeeperRegistry struct {
	conn     *zk.Conn      // ZooKeeper connection for interacting with the ZooKeeper service
	acl      []zk.ACL      // Access control list for ZooKeeper nodes, defining permissions
	nodePath string        // Path to the current node in ZooKeeper, includes unique sequential ID
	cleaner  *EventCleaner // Event Cleaner: Specialized to clear temporary flag data during task execution
}

// NewZookeeperRegistry creates a new Registry instance backed by ZooKeeper
// It initializes the registry with world-readable ACLs for all nodes
func NewZookeeperRegistry(conn *zk.Conn) Registry {

	acl := zk.WorldACL(zk.PermAll)

	return &zookeeperRegistry{
		conn: conn,
		acl:  acl,
	}
}

// Register creates a new ephemeral sequential node in ZooKeeper to represent this dcron node
// It ensures all required root paths exist and stores the node data in ZooKeeper
// Ephemeral nodes automatically disappear when the session ends, providing automatic failure detection
func (r *zookeeperRegistry) Register(ctx context.Context, node Node) error {

	// Ensure all required root paths exist in ZooKeeper.
	// These paths form the basic directory structure for dcron in ZooKeeper.
	rootPaths := []string{zkPrefix, zkNodePrefix, zkTasksPrefix, zkDeletedTasks, zkTaskLastExec}
	for _, p := range rootPaths {
		exists, _, err := r.conn.Exists(p)
		if err != nil && !errors.Is(err, zk.ErrNoNode) {
			return fmt.Errorf("unable to verify path %s existence in ZooKeeper: %v", p, err)
		}
		if !exists {
			// Ignore error if node already exists, which can happen in concurrent scenarios.
			_, err := r.conn.Create(p, []byte{}, 0, r.acl)
			if err != nil && !errors.Is(err, zk.ErrNodeExists) {
				return fmt.Errorf("error creating required ZooKeeper path %s for storing node data: %v", p, err)
			}
		}
	}

	// Marshal node data to JSON
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("could not serialize node data to JSON for ZooKeeper storage: %v", err)
	}

	// Create a protected ephemeral sequential node for the current node.
	// The node's actual path will have a unique sequential suffix.
	// Example: /distributed_cron/nodes/node-id-0000000001
	nodePath := path.Join(zkNodePrefix, node.ID)
	pathCreated, err := r.conn.CreateProtectedEphemeralSequential(nodePath, data, r.acl)
	if err != nil {
		return fmt.Errorf("error creating protected ephemeral node in ZooKeeper, node registration failed: %v", err)
	}

	r.nodePath = pathCreated

	// Initialize and start the event cleaner for managing execution records
	// The cleaner periodically removes stale execution records to prevent ZooKeeper from filling up
	r.cleaner = NewEventCleaner(ctx,
		WithEventDelay(CleanerDelay),
		WithEventBufferSize(CleanerBufferSize),
		WithBatchSize(CleanerBatchSize),
		WithInjectedTask([]func(ctx context.Context){r.cleanupHistoryExecKeys}),
		WithEventHandler(r.batchDeleteExecKeys),
	)
	go r.cleaner.Start()

	return nil
}

// Unregister removes the node from ZooKeeper registry
// For the current node, it directly deletes using the known path
// For other nodes, it searches through all children to find and delete the matching node
func (r *zookeeperRegistry) Unregister(ctx context.Context, nodeID string) error {
	if r.nodePath != "" {
		return r.conn.Delete(r.nodePath, -1)
	}

	// get all children
	children, _, err := r.conn.Children(zkNodePrefix)
	if err != nil {
		return fmt.Errorf("problem retrieving node list from ZooKeeper, cannot get children of root path: %v", err)
	}

	for _, child := range children {
		fullPath := path.Join(zkNodePrefix, child)
		data, _, err := r.conn.Get(fullPath)
		if err != nil {
			continue
		}

		var node Node
		if err := json.Unmarshal(data, &node); err != nil {
			continue
		}

		if node.ID == nodeID {
			return r.conn.Delete(fullPath, -1)
		}
	}

	// If the loop completes, the node was not found.
	// This is not necessarily an error, as the node might have already been unregistered.
	logger.Infof("Node '%s' not found in ZooKeeper during unregistration attempt - node may have already been unregistered or expired", nodeID)

	return nil
}

// UpdateStatus updates the status field of a node in ZooKeeper
// It uses the updateNodeField helper function to modify only the status
func (r *zookeeperRegistry) UpdateStatus(ctx context.Context, nodeID string, status NodeStatus) error {
	return r.updateNodeField(nodeID, func(node *Node) {
		node.Status = status
	})
}

// UpdateHeartbeat updates the LastAlive timestamp of a node to the current time
// This is used for detecting node liveness in the distributed system
func (r *zookeeperRegistry) UpdateHeartbeat(ctx context.Context, nodeID string) error {
	return r.updateNodeField(nodeID, func(node *Node) {
		node.LastAlive = time.Now()
	})
}

// updateNodeField is a generic helper function to update a field of a node.
// It fetches all child nodes under zkNodePrefix, finds the node with the matching nodeID,
// applies the updateFn to it, and then writes the modified node data back to ZooKeeper.
// This uses optimistic locking by providing the ZNode's current version during the Set operation.
func (r *zookeeperRegistry) updateNodeField(nodeID string, updateFn func(*Node)) error {
	// First, try to update using the known r.nodePath if it matches the nodeID.
	// This is an optimization to avoid listing all children if we are updating the current node.
	if r.nodePath != "" {
		// We need to extract the base ID from r.nodePath as it contains the sequential suffix.
		// A more robust way would be to fetch r.nodePath, unmarshal, check ID, then update.
		// For simplicity here, we assume if r.nodePath is set, it's for the current node.
		// However, nodeID passed to this function should be the canonical ID without suffix.
		// Let's fetch the node data from r.nodePath to confirm its ID.
		data, stat, err := r.conn.Get(r.nodePath)
		if err == nil { // Node exists
			var currentNode Node
			if err := json.Unmarshal(data, &currentNode); err == nil {
				if currentNode.ID == nodeID { // This is indeed the node we want to update
					updateFn(&currentNode)
					newData, err := json.Marshal(currentNode)
					if err != nil {
						return fmt.Errorf("unable to serialize updated node data for %s, status update failed: %v", r.nodePath, err)
					}
					_, err = r.conn.Set(r.nodePath, newData, stat.Version)
					if err != nil {
						return fmt.Errorf("issue updating node data at path %s in ZooKeeper, version conflict may have occurred: %v", r.nodePath, err)
					}
					return nil // Successfully updated via r.nodePath
				}
			}
		}
		// If you Get failed or ID didn't match, fall through to iterating children.
		// This could happen if r.nodePath is stale or belongs to a different node ID.
	}

	// Fallback approach: Search through all nodes to find the one with matching ID
	children, _, err := r.conn.Children(zkNodePrefix)
	if err != nil {
		if errors.Is(err, zk.ErrNoNode) {
			return fmt.Errorf("node prefix %s does not exist, cannot update node %s", zkNodePrefix, nodeID)
		}
		return fmt.Errorf("error retrieving node children from %s while updating node %s status: %v", zkNodePrefix, nodeID, err)
	}

	for _, child := range children {
		fullPath := path.Join(zkNodePrefix, child)
		data, stat, err := r.conn.Get(fullPath)
		if err != nil {
			// Node might have been deleted concurrently.
			logger.Errorf("Failed to retrieve data for node '%s' during status update - node may have been concurrently deleted: %v", fullPath, err)
			continue
		}

		var node Node
		if err := json.Unmarshal(data, &node); err != nil {
			logger.Errorf("Failed to parse node data from JSON for '%s' during status update - data may be corrupted: %v", fullPath, err)
			continue
		}

		if node.ID == nodeID {
			// Apply the update function to the fetched node data.
			updateFn(&node)
			newData, err := json.Marshal(node)
			if err != nil {
				return fmt.Errorf("could not serialize updated node data for node %s, JSON marshaling failed: %v", fullPath, err)
			}
			_, err = r.conn.Set(fullPath, newData, stat.Version)
			if err != nil {
				return fmt.Errorf("trouble updating node data at path %s in ZooKeeper, version conflict may have occurred: %v", fullPath, err)
			}
			return nil
		}
	}

	return fmt.Errorf("node %s not found for update", nodeID)
}

// GetNodes retrieves a list of all nodes in the ZooKeeper registry
func (r *zookeeperRegistry) GetNodes(ctx context.Context) ([]Node, error) {
	children, _, err := r.conn.Children(zkNodePrefix)
	if err != nil {
		return nil, fmt.Errorf("problem retrieving node list from ZooKeeper root path: %v", err)
	}

	nodes := make([]Node, 0, len(children))
	for _, child := range children {
		fullPath := path.Join(zkNodePrefix, child)
		data, _, err := r.conn.Get(fullPath)
		if err != nil {
			continue
		}

		var node Node
		if err := json.Unmarshal(data, &node); err != nil {
			continue
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// GetWorkingNodes retrieves a list of all working nodes in the ZooKeeper registry
func (r *zookeeperRegistry) GetWorkingNodes(ctx context.Context) ([]Node, error) {
	nodes, err := r.GetNodes(ctx)
	if err != nil {
		return nil, err
	}

	workingNodes := make([]Node, 0)
	for _, node := range nodes {
		if node.Status == NodeStatusWorking {
			workingNodes = append(workingNodes, node)
		}
	}

	return workingNodes, nil
}

// WatchNodes watches for changes in the list of nodes in the ZooKeeper registry
func (r *zookeeperRegistry) WatchNodes(ctx context.Context) (<-chan NodeEvent, error) {
	eventChan := make(chan NodeEvent, NodeEventChannelSize)

	// Initialize the watch
	_, _, watcher, err := r.conn.ChildrenW(zkNodePrefix)
	if err != nil {
		return nil, fmt.Errorf("initial watch failed: %w", err)
	}

	// Send initial event (empty change notification)
	eventChan <- NodeEvent{Type: NodeEventTypeChanged}

	go func() {
		defer close(eventChan)
		currentWatcher := watcher
		retryDelay := time.Second

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-currentWatcher:
				// Watch channel abnormally closed
				if !ok {
					if newWatcher, err := r.reestablishWatch(); err == nil {
						currentWatcher = newWatcher
						retryDelay = time.Second // Reset delay
						continue
					}
					time.Sleep(retryDelay)
					retryDelay = min(retryDelay*2, 30*time.Second)
					continue
				}

				// Event types that require watch rebuilding
				if event.Type == zk.EventNotWatching ||
					event.Type == zk.EventNodeDeleted ||
					event.Type == zk.EventSession {
					logger.Warnf("Watch invalidated by event: %v", event.Type)
					if newWatcher, err := r.reestablishWatch(); err == nil {
						currentWatcher = newWatcher
						continue
					}
					// Continue waiting for the next retry on rebuild failure
					continue
				}

				// Ignore non-child node change events
				if event.Type != zk.EventNodeChildrenChanged {
					logger.Debugf("Ignoring event type: %v", event.Type)
					continue
				}

				// Normal handling of child node changes
				newNodes, _, newWatcher, err := r.conn.ChildrenW(zkNodePrefix)
				if err != nil {
					logger.Errorf("Failed to update watch: %v", err)
					continue
				}

				currentWatcher = newWatcher
				logger.Infof("Node changed, current count: %d", len(newNodes))
				eventChan <- NodeEvent{Type: NodeEventTypeChanged}
			}
		}
	}()

	return eventChan, nil
}

// reestablishWatch is a dedicated method for rebuilding the watch
func (r *zookeeperRegistry) reestablishWatch() (<-chan zk.Event, error) {
	// Check if parent node exists (for EventNodeDeleted case)
	exists, _, err := r.conn.Exists(zkNodePrefix)
	if err != nil {
		return nil, fmt.Errorf("check node existence failed: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("watched node %s does not exist", zkNodePrefix)
	}

	// Re-establish the watch
	_, _, watcher, err := r.conn.ChildrenW(zkNodePrefix)
	if err != nil {
		return nil, fmt.Errorf("re-watch failed: %w", err)
	}
	return watcher, nil
}

// CleanupExpiredNodes is a no-op for ZooKeeper as ephemeral nodes are automatically cleaned up
func (r *zookeeperRegistry) CleanupExpiredNodes(ctx context.Context, timeout time.Duration) error {
	// ZooKeeper's ephemeral nodes are automatically deleted when the session ends.
	// No explicit cleanup action is required from the client side for these nodes.
	return nil
}

// PutTask creates or updates a task's metadata in ZooKeeper.
// Task metadata is stored as JSON under zkTasksPrefix/[taskName].
func (r *zookeeperRegistry) PutTask(ctx context.Context, task TaskMeta) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encountered an issue serializing task data to JSON for ZooKeeper storage: %v", err)
	}

	taskPath := path.Join(zkTasksPrefix, task.Name)
	exists, _, err := r.conn.Exists(taskPath)
	if err != nil && !errors.Is(err, zk.ErrNoNode) {
		return fmt.Errorf("unable to verify task existence in ZooKeeper at path %s: %v", taskPath, err)
	}

	if exists {
		_, err = r.conn.Set(taskPath, data, -1)
	} else {
		_, err = r.conn.Create(taskPath, data, 0, r.acl)
	}

	return err
}

// GetAllTasks retrieves metadata for all tasks stored in ZooKeeper.
// It lists children under zkTasksPrefix and fetches data for each task.
func (r *zookeeperRegistry) GetAllTasks(ctx context.Context) (map[string]TaskMeta, error) {
	children, _, err := r.conn.Children(zkTasksPrefix)
	if err != nil {
		if errors.Is(err, zk.ErrNoNode) {
			// If the parent task node doesn't exist, there are no tasks.
			return make(map[string]TaskMeta), nil
		}
		return nil, fmt.Errorf("error retrieving task list from ZooKeeper, cannot get children of tasks path: %v", err)
	}

	tasks := make(map[string]TaskMeta)
	for _, child := range children {
		fullPath := path.Join(zkTasksPrefix, child)
		data, _, err := r.conn.Get(fullPath)
		if err != nil {
			continue
		}

		var task TaskMeta
		if err := json.Unmarshal(data, &task); err != nil {
			continue
		}

		tasks[task.Name] = task
	}

	return tasks, nil
}

func (r *zookeeperRegistry) WatchTaskEvent(ctx context.Context) (<-chan TaskMetaEvent, error) {
	eventChan := make(chan TaskMetaEvent, TaskEventChannelSize)

	// Initial fetch of task children and establishment of a watch on the parent node (zkTasksPrefix).
	tasks, _, childWatch, err := r.conn.ChildrenW(zkTasksPrefix)
	if err != nil {
		close(eventChan)
		return nil, fmt.Errorf("error establishing watch on ZooKeeper tasks path for task status changes: %v", err)
	}

	// maintain current child node status snapshot
	currentTasks := make(map[string]struct{})
	for _, task := range tasks {
		currentTasks[task] = struct{}{}

		// get initial data for each child node
		fullPath := path.Join(zkTasksPrefix, task)
		data, _, err := r.conn.Get(fullPath)
		if err != nil {
			continue
		}

		var taskMeta TaskMeta
		if err := json.Unmarshal(data, &taskMeta); err != nil {
			continue
		}

		eventChan <- TaskMetaEvent{
			Type: TaskEventTypePut,
			Task: taskMeta,
		}
	}

	go func() {
		defer close(eventChan)

		for {
			select {
			case <-ctx.Done():
				return
			case event := <-childWatch:
				if event.Err != nil {
					continue
				}

				switch event.Type {
				case zk.EventNodeChildrenChanged:
					// handle child node list change (addition or deletion)
					newChildren, _, newChildWatch, err := r.conn.ChildrenW(zkTasksPrefix)
					if err != nil {
						childWatch = newChildWatch
						continue
					}

					// create new children set
					newChildrenSet := make(map[string]struct{})
					for _, child := range newChildren {
						newChildrenSet[child] = struct{}{}
					}

					// check for new children
					for child := range newChildrenSet {
						if _, exists := currentTasks[child]; !exists {
							fullPath := path.Join(zkTasksPrefix, child)
							data, _, err := r.conn.Get(fullPath)
							if err != nil {
								continue
							}

							var taskMeta TaskMeta
							if err := json.Unmarshal(data, &taskMeta); err != nil {
								continue
							}

							eventChan <- TaskMetaEvent{
								Type: TaskEventTypePut,
								Task: taskMeta,
							}
						}
					}

					// check for deleted children
					for child := range currentTasks {
						if _, exists := newChildrenSet[child]; !exists {
							eventChan <- TaskMetaEvent{
								Type: TaskEventTypeDelete,
								Task: TaskMeta{Name: child},
							}
						}
					}

					currentTasks = newChildrenSet
					childWatch = newChildWatch

				case zk.EventNodeDataChanged:
					// handle child node data change
					childName := path.Base(event.Path)
					if _, exists := currentTasks[childName]; exists {
						data, _, err := r.conn.Get(event.Path)
						if err != nil {
							continue
						}

						var taskMeta TaskMeta
						if err := json.Unmarshal(data, &taskMeta); err != nil {
							continue
						}

						eventChan <- TaskMetaEvent{
							Type: TaskEventTypePut,
							Task: taskMeta,
						}
					}

				case zk.EventNodeDeleted:
					// handle child node deletion
					childName := path.Base(event.Path)
					if _, exists := currentTasks[childName]; exists {
						delete(currentTasks, childName)
						eventChan <- TaskMetaEvent{
							Type: TaskEventTypeDelete,
							Task: TaskMeta{Name: childName},
						}
					}
				}
			}
		}
	}()

	return eventChan, nil
}

// MarkTaskDeleted moves a task from the active tasks list (zkTasksPrefix)
// to a deleted tasks list (zkDeletedTasks) in a single transaction.
// This is an atomic operation using ZooKeeper's multi-op feature.
func (r *zookeeperRegistry) MarkTaskDeleted(ctx context.Context, taskName string) error {
	deletedPath := path.Join(zkDeletedTasks, taskName)

	// Directly create a node under the deleted tasks' path.
	// The existence of this node signifies that the task is marked as deleted.
	// We use Create with Flags 0 (persistent node) and WorldACL(PermAll).
	// If the node already exists (task was already marked deleted), zk.ErrNodeExists will be returned,
	// which we can ignore as the desired state (marked deleted) is already achieved.
	_, err := r.conn.Create(deletedPath, []byte{}, 0, r.acl)
	if err != nil && !errors.Is(err, zk.ErrNodeExists) {
		return fmt.Errorf("problem marking task %s as deleted in ZooKeeper, cannot create deletion marker: %v", taskName, err)
	}

	return nil
}

func (r *zookeeperRegistry) GetDeletedTaskNames(ctx context.Context) []string {

	deletedTasks, _, err := r.conn.Children(path.Clean(zkDeletedTasks))
	if err != nil {
		if errors.Is(err, zk.ErrNoNode) {
			return nil
		}
		// Log other errors but return an empty list to avoid disrupting callers.
		logger.Errorf("Error getting deleted task names from %s: %v", zkDeletedTasks, err)
		return nil
	}

	return deletedTasks
}

// WatchDeletedTaskEvent watches for changes in the deleted tasks list.
func (r *zookeeperRegistry) WatchDeletedTaskEvent(ctx context.Context) (<-chan struct{}, error) {
	eventChan := make(chan struct{}, TaskEventChannelSize)

	// ensure the path for deleted tasks exists
	exists, _, err := r.conn.Exists(zkDeletedTasks)
	if err != nil {
		return nil, fmt.Errorf("error checking ZooKeeper deleted tasks path existence: %v", err)
	}
	if !exists {
		_, err := r.conn.Create(zkDeletedTasks, []byte{}, 0, r.acl)
		if err != nil && !errors.Is(err, zk.ErrNodeExists) {
			return nil, fmt.Errorf("error creating ZooKeeper path for deleted tasks tracking: %v", err)
		}
	}

	// set watch on the deleted tasks path
	_, _, childWatch, err := r.conn.ChildrenW(zkDeletedTasks)
	if err != nil {
		return nil, fmt.Errorf("error establishing watch on ZooKeeper deleted tasks path: %v", err)
	}

	// start a goroutine to listen for changes to the children
	go func() {
		defer close(eventChan)

		// send initial event
		select {
		case eventChan <- struct{}{}:
		case <-ctx.Done():
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case event := <-childWatch:
				if event.Type == zk.EventNodeChildrenChanged {
					// reset watch
					_, _, newChildWatch, err := r.conn.ChildrenW(zkDeletedTasks)
					if err != nil {
						logger.Errorf("Failed to re-establish watch on deleted tasks in ZooKeeper - retry will be attempted: %v", err)
						continue
					}
					childWatch = newChildWatch

					// send event notification
					select {
					case eventChan <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return eventChan, nil
}

// ForceCleanupTask removes all metadata associated with a task
// Including task meta, deleted tasks record and last execution time
func (r *zookeeperRegistry) ForceCleanupTask(ctx context.Context, taskName string) error {
	taskPath := path.Join(zkTasksPrefix, taskName)
	deletedPath := path.Join(zkDeletedTasks, taskName)
	execPath := path.Join(zkTaskLastExec, taskName)

	// Ignore any errors if nodes don't exist
	_ = r.conn.Delete(taskPath, -1)
	_ = r.conn.Delete(deletedPath, -1)
	_ = r.conn.Delete(execPath, -1)
	return nil
}

func (r *zookeeperRegistry) ForceCleanupAllTasks(ctx context.Context) error {
	// cleanup tasks
	children, _, err := r.conn.Children(zkTasksPrefix)
	if err == nil {
		for _, child := range children {
			_ = r.deleteRecursively(zkTasksPrefix + "/" + child)
		}
	}

	// cleanup deleted_tasks
	children, _, err = r.conn.Children(zkDeletedTasks)
	if err == nil {
		for _, child := range children {
			_ = r.deleteRecursively(zkDeletedTasks + "/" + child)
		}
	}

	// cleanup deleted_tasks
	children, _, err = r.conn.Children(zkTaskLastExec)
	if err == nil {
		for _, child := range children {
			_ = r.deleteRecursively(zkTaskLastExec + "/" + child)
		}
	}
	return nil
}

// recursively delete a node and its children
func (r *zookeeperRegistry) deleteRecursively(path string) error {
	children, _, err := r.conn.Children(path)
	if err != nil {
		return r.conn.Delete(path, -1)
	}
	for _, child := range children {
		_ = r.deleteRecursively(path + "/" + child)
	}
	return r.conn.Delete(path, -1)
}

// CanRunTask checks if a task can be run at a given time.
// It uses a distributed lock to ensure only one node can run a task at a time.
func (r *zookeeperRegistry) CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error) {

	// task key format
	// /distributed_cron/last_exec/2025-05-28T19:42:00+08:00:task-every-5s-1

	taskKey := fmt.Sprintf("%s-%s", execTime.Format(time.RFC3339), taskName)
	key := path.Join(zkTaskLastExec, taskKey)

	_, err := r.conn.Create(key, []byte(""), 0, zk.WorldACL(zk.PermAll))
	if err != nil {
		if errors.Is(err, zk.ErrNodeExists) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create node, key:%s, err: %v", key, err)
	}

	err = r.cleaner.SendEvent(&Event{
		Key:  key,
		Time: execTime,
	})
	if err != nil {
		logger.Errorf("Failed to send task execution event notification for key '%v': %v", key, err)
	}

	return true, nil
}

func (r *zookeeperRegistry) RemoveTaskDeleted(ctx context.Context, taskName string) error {

	err := r.conn.Delete(path.Join(zkDeletedTasks, taskName), -1)
	if err != nil && !errors.Is(err, zk.ErrNoNode) {
		logger.Errorf("Failed to remove task '%s' from deleted tasks registry in ZooKeeper: %v", taskName, err)
		return fmt.Errorf("failed to remove task '%s' from deleted tasks registry in ZooKeeper: %v", taskName, err)
	}

	return nil
}

func (r *zookeeperRegistry) batchDeleteExecKeys(keys []string) error {

	// perform multi-operation transaction
	ops := make([]interface{}, len(keys))
	for i, key := range keys {
		ops[i] = &zk.DeleteRequest{Path: key, Version: -1} // Version -1 ignores version check
	}

	// expected to return the same number of results as the number of operations
	resp, err := r.conn.Multi(ops...)
	if err != nil {
		return fmt.Errorf("batch deletion failed: %v", err)
	}

	// verify results
	for i, r := range resp {
		if r.Error != nil {
			return fmt.Errorf("path %s delete failed, err: %v", keys[i], r.Error)
		}
	}

	logger.Infof("Batch deletion operation completed successfully: removed %d task execution keys from ZooKeeper", len(keys))
	return nil
}

// Start scheduled cleanup service
func (r *zookeeperRegistry) cleanupHistoryExecKeys(ctx context.Context) {
	ticker := time.NewTicker(CleanupHistoryExecKeysTickerDuration)
	defer ticker.Stop()

	// Execute cleanup immediately at startup
	if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
		logger.Errorf("Initial history cleanup operation failed during ZooKeeper registry initialization: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
				logger.Errorf("Periodic task execution history cleanup failed in ZooKeeper: %v", err)
			}
		case <-ctx.Done():
			logger.Errorf("Periodic cleanup service for ZooKeeper execution history has been stopped")
			return
		}
	}
}

func (r *zookeeperRegistry) tryCleanupHistoryExecKeys(ctx context.Context) error {

	children, _, err := r.conn.Children(zkTaskLastExec)
	if err != nil {
		logger.Errorf("Failed to list nodes for historical cleanup in ZooKeeper: %v", err)
		return err
	}

	var counter int
	for _, child := range children {
		fullPath := fmt.Sprintf("%s/%s", zkTaskLastExec, child)

		// Extract time part (20230102T030405:task-1 → 20230102T030405)
		timePart := child[:len(time.RFC3339)]
		nodeTime, err := time.Parse(time.RFC3339, timePart)
		if err != nil {
			logger.Errorf("child[%s] parse time err: %v", child, err)
			continue
		}

		if time.Since(nodeTime) > CleanupHistoryExecKeysThresholdDuration {
			if err = r.conn.Delete(fullPath, -1); err != nil {
				logger.Errorf("Failed to delete outdated execution history node '%s': %v", fullPath, err)
				continue
			}
			counter++
		}
	}

	logger.Infof("Successfully, %d historical data have been deleted from Zookeeper.", counter)

	return nil
}
