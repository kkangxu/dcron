package dcron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
)

// Consul-specific constants for service registration and KV storage
const (
	consulServiceName         = "distributed_cron"                // Name of the service in Consul
	consulKVTasksPrefix       = "distributed_cron/tasks/"         // KV prefix for storing tasks
	consulKVDeletedTaskPrefix = "distributed_cron/deleted_tasks/" // KV prefix for storing deleted tasks
	consulKVTaskLastExec      = "distributed_cron/last_exec/"     // KV prefix for storing last execution time
	consulSessionTTL          = "15s"                             // TTL for Consul sessions
	consulCheckTimeout        = "10s"                             // Timeout for health checks
)

// Ensure consulRegistry implements the Registry interface
var _ Registry = (*consulRegistry)(nil)

// consulRegistry implements the Registry interface using Consul as the backend
type consulRegistry struct {
	client      *api.Client   // Consul API client
	sessionID   string        // Consul session ID for this node
	serviceID   string        // Service ID in Consul
	serviceName string        // Service name in Consul
	cleaner     *EventCleaner // Event Cleaner: Specialized to clear temporary flag data during task execution
}

// NewConsulRegistry creates a new Registry instance backed by Consul
func NewConsulRegistry(client *api.Client) Registry {
	return &consulRegistry{
		client:      client,
		serviceName: consulServiceName,
	}
}

// Register registers a node in the Consul registry
// It creates a session, registers a service, and maintains the session in a background goroutine
func (r *consulRegistry) Register(ctx context.Context, node Node) error {
	// create session
	sessionEntry := &api.SessionEntry{
		Name:      fmt.Sprintf("%s-session", node.ID),
		TTL:       consulSessionTTL,
		LockDelay: 5 * time.Second,
		Behavior:  api.SessionBehaviorDelete,
	}

	sessionID, _, err := r.client.Session().Create(sessionEntry, nil)
	if err != nil {
		return fmt.Errorf("unable to create Consul session for maintaining node registration: %v", err)
	}
	r.sessionID = sessionID

	// register node
	r.serviceID = node.ID
	registration := &api.AgentServiceRegistration{
		ID:   r.serviceID,
		Name: r.serviceName,
		Tags: []string{string(node.Status)},
		Meta: map[string]string{
			"ip":          node.IP,
			"hostname":    node.Hostname,
			"weight":      fmt.Sprintf("%d", node.Weight),
			"create_time": node.CreateTime.Format(time.RFC3339),
			"last_alive":  node.LastAlive.Format(time.RFC3339),
		},
		Check: &api.AgentServiceCheck{
			CheckID:                        fmt.Sprintf("%s-check", r.serviceID),
			TTL:                            consulCheckTimeout,
			DeregisterCriticalServiceAfter: "1m",
		},
	}

	if err := r.client.Agent().ServiceRegister(registration); err != nil {
		return fmt.Errorf("service '%s' registration in Consul failed, check Consul connection status: %v", node.ID, err)
	}

	// maintain the Consul session by periodic renewal
	go r.maintainSession(ctx)

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

// maintainSession keeps the Consul session alive by periodically renewing it
// It also updates the health check TTL to keep the service healthy
func (r *consulRegistry) maintainSession(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _, err := r.client.Session().Renew(r.sessionID, nil)
			if err != nil {
				logger.Errorf("Session renewal failed for Consul session '%s' - node may be marked as unhealthy: %v", r.sessionID, err)
				continue
			}
			// update health checks
			if err := r.client.Agent().UpdateTTL(fmt.Sprintf("%s-check", r.serviceID), "", api.HealthPassing); err != nil {
				logger.Errorf("Health check TTL update failed for service '%s' - service may be marked as critical: %v", r.serviceID, err)
			}
		}
	}
}

// Unregister removes a node from the Consul registry
// it destroys the session and removes the service registration
func (r *consulRegistry) Unregister(ctx context.Context, nodeID string) error {
	if r.serviceID != "" {
		if err := r.client.Agent().ServiceDeregister(r.serviceID); err != nil {
			return fmt.Errorf("encountered issues while deregistering service '%s' from Consul registry: %v", r.serviceID, err)
		}
	}

	if r.sessionID != "" {
		if _, err := r.client.Session().Destroy(r.sessionID, nil); err != nil {
			return fmt.Errorf("could not destroy Consul session '%s', which may cause resource leakage: %v", r.sessionID, err)
		}
	}

	return nil
}

