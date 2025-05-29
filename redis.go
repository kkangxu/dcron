package dcron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisKeyNodes              = "distributed_cron:nodes"                 // type sort set, Key for storing node information with score based on timestamp
	redisKeyTasks              = "distributed_cron:tasks"                 // type hash, Key for storing task metadata with task name as field
	redisKeyDeletedTasks       = "distributed_cron:deleted_tasks"         // type hash, Key for storing deleted task markers to prevent re-execution
	redisChannelNodes          = "distributed_cron:nodes_changes"         // type pubsub, Channel for publishing node change events for real-time notifications
	redisChannelTasks          = "distributed_cron:tasks_changes"         // type pubsub, Channel for publishing task change events for real-time notifications
	redisChannelDeletedTasks   = "distributed_cron:deleted_tasks_changes" // type pubsub, Channel for publishing deleted task change events
	redisKeyTaskLastExecPrefix = "distributed_cron:last_exec:"            // type string, Prefix for last execution time records to prevent duplicate runs
)

var _ Registry = (*redisRegistry)(nil) // Ensure redisRegistry implements the Registry interface

// redisRegistry implements the Registry interface using Redis as the backend
// Redis provides fast in-memory storage with persistence and pub/sub capabilities
type redisRegistry struct {
	client *redis.Client // Redis client for interacting with Redis server
}

// NewRedisRegistry creates a new instance of redisRegistry with the provided Redis client.
// This registry uses Redis data structures for distributed coordination among nodes.
func NewRedisRegistry(client *redis.Client) Registry {
	return &redisRegistry{client: client}
}

// Register adds a new node to the Redis registry.
// Nodes are stored in a sorted set with timestamp as score for efficient expiration checks.
// A node event is published to notify other nodes about the new addition.
func (r *redisRegistry) Register(ctx context.Context, node Node) error {
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}

	// Add node to sorted set with current timestamp as score
	// This allows for efficient range queries based on time
	_, err = r.client.ZAdd(ctx, redisKeyNodes, redis.Z{
		Score:  float64(time.Now().UnixNano()),
		Member: data,
	}).Result()
	if err != nil {
		return err
	}

	// Publish a node event to notify other nodes about this registration
	return r.publishNodeEvent(ctx, NodeEventTypePut, node)
}

// Unregister removes a node from the Redis registry.
// It searches for the node by ID in the sorted set and removes it if found.
// A node event is published to notify other nodes about the removal.
func (r *redisRegistry) Unregister(ctx context.Context, nodeID string) error {
	nodes, err := r.getNodesData(ctx)
	if err != nil {
		return err
	}

	// Iterate through all node data to find the one with matching ID
	for _, data := range nodes {
		var node Node
		if err := json.Unmarshal([]byte(data), &node); err != nil {
			continue
		}
		if node.ID == nodeID {
			// Remove the node from the sorted set
			_, err = r.client.ZRem(ctx, redisKeyNodes, data).Result()
			if err != nil {
				return err
			}
			// Publish a node deletion event to notify other nodes
			return r.publishNodeEvent(ctx, NodeEventTypeDelete, node)
		}
	}

	// Node not found but not treated as an error
	// It might have been already unregistered
	return nil
}

// UpdateStatus updates the status of a node in the registry.
// It uses the updateNodeField helper function to atomically update just the status field.
func (r *redisRegistry) UpdateStatus(ctx context.Context, nodeID string, status NodeStatus) error {
	return r.updateNodeField(ctx, nodeID, func(node *Node) {
		node.Status = status
	})
}

// UpdateHeartbeat updates the LastAlive timestamp of a node to the current time.
// This is used for liveness detection in the distributed system.
func (r *redisRegistry) UpdateHeartbeat(ctx context.Context, nodeID string) error {
	return r.updateNodeField(ctx, nodeID, func(node *Node) {
		node.LastAlive = time.Now()
	})
}

// updateNodeField is a helper function that atomically updates a field of a node.
// It implements optimistic concurrency control by removing the old data and adding new data.
// This is a critical function for maintaining consistency in the distributed system.
func (r *redisRegistry) updateNodeField(ctx context.Context, nodeID string, updateFn func(*Node)) error {
	nodes, err := r.getNodesData(ctx)
	if err != nil {
		return err
	}

	for _, data := range nodes {
		var node Node
		if err := json.Unmarshal([]byte(data), &node); err != nil {
			continue
		}
		if node.ID == nodeID {
			// Remove the existing node data
			if _, err := r.client.ZRem(ctx, redisKeyNodes, data).Result(); err != nil {
				return err
			}

			// Apply the update function to modify the node
			updateFn(&node)

			// Serialize the updated node
			newData, err := json.Marshal(node)
			if err != nil {
				return err
			}

			// Add the updated node back with a fresh timestamp
			_, err = r.client.ZAdd(ctx, redisKeyNodes, redis.Z{
				Score:  float64(time.Now().UnixNano()),
				Member: newData,
			}).Result()
			if err != nil {
				return err
			}

			// Publish a node update event
			return r.publishNodeEvent(ctx, NodeEventTypePut, node)
		}
	}

	return errors.New("node not found")
}

