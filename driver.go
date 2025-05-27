package dcron

import (
	"context"
	"time"
)

// NodeStatus represents the status of a node in the cluster
type NodeStatus string

const (
	NodeStatusStarting NodeStatus = "starting" // Node is starting up and initializing
	NodeStatusWorking  NodeStatus = "working"  // Node is active and processing tasks
	NodeStatusLeaving  NodeStatus = "leaving"  // Node is gracefully shutting down; will be deleted soon
)

// NodeEventType represents the type of node event
type NodeEventType string

const (
	NodeEventTypeDelete  NodeEventType = "delete"  // Node deleted event - node has been removed
	NodeEventTypePut     NodeEventType = "put"     // Node added/updated event - node was created or modified
	NodeEventTypeChanged NodeEventType = "changed" // Node changed event (details not specified)
)

// TaskEventType represents the type of task event
type TaskEventType string

const (
	TaskEventTypeDelete TaskEventType = "delete" // Task deleted event - task has been removed
	TaskEventTypePut    TaskEventType = "put"    // Task added/updated event - task was created or modified
)

// Node represents a node in the distributed cron cluster
// Each node is a separate process that can execute scheduled tasks
type Node struct {
	ID         string     `json:"id"`          // Node ID - unique identifier
	IP         string     `json:"ip"`          // IP address of the node
	Hostname   string     `json:"hostname"`    // Hostname of the machine running the node
	LastAlive  time.Time  `json:"last_alive"`  // Last heartbeat time - used for health checking
	CreateTime time.Time  `json:"create_time"` // Node creation time
	Status     NodeStatus `json:"status"`      // Node status (starting/working/leaving)
	Weight     int        `json:"weight"`      // Node weight - used for load balancing
}

// NodeEvent represents a node event (addition, deletion, or change)
type NodeEvent struct {
	Type NodeEventType `json:"type"` // "put", "delete", or "changed"
	Node Node          `json:"node"`
}

// TaskMetaEvent represents a task metadata event (addition or deletion)
type TaskMetaEvent struct {
	Type TaskEventType `json:"type"` // "put" or "delete"
	Task TaskMeta      `json:"task"` // dynamic task
}

const (
	NodeEventChannelSize                    = 64              // Size of the node event channel
	TaskEventChannelSize                    = 256             // Size of the task event channel
	RebalancedChanelSize                    = 512             // Size of the rebalanced channel
	CleanerBufferSize                       = 1024            // Buffer size for cleaner
	CleanerBatchSize                        = 20              // Batch size for cleaner
	CleanerDelay                            = 5 * time.Second // Delay before cleaning up expired exec keys
	CleanupHistoryExecKeysThresholdDuration = 1 * time.Hour   // Cleanup history exec keys threshold duration
)

// Registry defines the interface that all registry backends must implement
// This allows for different storage backends (etcd, ZooKeeper, Consul, etc.)
type Registry interface {
	NodeRegistry
	TaskRegistry
}

type NodeRegistry interface {
	Register(ctx context.Context, node Node) error                            // Register a new node
	Unregister(ctx context.Context, nodeID string) error                      // Unregister a node
	UpdateStatus(ctx context.Context, nodeID string, status NodeStatus) error // Update node status
	UpdateHeartbeat(ctx context.Context, nodeID string) error                 // Update node heartbeat
	GetNodes(ctx context.Context) ([]Node, error)                             // Get all nodes
	GetWorkingNodes(ctx context.Context) ([]Node, error)                      // Get all working nodes
	WatchNodes(ctx context.Context) (<-chan NodeEvent, error)                 // Watch for node events
	CleanupExpiredNodes(ctx context.Context, timeout time.Duration) error     // Cleanup expired nodes
}

// TaskRegistry defines the interface for managing dynamic and one-shot tasks
type TaskRegistry interface {
	PutTask(ctx context.Context, task TaskMeta) error                                  // Put or update task metadata
	MarkTaskDeleted(ctx context.Context, taskName string) error                        // Mark a task as deleted
	RemoveTaskDeleted(ctx context.Context, taskName string) error                      // Remove task from deleted list
	WatchDeletedTaskEvent(ctx context.Context) (<-chan struct{}, error)                // Watch for deleted task events
	ForceCleanupTask(ctx context.Context, taskName string) error                       // Force cleanup of task metadata
	GetAllTasks(ctx context.Context) (map[string]TaskMeta, error)                      // Get all task metadata
	WatchTaskEvent(ctx context.Context) (<-chan TaskMetaEvent, error)                  // Watch for task metadata events
	GetDeletedTaskNames(ctx context.Context) []string                                  // Get all deleted task names
	ForceCleanupAllTasks(ctx context.Context) error                                    // Force cleanup of all tasks and deleted tasks. !!!d
	CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error) // CanRunTask determines if a specific task can be executed at the given time. It uses distributed locking to ensure only one node in the cluster executes a task instance
}