// UpdateStatus updates the status of a node in the Consul registry
// It modifies the service tags to reflect the new status
func (r *consulRegistry) UpdateStatus(ctx context.Context, nodeID string, status NodeStatus) error {
	// query services directly by node ID (instead of filtering by status)
	// serviceID == nodeID, in consul
	service, _, err := r.client.Agent().Service(nodeID, nil)
	if err != nil {
		return fmt.Errorf("error querying node '%s' information, service might not exist: %v", nodeID, err)
	}
	if service == nil {
		return fmt.Errorf("node '%s' does not exist in Consul registry, cannot update its status", nodeID)
	}

	var newTags []string
	for _, tag := range service.Tags {
		if !strings.HasPrefix(tag, string(NodeStatus(""))) {
			newTags = append(newTags, tag)
		}
	}
	newTags = append(newTags, string(status)) // add new status label
	service.Meta["last_alive"] = time.Now().Format(time.RFC3339)

	// update service registration information
	registration := &api.AgentServiceRegistration{
		ID:   nodeID,
		Name: r.serviceName, // use global service name
		Tags: newTags,
		Meta: service.Meta, // keep original metadata
		Check: &api.AgentServiceCheck{
			CheckID:                        fmt.Sprintf("%s-check", nodeID),
			TTL:                            consulCheckTimeout,
			DeregisterCriticalServiceAfter: "1m",
		},
	}

	if err := r.client.Agent().ServiceRegister(registration); err != nil {
		return fmt.Errorf("unable to update node '%s' service status to '%s': %v", nodeID, status, err)
	}
	return nil
}

// UpdateHeartbeat updates the TTL health check for a node
// This keeps the node marked as healthy in Consul
func (r *consulRegistry) UpdateHeartbeat(ctx context.Context, nodeID string) error {
	checkID := fmt.Sprintf("%s-check", nodeID)
	return r.client.Agent().UpdateTTL(checkID, "", api.HealthPassing)
}

// GetNodes retrieves all nodes registered with the service name
// Returns a list of Node objects with their status and metadata
func (r *consulRegistry) GetNodes(ctx context.Context) ([]Node, error) {
	services, _, err := r.client.Catalog().Service(r.serviceName, "", nil)
	if err != nil {
		return nil, fmt.Errorf("error retrieving service list from Consul catalog, node information unavailable: %v", err)
	}

	nodes := make([]Node, 0, len(services))
	for _, service := range services {
		var status NodeStatus
		if len(service.ServiceTags) > 0 {
			status = NodeStatus(service.ServiceTags[0])
		}

		weight, _ := strconv.Atoi(service.ServiceMeta["weight"])
		createTime, _ := time.Parse(time.RFC3339, service.ServiceMeta["create_time"])
		lastAlive, _ := time.Parse(time.RFC3339, service.ServiceMeta["last_alive"])

		nodes = append(nodes, Node{
			ID:         service.ServiceID,
			IP:         service.ServiceMeta["ip"],
			Hostname:   service.ServiceMeta["hostname"],
			Status:     status,
			Weight:     weight,
			CreateTime: createTime,
			LastAlive:  lastAlive,
		})
	}

	return nodes, nil
}

// GetWorkingNodes retrieves only nodes with "working" status.
// It filters nodes by both status tag and health check passing.
func (r *consulRegistry) GetWorkingNodes(ctx context.Context) ([]Node, error) {
	services, _, err := r.client.Health().Service(r.serviceName, string(NodeStatusWorking), true, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot obtain list of healthy services with status '%s', working node information unavailable: %v", string(NodeStatusWorking), err)
	}

	nodes := make([]Node, 0, len(services))
	for _, service := range services {
		var status NodeStatus
		if len(service.Service.Tags) > 0 {
			status = NodeStatus(service.Service.Tags[0])
		}

		// Ensure only working nodes are included
		if status != NodeStatusWorking {
			continue
		}

		weight, _ := strconv.Atoi(service.Service.Meta["weight"])
		lastAlive, _ := time.Parse(time.RFC3339, service.Service.Meta["last_alive"])
		createTime, _ := time.Parse(time.RFC3339, service.Service.Meta["create_time"])
		nodes = append(nodes, Node{
			ID:         service.Service.ID,
			IP:         service.Service.Meta["ip"],
			Hostname:   service.Service.Meta["hostname"],
			Status:     status,
			Weight:     weight,
			CreateTime: createTime,
			LastAlive:  lastAlive,
		})
	}

	return nodes, nil
}