// GetNodes retrieves all nodes from the Redis registry.
// It deserializes each node data from the sorted set into Node objects.
func (r *redisRegistry) GetNodes(ctx context.Context) ([]Node, error) {
	nodesData, err := r.getNodesData(ctx)
	if err != nil {
		return nil, err
	}

	nodes := make([]Node, 0, len(nodesData))
	for _, data := range nodesData {
		var node Node
		if err := json.Unmarshal([]byte(data), &node); err != nil {
			continue
		}
		nodes = append(nodes, node)
	}

	return nodes, nil
}

// GetWorkingNodes retrieves all nodes that are currently working.
// It filters the nodes based on their status being NodeStatusWorking.
func (r *redisRegistry) GetWorkingNodes(ctx context.Context) ([]Node, error) {
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

// WatchNodes sets up a watch on node changes in Redis and returns a channel of NodeEvents.
// It uses Redis Pub/Sub to receive real-time notifications about node changes.
// The events are deserialized and sent to the returned channel.
func (r *redisRegistry) WatchNodes(ctx context.Context) (<-chan NodeEvent, error) {
	pubsub := r.client.Subscribe(ctx, redisChannelNodes)
	eventChan := make(chan NodeEvent, NodeEventChannelSize)
	eventChan <- NodeEvent{Type: NodeEventTypeChanged}

	go func() {
		defer pubsub.Close()
		defer close(eventChan)

		ch := pubsub.Channel()
		for {
			select {
			case msg := <-ch:
				var event NodeEvent
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					continue
				}
				eventChan <- event
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventChan, nil
}

// CleanupExpiredNodes removes nodes whose last heartbeat is older than the timeout.
// It uses the sorted set's score (timestamp) to efficiently find and remove expired nodes.
func (r *redisRegistry) CleanupExpiredNodes(ctx context.Context, timeout time.Duration) error {
	minScore := float64(time.Now().Add(-timeout).UnixNano())
	removed, err := r.client.ZRemRangeByScore(ctx, redisKeyNodes, "0", fmt.Sprintf("%f", minScore)).Result()
	if err != nil {
		return err
	}

	if removed > 0 {
		nodes, err := r.GetNodes(ctx)
		if err == nil {
			for _, node := range nodes {
				_ = r.publishNodeEvent(ctx, NodeEventTypePut, node)
			}
		}
	}

	return nil
}

// PutTask stores or updates task metadata in Redis.
// Task metadata is stored in a hash with task name as field.
func (r *redisRegistry) PutTask(ctx context.Context, task TaskMeta) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}

	_, err = r.client.HSet(ctx, redisKeyTasks, task.Name, data).Result()
	if err != nil {
		return err
	}

	return r.publishTaskEvent(ctx, TaskEventTypePut, task)
}

// GetAllTasks retrieves all tasks from Redis.
// It deserializes each task data from the hash into TaskMeta objects.
func (r *redisRegistry) GetAllTasks(ctx context.Context) (map[string]TaskMeta, error) {
	var cursor uint64
	tasks := make(map[string]TaskMeta)

	for {
		keys, cursor, err := r.client.HScan(ctx, redisKeyTasks, cursor, "*", 100).Result()
		if err != nil {
			return nil, err
		}

		for i := 0; i < len(keys); i += 2 {
			field := keys[i]
			value := keys[i+1]

			var task TaskMeta
			if err := json.Unmarshal([]byte(value), &task); err != nil {
				continue
			}
			tasks[field] = task
		}

		if cursor == 0 {
			break
		}
	}

	tasksData, err := r.client.HGetAll(ctx, redisKeyTasks).Result()
	if err != nil {
		return nil, err
	}

	for name, data := range tasksData {
		var task TaskMeta
		if err := json.Unmarshal([]byte(data), &task); err != nil {
			continue
		}
		tasks[name] = task
	}

	return tasks, nil
}

// WatchTaskEvent sets up a watch on task changes in Redis and returns a channel of TaskMetaEvents.
// It uses Redis Pub/Sub to receive real-time notifications about task changes.
// The events are deserialized and sent to the returned channel.
func (r *redisRegistry) WatchTaskEvent(ctx context.Context) (<-chan TaskMetaEvent, error) {
	pubsub := r.client.Subscribe(ctx, redisChannelTasks)
	eventChan := make(chan TaskMetaEvent, TaskEventChannelSize)

	go func() {
		defer pubsub.Close()
		defer close(eventChan)

		ch := pubsub.Channel()
		for {
			select {
			case msg := <-ch:
				var event TaskMetaEvent
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					continue
				}
				eventChan <- event
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventChan, nil
}

// MarkTaskDeleted marks a task as deleted in Redis.
// It sets a field in the deleted tasks hash to prevent re-execution.
// A deleted task event is published to notify other nodes.
func (r *redisRegistry) MarkTaskDeleted(ctx context.Context, taskName string) error {

	// Mark the task as deleted by setting a field in the deleted tasks hash.
	_, err := r.client.HSet(ctx, redisKeyDeletedTasks, taskName, "").Result()
	if err != nil {
		return fmt.Errorf("unable to mark task %s as deleted: %v", taskName, err)
	}

	// Publish a general deleted task list change event.
	if err := r.client.Publish(ctx, redisChannelDeletedTasks, "").Err(); err != nil {
		logger.Errorf("unable to publish deleted task event for %s: %v", taskName, err)
		return err
	}

	return nil
}

// GetDeletedTaskNames retrieves the names of all deleted tasks.
// It returns the field names from the deleted tasks hash.
func (r *redisRegistry) GetDeletedTaskNames(ctx context.Context) []string {
	deletedTasks, err := r.client.HKeys(ctx, redisKeyDeletedTasks).Result()
	if err != nil {
		return nil
	}

	return deletedTasks
}

// WatchDeletedTaskEvent sets up a watch on deleted task changes and returns a channel of events.
// It uses Redis Pub/Sub to receive real-time notifications about deleted task changes.
// The events are sent to the returned channel.
func (r *redisRegistry) WatchDeletedTaskEvent(ctx context.Context) (<-chan struct{}, error) {
	eventChan := make(chan struct{}, TaskEventChannelSize)

	pubsub := r.client.Subscribe(ctx, redisChannelDeletedTasks)

	go func() {
		defer pubsub.Close()
		defer close(eventChan)

		select {
		case eventChan <- struct{}{}:
		case <-ctx.Done():
			return
		}

		ch := pubsub.Channel()
		for {
			select {
			case <-ch:
				select {
				case eventChan <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventChan, nil
}

// getNodesData retrieves all nodes data from Redis.
// It returns the member data from the sorted set.
func (r *redisRegistry) getNodesData(ctx context.Context) ([]string, error) {
	return r.client.ZRange(ctx, redisKeyNodes, 0, -1).Result()
}

// publishNodeEvent publishes a node event to the Redis channel.
// It serializes the event and publishes it to the node changes channel.
func (r *redisRegistry) publishNodeEvent(ctx context.Context, eventType NodeEventType, node Node) error {
	event := NodeEvent{
		Type: eventType,
		Node: node,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, redisChannelNodes, data).Err()
}

// publishTaskEvent publishes a task event to the Redis channel.
// It serializes the event and publishes it to the task changes channel.
func (r *redisRegistry) publishTaskEvent(ctx context.Context, eventType TaskEventType, task TaskMeta) error {
	event := TaskMetaEvent{
		Type: eventType,
		Task: task,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, redisChannelTasks, data).Err()
}

// ForceCleanupTask forcefully removes all metadata related to a task: task meta, deleted marker, and last execution time.
// It uses a Redis pipeline to atomically remove the data.
func (r *redisRegistry) ForceCleanupTask(ctx context.Context, taskName string) error {
	keyTask := redisKeyTasks
	keyDeleted := redisKeyDeletedTasks
	keyExec := redisKeyTaskLastExecPrefix + taskName

	_, err := r.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		_ = pipe.HDel(ctx, keyTask, taskName)
		_ = pipe.HDel(ctx, keyDeleted, taskName)
		_ = pipe.Del(ctx, keyExec)
		return nil
	})
	return err
}

// ForceCleanupAllTasks forcefully cleans up all tasks and their metadata.
// It removes all data from the tasks and deleted tasks hashes.
func (r *redisRegistry) ForceCleanupAllTasks(ctx context.Context) error {
	_, err := r.client.Del(ctx, redisKeyTasks, redisKeyDeletedTasks, redisKeyDeletedTasks).Result()
	return err
}

// CanRunTask checks if a task can be run at the specified execution time.
// It uses a Redis lock to prevent concurrent execution.
func (r *redisRegistry) CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error) {
	key := redisKeyTaskLastExecPrefix + taskName + ":" + execTime.Format(time.RFC3339)

	success, err := r.client.SetNX(ctx, key, "", 5*time.Second).Result()
	if err != nil {
		return false, fmt.Errorf("issue encountered while setting lock: %v", err)
	}

	return success, nil
}

func (r *redisRegistry) RemoveTaskDeleted(ctx context.Context, taskName string) error {

	_, err := r.client.Del(ctx, redisKeyDeletedTasks).Result()
	return err

}
