package dcron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Constants for etcd key prefixes and configuration
// These define the key structure for storing distributed cron data in etcd
const (
	etcdNodePrefix         = "/distributed_cron/nodes/"         // Prefix for storing node information (with node ID as suffix)
	etcdTaskPrefix         = "/distributed_cron/tasks/"         // Prefix for storing task metadata (with task name as suffix)
	etcdDeletedTaskPrefix  = "/distributed_cron/deleted_tasks/" // Prefix for marking tasks as deleted (with task name as suffix)
	etcdTaskLastExecPrefix = "/distributed_cron/last_exec/"     // Prefix for recording last execution times (with task name and timestamp as suffix)
	etcdLeaseTTL           = 15                                 // Lease time-to-live in seconds for node heartbeats
)

// Ensure etcdRegistry implements the Registry interface
var _ Registry = (*etcdRegistry)(nil)

// etcdRegistry implements the Registry interface using etcd as the backend storage
// etcd provides strong consistency guarantees with its distributed key-value store
type etcdRegistry struct {
	client  *clientv3.Client // etcd client connection for API operations
	lease   clientv3.LeaseID // lease ID for node registration to enable automatic expiration
	cleaner *EventCleaner    // Cleaner for removing old execution records
}

// NewEtcdRegistry creates a new Registry instance backed by etcd
// The etcd client should be properly configured with endpoints and authentication
func NewEtcdRegistry(client *clientv3.Client) Registry {
	return &etcdRegistry{client: client}
}

// Register registers a node in the etcd registry with a lease
// It stores node information and keeps the lease alive in a background goroutine
// The lease mechanism ensures automatic cleanup of node data if the process crashes
func (r *etcdRegistry) Register(ctx context.Context, node Node) error {
	// Create a lease for the node with the configured TTL
	// This lease will expire if not renewed, automatically removing the node
	resp, err := r.client.Grant(ctx, etcdLeaseTTL)
	if err != nil {
		return err
	}
	r.lease = resp.ID

	// Marshal node data to JSON for storage
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}

	// Store node information in etcd with the lease attached
	// When the lease expires, this key will be automatically deleted
	key := path.Join(etcdNodePrefix, node.ID)
	_, err = r.client.Put(ctx, key, string(data), clientv3.WithLease(r.lease))
	if err != nil {
		return err
	}

	// Keep the lease alive in a background goroutine
	// This ensures the node registration doesn't expire as long as the process is running
	// and can communicate with etcd
	kaCh, err := r.client.KeepAlive(ctx, r.lease)
	if err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				// Context canceled, stop renewing the lease
				return
			case _, ok := <-kaCh:
				if !ok {
					// Channel closed, lease can no longer be renewed
					return
				}
				// Successfully renewed lease
			}
		}
	}()

	// Initialize and start the event cleaner for managing execution records
	// The cleaner periodically removes stale execution records to prevent etcd from filling up
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

// Unregister removes a node from the etcd registry
// It deletes the node key and revokes the lease to clean up associated resources
func (r *etcdRegistry) Unregister(ctx context.Context, nodeID string) error {
	// Delete the node's key from etcd
	key := path.Join(etcdNodePrefix, nodeID)
	_, err := r.client.Delete(ctx, key)

	// Revoke the lease to ensure it's deleted and all associated keys are removed
	// This is important for cleanup even if the node key deletion failed
	_, err = r.client.Revoke(ctx, r.lease)

	return err
}

// UpdateStatus updates the status of a node in the registry
// It uses the updateNodeField helper function to atomically modify just the status field
func (r *etcdRegistry) UpdateStatus(ctx context.Context, nodeID string, status NodeStatus) error {
	return r.updateNodeField(ctx, nodeID, func(node *Node) {
		node.Status = status // Update the node's status
	})
}

// UpdateHeartbeat updates the LastAlive timestamp of a node to the current time
// This is used for node liveness detection in the distributed system
func (r *etcdRegistry) UpdateHeartbeat(ctx context.Context, nodeID string) error {
	return r.updateNodeField(ctx, nodeID, func(node *Node) {
		node.LastAlive = time.Now() // Update the last alive timestamp
	})
}