// WatchNodes sets up a watch on the service catalog to monitor node changes.
// It returns a channel that receives NodeEvent objects when nodes are added, updated, or removed.
func (r *consulRegistry) WatchNodes(ctx context.Context) (<-chan NodeEvent, error) {
	// Create a channel for node events with a defined buffer size
	eventChan := make(chan NodeEvent, NodeEventChannelSize)
	eventChan <- NodeEvent{Type: NodeEventTypeChanged}

	// Define parameters for the Consul watch plan
	params := map[string]interface{}{
		"type":        "service",
		"service":     r.serviceName,
		"passingonly": true, // Only watch healthy services
	}

	// Parse the watch parameters to create a watch plan
	plan, err := watch.Parse(params)
	if err != nil {
		return nil, fmt.Errorf("service watch plan creation failed, cannot monitor node status changes: %w", err)
	}

	// status tracking: record the last snapshot of node status
	// snapshot stores the last known state of nodes to detect changes
	snapshot := make(map[string]Node)
	var snapshotLocker sync.Mutex // Mutex to protect access to the snapshot

	// Define the handler function for the watch plan
	plan.Handler = func(idx uint64, data interface{}) {
		services := data.([]*api.ServiceEntry) // Cast data to service entries

		snapshotLocker.Lock()
		defer snapshotLocker.Unlock()

		// build current node status
		// currentNodes holds the current state of nodes from the watch event
		currentNodes := make(map[string]Node)
		for _, s := range services {
			weight, _ := strconv.Atoi(s.Service.Meta["weight"])
			node := Node{
				ID:       s.Service.ID,
				IP:       s.Service.Meta["ip"],
				Hostname: s.Service.Meta["hostname"],
				Weight:   weight,
			}
			if len(s.Service.Tags) > 0 {
				node.Status = NodeStatus(s.Service.Tags[0])
			}
			currentNodes[node.ID] = node
		}

		// check for new/updated nodes
		// Iterate over current nodes to find new or updated ones
		for id, node := range currentNodes {
			if oldNode, exists := snapshot[id]; !exists {
				// add node: if node does not exist in snapshot, it's a new node
				select {
				case eventChan <- NodeEvent{Type: NodeEventTypePut, Node: node}: // Send Put event
				case <-ctx.Done(): // If context is cancelled, stop sending
					return
				}
			} else if !reflect.DeepEqual(oldNode, node) {
				// node updated: if node exists and is different, it's an updated node
				select {
				case eventChan <- NodeEvent{Type: NodeEventTypePut, Node: node}: // Send Put event for update
				case <-ctx.Done():
					return
				}
			}
		}

		// check for deleted nodes
		// Iterate over snapshot to find nodes that are no longer in currentNodes
		for id, node := range snapshot {
			if _, exists := currentNodes[id]; !exists {
				// Node is in snapshot but not in currentNodes, so it's deleted
				select {
				case eventChan <- NodeEvent{
					Type: NodeEventTypeDelete,
					Node: node, // Send Delete event
				}:
				case <-ctx.Done():
					return
				}
			}
		}

		// Update snapshot with the current state of nodes
		snapshot = currentNodes
	}

	// Run the watch plan in a new goroutine
	go func() {
		defer close(eventChan) // Close the event channel when the goroutine exits
		// RunWithClientAndHclog starts the watch plan; it blocks until the plan stops or an error occurs.
		if err := plan.RunWithClientAndHclog(r.client, nil); err != nil {
			// Log an error if the watch plan fails, unless it's due to context cancellation
			if !errors.Is(err, context.Canceled) {
				logger.Errorf("Consul node watch plan failed: %v", err)
			}
		}
	}()

	return eventChan, nil
}

// CleanupExpiredNodes removes nodes that haven't updated their health check.
// In Consul, this is handled automatically by the TTL check expiration, so this function is a no-op.
func (r *consulRegistry) CleanupExpiredNodes(ctx context.Context, timeout time.Duration) error {
	// Consul automatically cleans up unhealthy services based on TTL checks.
	// No additional explicit cleanup logic is required here.
	return nil
}