// updateNodeField is a helper function that atomically updates a field of a node
// It uses etcd's STM (Software Transactional Memory) to ensure atomic updates
// This prevents race conditions when multiple processes update the same node
func (r *etcdRegistry) updateNodeField(ctx context.Context, nodeID string, updateFn func(*Node)) error {
	key := path.Join(etcdNodePrefix, nodeID)

	// Use STM (Software Transactional Memory) to update node fields atomically
	// STM will retry the transaction if there are conflicts with other updates
	_, err := concurrency.NewSTM(r.client, func(stm concurrency.STM) error {
		value := stm.Get(key)
		if value == "" {
			return errors.New("node not found") // Return error if node not found
		}

		var node Node
		if err := json.Unmarshal([]byte(value), &node); err != nil {
			return err
		}

		updateFn(&node) // Call the update function to modify the node

		newData, err := json.Marshal(node)
		if err != nil {
			return err
		}

		stm.Put(key, string(newData)) // Update the node in etcd
		return nil
	})

	return err
}

// GetNodes retrieves all nodes from the etcd registry
// It returns a list of all node objects by querying the node prefix
func (r *etcdRegistry) GetNodes(ctx context.Context) ([]Node, error) {
	// Get all keys with the node prefix
	resp, err := r.client.Get(ctx, etcdNodePrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	// Parse each key's value into a Node object
	nodes := make([]Node, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var node Node
		if err := json.Unmarshal(kv.Value, &node); err != nil {
			continue
		}
		nodes = append(nodes, node) // Append node to the list
	}

	return nodes, nil
}

// GetWorkingNodes retrieves all nodes that are currently working
// It filters the nodes based on their status being NodeStatusWorking
func (r *etcdRegistry) GetWorkingNodes(ctx context.Context) ([]Node, error) {
	nodes, err := r.GetNodes(ctx)
	if err != nil {
		return nil, err
	}

	// Filter nodes to include only those with working status
	workingNodes := make([]Node, 0)
	for _, node := range nodes {
		if node.Status == NodeStatusWorking {
			workingNodes = append(workingNodes, node) // Append working node to the list
		}
	}

	return workingNodes, nil
}

// WatchNodes sets up a watch on node changes in etcd and returns a channel of NodeEvents
// The channel will receive events when nodes are added, updated, or deleted
// This enables real-time reaction to cluster membership changes
func (r *etcdRegistry) WatchNodes(ctx context.Context) (<-chan NodeEvent, error) {
	// Watch for changes to node information in etcd using the prefix
	watchChan := r.client.Watch(ctx, etcdNodePrefix, clientv3.WithPrefix())
	eventChan := make(chan NodeEvent, NodeEventChannelSize)

	eventChan <- NodeEvent{Type: NodeEventTypeChanged}

	go func() {
		defer close(eventChan)
		for resp := range watchChan {
			for _, ev := range resp.Events {
				var event NodeEvent
				switch ev.Type {
				case clientv3.EventTypePut:
					event.Type = NodeEventTypePut
					var node Node
					if err := json.Unmarshal(ev.Kv.Value, &node); err != nil {
						continue
					}
					event.Node = node
				case clientv3.EventTypeDelete:
					// For delete events, only the nodeID can be obtained from the key
					event.Type = NodeEventTypeDelete
					nodeID := path.Base(string(ev.Kv.Key))
					event.Node = Node{ID: nodeID}
				}
				eventChan <- event // Send the event to the channel
			}
		}
	}()

	return eventChan, nil
}

// CleanupExpiredNodes does not require explicit cleanup for etcd; expired nodes are handled automatically by lease expiration.
func (r *etcdRegistry) CleanupExpiredNodes(ctx context.Context, timeout time.Duration) error {
	return nil
}

// PutTask stores or updates task metadata in etcd
func (r *etcdRegistry) PutTask(ctx context.Context, task TaskMeta) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}

	key := path.Join(etcdTaskPrefix, task.Name)
	_, err = r.client.Put(ctx, key, string(data)) // Store task metadata in etcd
	return err
}