// PutTask stores a task in the Consul KV store.
// It serializes the task to JSON and stores it under the configured tasks prefix.
func (r *consulRegistry) PutTask(ctx context.Context, task TaskMeta) error {
	// Marshal the task metadata into JSON format
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("error serializing metadata for task '%s', cannot store to Consul KV: %v", task.Name, err)
	}

	// Create a KVPair object for storing in Consul
	kv := &api.KVPair{
		Key:   consulKVTasksPrefix + task.Name, // Key is prefix + task name
		Value: data,                            // Value is the JSON marshaled task
	}

	// Put the KVPair into Consul's KV store
	_, err = r.client.KV().Put(kv, nil)
	if err != nil {
		return fmt.Errorf("unable to write task '%s' data to Consul KV store, task metadata update failed: %v", task.Name, err)
	}
	return nil
}

// GetAllTasks retrieves all tasks from the Consul KV store.
// It returns a map of task name to TaskMeta objects.
func (r *consulRegistry) GetAllTasks(ctx context.Context) (map[string]TaskMeta, error) {
	// List all KVPairs under the tasks prefix
	pairs, _, err := r.client.KV().List(consulKVTasksPrefix, nil)
	if err != nil {
		return nil, fmt.Errorf("problem retrieving task list from Consul KV store, task metadata unavailable: %v", err)
	}

	// Initialize a map to store the tasks
	tasks := make(map[string]TaskMeta)
	// Iterate over the retrieved KVPairs
	for _, pair := range pairs {
		var task TaskMeta
		// Unmarshal the value (JSON) into a TaskMeta object
		if err := json.Unmarshal(pair.Value, &task); err != nil {
			logger.Warnf("Failed to unmarshal task data for key '%s', skipping: %v", pair.Key, err)
			continue // Skip if unmarshaling fails
		}
		tasks[task.Name] = task // Add the task to the map
	}

	return tasks, nil
}

// WatchTaskEvent sets up a watch on the tasks KV prefix to monitor task changes.
// It returns a channel that receives TaskMetaEvent objects when tasks are added, updated, or removed.
func (r *consulRegistry) WatchTaskEvent(ctx context.Context) (<-chan TaskMetaEvent, error) {
	// Create a channel for task events with a defined buffer size
	eventChan := make(chan TaskMetaEvent, TaskEventChannelSize)

	// maintain current key-value pair state snapshot
	// currentPairs stores the last known state of task KVPairs to detect changes
	currentPairs := make(map[string]*api.KVPair)

	// get all initial tasks
	// Retrieve the initial set of tasks to populate currentPairs and send initial events
	initialPairs, _, err := r.client.KV().List(consulKVTasksPrefix, nil)
	if err != nil {
		return nil, fmt.Errorf("error listing initial state for task watch, cannot establish task change monitoring: %v", err)
	}

	// send initial status events
	// Process initial tasks and send Put events for each
	for _, pair := range initialPairs {
		var task TaskMeta
		if err := json.Unmarshal(pair.Value, &task); err != nil {
			logger.Warnf("Failed to unmarshal initial task data for key '%s': %v", pair.Key, err)
			continue
		}

		currentPairs[pair.Key] = pair // Store in snapshot
		// Send Put event for the initial task
		select {
		case eventChan <- TaskMetaEvent{Type: TaskEventTypePut, Task: task}:
		case <-ctx.Done(): // If context is cancelled, stop processing
			close(eventChan)
			return nil, ctx.Err()
		}
	}

	// create watch plan
	// Define parameters for the Consul watch plan for key prefix
	params := map[string]interface{}{
		"type":   "keyprefix",
		"prefix": consulKVTasksPrefix,
	}

	// Parse the watch parameters to create a watch plan
	plan, err := watch.Parse(params)
	if err != nil {
		return nil, fmt.Errorf("task watch plan creation failed, cannot monitor task status changes: %v", err)
	}

	// Define the handler function for the watch plan
	plan.Handler = func(idx uint64, data interface{}) {
		newPairs := data.(api.KVPairs) // Cast data to KVPairs

		// create new snapshot
		// newPairMap holds the current state of task KVPairs from the watch event
		newPairMap := make(map[string]*api.KVPair)
		for _, pair := range newPairs {
			newPairMap[pair.Key] = pair
		}

		// check for new/updated keys
		// Iterate over new KVPairs to find new or updated tasks
		for key, newPair := range newPairMap {
			oldPair, exists := currentPairs[key]

			var eventType TaskEventType
			var task TaskMeta

			if err := json.Unmarshal(newPair.Value, &task); err != nil {
				logger.Warnf("Failed to unmarshal task data during watch for key '%s', skipping: %v", key, err)
				continue
			}

			if !exists {
				// add key: if key does not exist in currentPairs, it's a new task
				eventType = TaskEventTypePut
			} else if !bytes.Equal(oldPair.Value, newPair.Value) {
				// update key: if key exists and value is different, it's an updated task
				eventType = TaskEventTypePut
			} else {
				// nothing changed for this key
				continue
			}

			// Send the appropriate event
			select {
			case eventChan <- TaskMetaEvent{Type: eventType, Task: task}:
			case <-ctx.Done():
				return
			}
		}

		// find deleted keys
		// Iterate over currentPairs to find tasks that are no longer in newPairMap
		for key := range currentPairs {
			if _, exists := newPairMap[key]; !exists {
				// Key is in currentPairs but not in newPairMap, so it's a deleted task
				// get task name (remove prefix from key)
				taskName := strings.TrimPrefix(key, consulKVTasksPrefix)
				taskName = strings.TrimPrefix(taskName, "/") // Also trim leading slash if present

				// Send Delete event
				select {
				case eventChan <- TaskMetaEvent{Type: TaskEventTypeDelete, Task: TaskMeta{Name: taskName}}:
				case <-ctx.Done():
					return
				}
			}
		}

		// update current snapshot
		currentPairs = newPairMap
	}

	// Run the watch plan in a new goroutine
	go func() {
		defer close(eventChan) // Close the event channel when the goroutine exits

		// start watch plan
		if err := plan.RunWithClientAndHclog(r.client, nil); err != nil {
			// Log an error if the watch plan fails, unless it's due to context cancellation
			if !errors.Is(err, context.Canceled) {
				logger.Errorf("Consul task watch plan failed: %v", err)
				return
			}
		}
	}()

	return eventChan, nil
}