// GetAllTasks retrieves all tasks from etcd
func (r *etcdRegistry) GetAllTasks(ctx context.Context) (map[string]TaskMeta, error) {
	resp, err := r.client.Get(ctx, etcdTaskPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	tasks := make(map[string]TaskMeta)
	for _, kv := range resp.Kvs {
		var task TaskMeta
		if err := json.Unmarshal(kv.Value, &task); err != nil {
			continue
		}
		tasks[task.Name] = task // Map task name to task metadata
	}

	return tasks, nil
}

// WatchTaskEvent sets up a watch on task metadata changes and returns a channel of TaskMetaEvents
func (r *etcdRegistry) WatchTaskEvent(ctx context.Context) (<-chan TaskMetaEvent, error) {
	eventChan := make(chan TaskMetaEvent, 100)

	// Get current tasks for initial state
	resp, err := r.client.Get(ctx, etcdTaskPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	// Send initial status
	go func() {
		for _, kv := range resp.Kvs {
			var task TaskMeta
			if err := json.Unmarshal(kv.Value, &task); err == nil {
				eventChan <- TaskMetaEvent{
					Type: TaskEventTypePut,
					Task: task, // Send initial task event
				}
			}
		}

		// Start watching for changes to task metadata
		watchChan := r.client.Watch(ctx, etcdTaskPrefix, clientv3.WithPrefix())
		for {
			select {
			case resp := <-watchChan:
				for _, ev := range resp.Events {
					var event TaskMetaEvent
					taskName := strings.TrimPrefix(string(ev.Kv.Key), etcdTaskPrefix)

					switch ev.Type {
					case mvccpb.PUT:
						var task TaskMeta
						if err := json.Unmarshal(ev.Kv.Value, &task); err == nil {
							event = TaskMetaEvent{
								Type: TaskEventTypePut,
								Task: task, // Send task update event
							}
						}
					case mvccpb.DELETE:
						event = TaskMetaEvent{
							Type: TaskEventTypeDelete,
							Task: TaskMeta{Name: taskName}, // Send task delete event
						}
					}

					if event.Type != "" {
						eventChan <- event // Send the event to the channel
					}
				}
			case <-ctx.Done():
				close(eventChan) // Close the channel on context done
				return
			}
		}
	}()

	return eventChan, nil
}

// MarkTaskDeleted marks a task as deleted in etcd
func (r *etcdRegistry) MarkTaskDeleted(ctx context.Context, taskName string) error {
	// Construct operation path
	// taskKey := path.Join(etcdTaskPrefix, taskName)
	deletedKey := path.Join(etcdDeletedTaskPrefix, taskName)

	// Directly put a key to mark the task as deleted.
	// The value can be empty; the existence of the key signifies deletion.
	_, err := r.client.Put(ctx, deletedKey, "")
	if err != nil {
		return fmt.Errorf("error when marking task %s as deleted in etcd: %w", taskName, err)
	}

	// Log the operation result (optional, based on original logging)
	logger.Infof("Task '%s' deletion marker successfully created at etcd path: %s", taskName, deletedKey)

	return nil
}

// GetDeletedTaskNames retrieves the names of all deleted tasks
func (r *etcdRegistry) GetDeletedTaskNames(ctx context.Context) []string {
	resp, err := r.client.Get(ctx, etcdDeletedTaskPrefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		logger.Errorf("Unable to retrieve deleted tasks from etcd with prefix '%s': %v", etcdDeletedTaskPrefix, err)
		return nil
	}

	deletedTasks := make([]string, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		deletedTask := strings.TrimPrefix(string(kv.Key), etcdDeletedTaskPrefix)
		deletedTasks = append(deletedTasks, deletedTask) // Append deleted task name to the list
	}

	return deletedTasks
}

// WatchDeletedTaskEvent sets up a watch on deleted task changes and returns a channel of events
func (r *etcdRegistry) WatchDeletedTaskEvent(ctx context.Context) (<-chan struct{}, error) {
	eventChan := make(chan struct{}, TaskEventChannelSize)

	// Watch for all changes under the deleted task prefix
	watchChan := r.client.Watch(ctx, etcdDeletedTaskPrefix, clientv3.WithPrefix())

	go func() {
		defer close(eventChan)

		// Send an initial event
		select {
		case eventChan <- struct{}{}:
		case <-ctx.Done():
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case watchResp := <-watchChan:
				if watchResp.Canceled {
					return
				}

				// Send notification when any change occurs
				if len(watchResp.Events) > 0 {
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

func (r *etcdRegistry) CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error) {

	// task key format
	// /distributed_cron/last_exec/1748446976-task-every-7s-87

	key := etcdTaskLastExecPrefix + fmt.Sprintf("%d", execTime.Unix()) + "-" + taskName
	// create transaction
	txn := r.client.Txn(ctx)

	// transaction condition: key does not exist
	cond := clientv3.Compare(clientv3.Version(key), "=", 0)

	// transaction operation: if the condition is met (key does not exist), execute the put operation
	putOp := clientv3.OpPut(key, "")

	// commit transaction
	resp, err := txn.If(cond).Then(putOp).Else().Commit()
	if err != nil {
		return false, fmt.Errorf("etcd transaction failed during task execution attempt: %v", err)
	}

	if resp.Succeeded {
		err := r.cleaner.SendEvent(&Event{
			Key:  key,
			Time: execTime,
		})
		if err != nil {
			logger.Errorf("Failed to send event notification for key '%v': %v", key, err)
		}
	}

	return resp.Succeeded, nil
}

func (r *etcdRegistry) batchDeleteExecKeys(keys []string) error {
	ops := make([]clientv3.Op, len(keys))
	for i := 0; i < len(keys); i++ {
		ops[i] = clientv3.OpDelete(keys[i])
	}

	// Use a transaction to delete all keys
	_, err := r.client.Txn(context.Background()).Then(ops...).Commit()
	if err != nil {
		logger.Errorf("Batch delete operation failed in etcd key-value store: %v", err)
		return fmt.Errorf("problem deleting historical execution keys from etcd, cleanup operation incomplete: %v", err)
	}
	logger.Infof("Successfully batch deleted %d keys from etcd key-value store", len(keys))
	return nil
}

func (r *etcdRegistry) cleanupHistoryExecKeys(ctx context.Context) {
	// task key format
	// /distributed_cron/last_exec/2025-05-28T19:42:00+08:00:task-every-5s-1

	ticker := time.NewTicker(CleanupHistoryExecKeysTickerDuration)
	defer ticker.Stop()

	if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
		logger.Errorf("Historical execution key cleanup operation failed in etcd: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
				logger.Errorf("Historical execution key cleanup operation failed in etcd background routine: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}
func (r *etcdRegistry) tryCleanupHistoryExecKeys(ctx context.Context) error {

	// one year ago of data key
	startKey := etcdTaskLastExecPrefix + fmt.Sprintf("%d", time.Now().Add(-365*24*time.Hour).Unix())
	// ten minutes of data key
	endKey := etcdTaskLastExecPrefix + fmt.Sprintf("%d", time.Now().Add(-CleanupHistoryExecKeysThresholdDuration).Unix())

	logger.Infof("Starting cleanup of historical execution keys older than timestamp %s", endKey)

	// Perform range deletion (use WithRange to ensure only keys in the range are deleted)
	resp, err := r.client.Delete(ctx, startKey, clientv3.WithRange(endKey))
	if err != nil {
		logger.Errorf("Range deletion operation failed when cleaning up historical execution keys: %v", err)
		return fmt.Errorf("problem deleting historical execution keys from etcd, cleanup operation incomplete: %v", err)
	}

	logger.Infof("Historical execution key cleanup completed successfully: removed %d keys older than %s", resp.Deleted, endKey)
	return nil
}

// ForceCleanupTask forcefully removes all metadata related to a task
func (r *etcdRegistry) ForceCleanupTask(ctx context.Context, taskName string) error {
	// Forcefully remove all metadata related to a task: task meta, deleted marker, and last execution time
	keyTask := path.Join(etcdTaskPrefix, taskName)
	keyDeleted := path.Join(etcdDeletedTaskPrefix, taskName)
	keyExec := etcdTaskLastExecPrefix + taskName

	ops := []clientv3.Op{
		clientv3.OpDelete(keyTask),    // Delete task metadata
		clientv3.OpDelete(keyDeleted), // Delete deleted marker
		clientv3.OpDelete(keyExec),    // Delete last execution time
	}

	_, err := r.client.Txn(ctx).Then(ops...).Commit() // Commit the transaction
	return err
}

// ForceCleanupAllTasks cleans up all tasks and their metadata
func (r *etcdRegistry) ForceCleanupAllTasks(ctx context.Context) error {
	// cleanup tasks
	_, err := r.client.Delete(ctx, etcdTaskPrefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	// cleanup deleted_tasks
	_, err = r.client.Delete(ctx, etcdDeletedTaskPrefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	// cleanup last_exec
	_, err = r.client.Delete(ctx, etcdTaskLastExecPrefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	return nil
}

func (r *etcdRegistry) RemoveTaskDeleted(ctx context.Context, taskName string) error {

	deletedKey := path.Join(etcdDeletedTaskPrefix, taskName)
	_, err := r.client.Delete(ctx, deletedKey)
	if err != nil {
		return err
	}

	return nil
}