// MarkTaskDeleted moves a task from the active tasks to the deleted tasks list by creating a key in the deleted tasks prefix.
func (r *consulRegistry) MarkTaskDeleted(ctx context.Context, taskName string) error {
	// Create a KVPair for the deleted task marker
	kv := &api.KVPair{
		Key:   consulKVDeletedTaskPrefix + taskName, // Key indicates the task is deleted
		Value: []byte(""),                           // Value can be empty; existence of the key marks deletion
	}

	// Put the marker key into Consul's KV store
	_, err := r.client.KV().Put(kv, nil)
	if err != nil {
		return fmt.Errorf("unable to mark task '%s' as deleted in Consul KV, deletion operation failed: %v", taskName, err)
	}

	return nil
}

// GetDeletedTaskNames retrieves the list of deleted task names from Consul.
// It returns a slice of task names that have been marked as deleted.
func (r *consulRegistry) GetDeletedTaskNames(ctx context.Context) []string {
	// List all KVPairs under the deleted tasks prefix
	pairs, _, err := r.client.KV().List(consulKVDeletedTaskPrefix, nil)
	if err != nil {
		logger.Errorf("Failed to list deleted tasks from Consul KV: %v", err)
		return nil // Return nil if there's an error
	}

	// Initialize a slice to store the names of deleted tasks
	var deletedTasks []string
	// Iterate over the retrieved KVPairs
	for _, pair := range pairs {
		// Extract the task name by trimming the prefix from the key
		deletedTask := strings.TrimPrefix(pair.Key, consulKVDeletedTaskPrefix)
		deletedTasks = append(deletedTasks, deletedTask)
	}

	return deletedTasks
}

// WatchDeletedTaskEvent sets up a watch on the deleted tasks KV prefix.
// It returns a channel that receives an empty struct when the deleted tasks list changes, signaling a need to refresh.
func (r *consulRegistry) WatchDeletedTaskEvent(ctx context.Context) (<-chan struct{}, error) {
	// Create a channel for delete events with a defined buffer size
	eventChan := make(chan struct{}, NodeEventChannelSize) // Reusing NodeEventChannelSize, consider a specific one if needed

	// Create a watch plan to monitor the KV prefix for deleted tasks
	params := map[string]interface{}{
		"type":   "keyprefix",
		"prefix": consulKVDeletedTaskPrefix,
	}

	// Parse the watch parameters to create a watch plan
	plan, err := watch.Parse(params)
	if err != nil {
		return nil, fmt.Errorf("trouble creating deleted task watch plan, cannot monitor deleted task status: %v", err)
	}

	// Start a goroutine to monitor key-value changes
	go func() {
		defer close(eventChan) // Close the event channel when the goroutine exits

		// Send an initial event to signal the current state or trigger initial processing
		select {
		case eventChan <- struct{}{}:
		case <-ctx.Done(): // If context is cancelled, stop
			return
		}

		// Set up the handler function for the watch plan
		plan.Handler = func(idx uint64, data interface{}) {
			// Send notification when the deleted task list changes
			select {
			case eventChan <- struct{}{}: // Send an empty struct to signal a change
			default:
				// Channel is full, ignore this event to prevent blocking. Consider logging if this happens frequently.
				logger.Warnf("Deleted task event channel full, event dropped for prefix %s", consulKVDeletedTaskPrefix)
			}
		}

		// Start the watch
		if err := plan.RunWithClientAndHclog(r.client, nil); err != nil {
			// Log an error if the watch plan fails, unless it's due to context cancellation
			if !errors.Is(err, context.Canceled) {
				logger.Errorf("Consul deleted task watch plan failed: %v", err)
			}
		}
	}()

	return eventChan, nil
}

// CanRunTask checks whether a task can be executed at a given time using a Consul transaction.
// It attempts to create a key representing the task execution at a specific time.
// If the key creation is successful (meaning it didn't exist), the task can run.
func (r *consulRegistry) CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error) {
	// Construct the key using the task execution prefix, formatted time, and task name
	key := consulKVTaskLastExec + execTime.Format(time.RFC3339) + "-" + taskName

	// try to atomically write (only when Key does not exist)
	success, _, err := r.client.KV().CAS(&api.KVPair{
		Key:         key,
		Value:       []byte(""),
		ModifyIndex: 0, // key parameter: 0 means "write only when Key does not exist"
	}, nil)

	if err != nil {
		return false, fmt.Errorf("consul CAS failed: %v", err)
	}

	if !success {
		return false, nil // key already exists, task cannot run
	}

	// write success, trigger cleanup event
	// Send an event to the cleaner to eventually remove this execution marker key.
	err = r.cleaner.SendEvent(&Event{
		Key:  key,
		Time: execTime, // Time for the cleaner to know when this key might be eligible for cleanup
	})
	if err != nil {
		// Log if sending the event to the cleaner fails, but don't fail CanRunTask itself.
		logger.Errorf("Failed to send cleanup event for task execution key '%s': %v", key, err)
	}
	return true, nil
}

// ForceCleanupTask forcibly removes all data related to a specific task from Consul KV.
// This includes the task definition, any deleted task markers, and last execution time records associated with the task name.
// Note: This is a direct cleanup and might not be perfectly accurate if taskName is a substring of other keys.
// For more precise cleanup of execution keys, a pattern match and delete would be needed, which is more complex with transactions.
func (r *consulRegistry) ForceCleanupTask(ctx context.Context, taskName string) error {
	// Prepare a transaction to delete multiple keys related to the task.
	txn := api.KVTxnOps{
		// Delete the main task definition.
		&api.KVTxnOp{Verb: api.KVDelete, Key: consulKVTasksPrefix + taskName},
		// Delete the marker for the task if it was marked as deleted.
		&api.KVTxnOp{Verb: api.KVDelete, Key: consulKVDeletedTaskPrefix + taskName},
		// Attempt to delete a generic last execution key. This is a best-effort and might not cover all time-specific keys.
		// A more robust cleanup of execution keys would require listing and deleting, which isn't atomic in a single Txn this way.
		&api.KVTxnOp{Verb: api.KVDelete, Key: consulKVTaskLastExec + taskName}, // This specific key format might not exist.
	}
	// Execute the transaction.
	// The boolean 'ok' indicates if the transaction was committed. We don't check 'ok' here because KVDelete ops don't fail if key doesn't exist.
	// We only care about the error from the transaction execution itself.
	_, _, _, err := r.client.KV().Txn(txn, nil)
	if err != nil {
		return fmt.Errorf("encountered issues while forcing cleanup of task '%s' data in Consul KV: %v", taskName, err)
	}
	return nil
}

// ForceCleanupAllTasks forcibly removes all task-related data from the Consul KV registry.
// This includes all active tasks, all deleted task markers, and all task execution time records.
func (r *consulRegistry) ForceCleanupAllTasks(ctx context.Context) error {
	// cleanup tasks: Delete all keys under the active tasks prefix.
	_, err := r.client.KV().DeleteTree(consulKVTasksPrefix, nil)
	if err != nil {
		return fmt.Errorf("error cleaning up all active tasks from Consul KV, some task data may still exist: %v", err)
	}
	// cleanup deleted_tasks: Delete all keys under the deleted tasks prefix.
	_, err = r.client.KV().DeleteTree(consulKVDeletedTaskPrefix, nil)
	if err != nil {
		return fmt.Errorf("unable to cleanup all deleted task markers from Consul KV, marker information may not be fully removed: %v", err)
	}

	// cleanup task_last_exec: Delete all keys under the task last execution time prefix.
	_, err = r.client.KV().DeleteTree(consulKVTaskLastExec, nil)
	if err != nil {
		return fmt.Errorf("problem cleaning up all task execution records from Consul KV, historical execution records may not be fully cleared: %v", err)
	}
	return nil
}

// RemoveTaskDeleted removes a task from the deleted tasks list in Consul KV.
func (r *consulRegistry) RemoveTaskDeleted(ctx context.Context, taskName string) error {
	// Delete the key that marks the task as deleted.
	_, err := r.client.KV().Delete(consulKVDeletedTaskPrefix+taskName, nil)
	if err != nil {
		return fmt.Errorf("error removing task '%s' from deleted tasks in Consul KV, task may still be marked as deleted: %v", taskName, err)
	}
	return nil
}

// batchDeleteExecKeys performs a batch deletion of task execution keys using a Consul transaction.
func (r *consulRegistry) batchDeleteExecKeys(keys []string) error {
	// If there are no keys to delete, return immediately.
	if len(keys) == 0 {
		return nil
	}

	// prepare transaction operations
	// Create a slice of KVTxnOp for deleting each key.
	ops := make(api.KVTxnOps, len(keys))
	for i, key := range keys {
		ops[i] = &api.KVTxnOp{
			Verb: api.KVDelete,
			Key:  key,
		}
	}

	// commit transaction
	ok, resp, _, err := r.client.KV().Txn(ops, nil)
	if err != nil {
		return fmt.Errorf("batch deletion transaction for execution keys failed, cannot cleanup historical execution records: %v", err)
	}

	// Check if the transaction was successful.
	if !ok {
		// If 'ok' is false, the transaction was rolled back.
		if len(resp.Errors) > 0 {
			return fmt.Errorf("batch deletion transaction for execution keys rolled back, reason: %s", resp.Errors[0].What)
		}
		return fmt.Errorf("batch deletion transaction for execution keys failed for an unknown reason, check Consul service status")
	}

	logger.Infof("Batch deleted %d task execution keys from Consul KV", len(keys))
	return nil
}

// cleanupHistoryExecKeys periodically cleans up historical task execution keys from Consul KV.
// It runs on a ticker and calls tryCleanupHistoryExecKeys to perform the actual cleanup logic.
func (r *consulRegistry) cleanupHistoryExecKeys(ctx context.Context) {
	// Create a ticker for periodic cleanup (e.g., every 20 minutes).
	ticker := time.NewTicker(CleanupHistoryExecKeysTickerDuration) // TODO: Make this interval configurable
	defer ticker.Stop()

	// Perform an initial cleanup immediately.
	if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
		logger.Errorf("Initial historical execution key cleanup failed during registry initialization: %v", err)
	}

	// Loop indefinitely, waiting for ticker ticks or context cancellation.
	for {
		select {
		case <-ticker.C: // When the ticker fires
			if err := r.tryCleanupHistoryExecKeys(ctx); err != nil {
				logger.Errorf("Scheduled cleanup of historical execution keys failed during periodic execution: %v", err)
			} else {
				logger.Infof("Scheduled cleanup of historical execution keys completed successfully - system resources freed")
			}

		case <-ctx.Done(): // When the context is cancelled (e.g., service shutdown)
			logger.Info("Historical execution key cleanup service shutting down due to context cancellation.")
			return
		}
	}
}

// tryCleanupHistoryExecKeys performs the actual cleanup of historical task execution keys.
// It lists keys under consulKVTaskLastExec, identifies expired keys, and batch deletes them.
func (r *consulRegistry) tryCleanupHistoryExecKeys(ctx context.Context) error {
	const pageSize = 1000 // Number of keys to process in each batch
	var toDelete []string

	// Initialize pagination
	var after string
	var allProcessed bool

	for !allProcessed {
		// Use 'after' as the pagination marker
		keys, _, err := r.client.KV().Keys(consulKVTaskLastExec, after, &api.QueryOptions{})
		if err != nil {
			return fmt.Errorf("failed to list task execution keys for cleanup: %v", err)
		}

		// If no keys found, end processing
		if len(keys) == 0 {
			logger.Debugf("No more task execution keys found under prefix '%s' for cleanup.", consulKVTaskLastExec)
			break
		}

		logger.Debugf("Processing batch of %d keys for cleanup", len(keys))

		// Process this batch of keys
		for _, key := range keys {
			// Extract timestamp and check for expiration
			keyWithoutPrefix := strings.TrimPrefix(key, consulKVTaskLastExec)
			if len(keyWithoutPrefix) < len(time.RFC3339) {
				logger.Warnf("Skipping key '%s' during cleanup: too short to contain a valid timestamp.", key)
				continue
			}

			timePart := keyWithoutPrefix[:len(time.RFC3339)]
			execTime, err := time.Parse(time.RFC3339, timePart)
			if err != nil {
				logger.Warnf("Skipping key '%s' during cleanup: failed to parse time part '%s': %v", key, timePart, err)
				continue
			}

			// If the execution time is older than the cleanup threshold, mark it for deletion
			if time.Since(execTime) >= CleanupHistoryExecKeysThresholdDuration {
				toDelete = append(toDelete, key)
			}
		}

		// If we received fewer keys than the page size, we've processed all keys
		if len(keys) < pageSize {
			allProcessed = true
		} else {
			// Set the starting point for the next batch
			after = keys[len(keys)-1]
		}

		// If we've collected enough keys for batch deletion, or processed all keys
		if len(toDelete) >= CleanerBatchSize || allProcessed {
			if len(toDelete) > 0 {

				for i := 0; i < len(toDelete); i += CleanerBatchSize {
					end := i + CleanerBatchSize
					if end > len(toDelete) {
						end = len(toDelete)
					}

					batch := toDelete[i:end]
					logger.Infof("Attempting to batch delete %d expired task execution keys.", len(batch))
					if err := r.batchDelete(batch); err != nil {
						return err
					}
				}

				// Reset the deletion batch
				toDelete = nil
			}
		}
	}

	// Process the last keys
	if len(toDelete) > 0 {
		logger.Infof("Attempting to batch delete %d expired task execution keys.", len(toDelete))
		return r.batchDelete(toDelete)
	}

	logger.Infof("No expired task execution keys found to delete.")
	return nil
}

// batchDelete performs a generic batch deletion of keys using a Consul transaction.
func (r *consulRegistry) batchDelete(keys []string) error {
	// If there are no keys to delete, return immediately.
	if len(keys) == 0 {
		return nil
	}

	// Prepare transaction operations for deleting each key.
	ops := make(api.KVTxnOps, len(keys))
	for i, k := range keys {
		ops[i] = &api.KVTxnOp{
			Verb: api.KVDelete,
			Key:  k,
		}
	}

	// Execute the transaction.
	ok, resp, _, err := r.client.KV().Txn(ops, nil)
	if err != nil {
		return fmt.Errorf("consul transaction for batch delete failed, possibly exceeding transaction size limits: %v", err)
	}
	// Check if the transaction was successful.
	if !ok {
		if len(resp.Errors) > 0 {
			return fmt.Errorf("batch delete transaction rolled back, reason: %s", resp.Errors[0].What)
		}
		return fmt.Errorf("batch delete transaction failed for an unknown reason, check Consul logs for more information")
	}

	logger.Infof("Successfully, %d historical data have been deleted from Consul.", len(keys))

	return nil
}
