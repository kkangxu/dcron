# 📚 `dcron` Distributed Scheduled Task System Documentation

`dcron` is a distributed scheduled task system implemented based on various registries (such as Consul, Redis, Etcd, ZooKeeper). It supports the management of static and dynamic tasks and provides service node discovery and load balancing mechanisms, aiming to provide an efficient and reliable solution for scheduled task scheduling in microservice architectures.

---

## 🧩 Features

- **Support for Multiple Registries**: Supports various registries such as Consul, Redis, Etcd, and Zookeeper, allowing users to choose the appropriate implementation based on their needs.
- **Static and Dynamic Task Management**: Supports static tasks (pre-defined) and dynamic tasks (added at runtime), flexibly responding to different scenario requirements.
- **Node Change Listening**: Provides a combination of Poll and Watch modes to ensure timely task scheduling.
- **Task Scheduling Strategies**: Supports various task distribution strategies, including consistent hashing, average distribution, hash slots, range distribution, weighted distribution, and round-robin, optimizing task distribution across nodes.
- **Node Status Management**: Provides node status management functionality, supporting the startup, working, and exit states of nodes.
- **Flexibility of Dynamic Tasks**: Dynamic tasks can be set as one-time tasks (OneShot) and support custom data (Payload).
- **Error Handling Mechanism**: Supports callback handling for task execution errors, ensuring the reliability of task execution.
- **Synchronization of Deleted Tasks**: After deleting dynamic tasks, the system automatically synchronizes to the registry's deleted task list to avoid dirty data.
- **Distributed Lock Mechanism**: Implements distributed locks through the CanRunTask interface to ensure that tasks are executed only once in the cluster.
- **Task Lifecycle Management**: Supports automatic deletion or cleanup of tasks after execution, meeting the needs of one-time and temporary tasks.

---

## 📦 Installation Dependencies
Before using `dcron`, please ensure that the following dependencies are installed:
```bash
# consul registry
go get github.com/hashicorp/consul/api

# redis registry
go get github.com/redis/go-redis/v9

# etcd registry
go get go.etcd.io/etcd/client/v3

# zookeeper registry
go get github.com/go-zookeeper/zk

# cron scheduler
go get github.com/robfig/cron/v3
```
---
## 🧰 Quick Start

### 1. Initialize Registry Client (Example with Consul)

First, you need to initialize a registry client. Here is an example using Consul:

```go
import (
    "github.com/kkangxu/dcron"
    "github.com/hashicorp/consul/api"
)

config := api.DefaultConfig()
config.Address = "localhost:8500" // Consul address
client, err := api.NewClient(config)
if err != nil {
    log.Fatalf("Failed to create Consul client: %v", err)
}
registry := dcron.NewConsulRegistry(client)
```

### 2. Create Dcron Instance

Next, you can create a `Dcron` instance and configure options:

```go
dc := dcron.NewDcron(
    registry,
    dcron.WithStrategy(dcron.StrategyConsistent), // Use consistent hashing strategy
    dcron.WithCronOptions(cron.WithSeconds()),    // Set to second-level precision
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
        fmt.Println("Executing dynamic task:", task.Name, "Payload:", task.Payload)
        return nil
    }),
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
        fmt.Printf("Task %s execution error: %v\n", task.Name, err)
    }),
)
```

### 3. Add Static Task

You can add static tasks as follows:

```go
err := dc.AddFunc("static-task", "*/5 * * * * *", func() error {
    fmt.Println("Executing static task every 5 seconds")
    return nil
})
if err != nil {
    log.Fatal(err)
}
```

#### 🚩 Force Add Task (Ignoring Existing, Deleted, etc.)

Sometimes you need to ignore whether the task already exists, has been deleted, or executed, and forcefully add the task. You can use ForceAddTask/ForceAddFunc/ForceAddOneShotTask/ForceAddOneShotFunc:

```go
// Force add a normal task
_ = dc.ForceAddFunc("force-task", "*/3 * * * * *", func() error {
    fmt.Println("Forced task added, executing every 3 seconds")
    return nil
})

// Force add a one-time task
_ = dc.ForceAddOneShotFunc("force-oneshot", "*/10 * * * * *", func() error {
    fmt.Println("Forced one-time task added")
    return nil
})
```

> These methods will directly overwrite tasks with the same name, ignoring existing, deleted, or executed restrictions.

### 4. Add Dynamic Task

Here is an example of adding dynamic tasks:

```go
err := dc.AddTaskMeta(dcron.TaskMeta{
    Name:       "dynamic-task",
    CronFormat: "*/10 * * * * *", // Execute every 10 seconds
    Payload:    "hello from dynamic task",
})
if err != nil {
    log.Fatal(err)
}
```

### 5. Start Service

Finally, start the `Dcron` service:

```go
if err := dc.Start(); err != nil {
    log.Fatal(err)
}
```

---

## 🧰 Registry Examples

Here are example codes using different registries, demonstrating how to initialize clients, create `Dcron` instances, and add tasks.

#### Consul Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"github.com/hashicorp/consul/api"
	"log"
)

func main() {
	// Configure Consul client
	config := api.DefaultConfig()
	config.Address = "localhost:8500" // Consul address
	client, err := api.NewClient(config)
	if err != nil {
		log.Fatalf("Failed to create Consul client: %v", err)
	}

	// Create Dcron instance
	dc := dcron.NewDcron(dcron.NewConsulRegistry(client), dcron.WithStrategy(dcron.StrategyConsistent))

	// Add static task
	err = dc.AddFunc("test-per5m", "*/5 * * * * *", func() error {
		log.Println("Executing static task every 5 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Start the service
	if err := dc.Start(); err != nil {
		log.Fatal(err)
	}
}
```

#### Redis Registry Example

```go
package main

import (
	"context"
	"github.com/kkangxu/dcron"
	"github.com/redis/go-redis/v9"
	"log"
)

func main() {
	// Initialize Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	// Create Dcron instance
	dc := dcron.NewDcron(dcron.NewRedisRegistry(rdb), dcron.WithStrategy(dcron.StrategyConsistent))

	// Add static task
	err := dc.AddFunc("test-per10s", "*/10 * * * * *", func() error {
		log.Println("Executing static task every 10 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Start the service
	if err := dc.Start(); err != nil {
		log.Fatal(err)
	}
}
```

#### Etcd Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"go.etcd.io/etcd/client/v3"
	"log"
	"time"
)

func main() {
	// Initialize Etcd client
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:2379"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect Etcd: %v", err)
	}

	// Create Dcron instance
	dc := dcron.NewDcron(dcron.NewEtcdRegistry(cli), dcron.WithStrategy(dcron.StrategyConsistent))

	// Add static task
	err = dc.AddFunc("etcd-test", "*/3 * * * * *", func() error {
		log.Println("Executing task every 3 seconds (Etcd)")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Start the service
	if err := dc.Start(); err != nil {
		log.Fatal(err)
	}
}
```

#### ZooKeeper Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"github.com/go-zookeeper/zk"
	"log"
	"time"
)

func main() {
	// Connect to ZooKeeper
	conn, _, err := zk.Connect([]string{"127.0.0.1:2181"}, time.Second*5)
	if err != nil {
		log.Fatalf("Failed to connect ZooKeeper: %v", err)
	}

	// Create Dcron instance
	dc := dcron.NewDcron(dcron.NewZookeeperRegistry(conn), dcron.WithStrategy(dcron.StrategyConsistent))

	// Add static task
	err = dc.AddFunc("zk-test", "*/2 * * * * *", func() error {
		log.Println("Executing static task every 2 seconds (ZooKeeper)")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Start the service
	if err := dc.Start(); err != nil {
		log.Fatal(err);
	}
}
```

---

## 🧭 Dynamic Task Deletion and Synchronization

- When deleting dynamic tasks, calling `MarkTaskDeleted("task-name")` will automatically synchronize to the registry's deleted task list to avoid dirty data.

## 📝 Lifecycle and Node Status

- The node status is `starting` at startup.
- After successful registration, it updates to `working`.
- The status updates to `leaving` when the node exits.
- Node information is completely deleted.

---

## ⚙️ Optional Configuration Items (Option)
`Option` is a function type used to configure options for the `Dcron` instance. Here are the available option functions:

```go
type Option func(*dcron)
```

| Option | Description                     |
|--------|---------------------------------|
| `WithStrategy(StrategyConsistent)` | Set task distribution strategy: consistent hashing         |
| `WithStrategy(StrategyHashSharding)` | Set task distribution strategy: average distribution          |
| `WithStrategy(StrategyHashSlot)` | Set task distribution strategy: hash slot           |
| `WithStrategy(StrategyRange)` | Set task distribution strategy: range distribution          |
| `WithStrategy(StrategyWeighted)` | Set task distribution strategy: weighted distribution          |
| `WithStrategy(StrategyRoundRobin)` | Set task distribution strategy: round-robin            |
| `WithAssigner(Assigner)` | Set custom task assigner or built-in assigner of dcron      |
| `WithCronOptions(...)` | Custom Cron configuration (e.g., second-level precision)     |
| `WithTaskRunFunc(handler)` | Set dynamic task execution function             |
| `WithErrHandler(handler ErrHandler)` | Set task execution error handling function.          |
| `WithLogger(log Logger)` | Set custom logger; if not set, use default logger |

---

## 🧾 Error Handling

You can inject a task execution error handling function through `WithErrHandler`:

```go
dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
    logger.Infof("Task %s execution error: %v", task.Name, err)
})
```

---

## 🧾 Unified Handling Function for Dynamic Tasks

You can inject a dynamic task execution function through `WithTaskRunFunc`:

```go
dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
    fmt.Println("Executing dynamic task:", task.Name, "Payload:", task.Payload)
    return nil
}),
```

---

## 🛠️ Dynamic Task API

```go
// Add dynamic task
err := dc.AddTaskMeta(TaskMeta{
    Name:       "task-name",
    CronFormat: "*/5 * * * * *",
    Payload:    "demo payload",
})

// Remove dynamic task
err := dc.MarkTaskDeleted("task-name")
```

---

## 🔒 Distributed Lock Mechanism

`dcron` uses the distributed lock mechanism provided by the registry to ensure that tasks are executed only once in the cluster. This is implemented through the `CanRunTask` interface:

```go
// CanRunTask determines whether a specific task can be executed at a given time
// It uses distributed locks to ensure that only one node in the cluster executes the task instance
CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error)
```

When a node attempts to execute a task, it first checks whether that task has already been executed by other nodes at the current time. This mechanism is particularly suitable for:

1. Scenarios with frequent node changes
2. Scenarios that require strict guarantees that tasks are executed once
3. Scenarios where task execution time is sensitive

---

## 📊 API Description

| Method Name | Description                                                                               |
|-------------|-------------------------------------------------------------------------------------------|
| AddTask(name, cron, tasker) | Add static task                                                                           |
| AddFunc(name, cron, func) | Add static task (function)                                                                   |
| AddOneShotTask(name, cron, tasker) | Add one-time task                                                                      |
| AddOneShotFunc(name, cron, func) | Add one-time task (function)                                                                |
| AddTaskMeta(meta) | Add dynamic task                                                                           |
| ForceAddTask(name, cron, tasker) | Force add static task (ignore existing/deleted status)                                        |
| ForceAddFunc(name, cron, func) | Force add static task (function)                                                             |
| ForceAddOneShotTask(name, cron, tasker) | Force add one-time task                                                                    |
| ForceAddOneShotFunc(name, cron, func) | Force add one-time task (function)                                                          |
| ForceAddTaskMeta(meta) | Force add dynamic task (ignore existing/deleted status)                                        |
| ReuseDeletedTask(name) | Reuse a task marked as deleted                                                               |
| MarkTaskDeleted(name) | Mark task as deleted. The mark is stored in the registry. It will not succeed or be executed after service restart or re-Addxxx API. If you want to add the same task, please use ForceXXX API. |
| CleanupTask(name) | Completely clean all data of the task. If the service restarts, running Addxxx/Forcexxx API will cause the task to rejoin.                       |
| Start() | Start service                                                                             |
| Stop() | Stop service                                                                              |
| GetNodeID() | Get current node ID                                                                        |
| GetAllTasks() | Get all task names                                                                         |
| GetMyselfRunningTasks() | Get the names of tasks currently running on the node                                          |
| ForceCleanupAllTasks(ctx) | Force clean all tasks and deleted tasks. !!!Danger!!! Only for testing, can only be used by one node. |

---

## 📦 TaskMeta Structure

```go
type TaskMeta struct {
    Name                   string `json:"name"`                                // Task name
    CronFormat             string `json:"cron_format"`                         // Cron format
    OneShot                bool   `json:"one_shot,omitempty"`                  // One-time task flag
    ExecutedAndMarkDeleted bool   `json:"executed_and_mark_deleted,omitempty"` // After task execution, delete the task and mark it as deleted
    ExecutedAndCleanup     bool   `json:"executed_and_cleanup,omitempty"`      // After task execution, completely delete task data
    Payload                string `json:"payload,omitempty"`                   // Payload
}
```

---

## 📝 Node Structure

```go
type Node struct {
    ID         string     `json:"id"`          // Node ID
    IP         string     `json:"ip"`          // IP address
    Hostname   string     `json:"hostname"`    // Hostname
    LastAlive  time.Time  `json:"last_alive"`  // Last heartbeat time
    CreateTime time.Time  `json:"create_time"` // Creation time
    Status     NodeStatus `json:"status"`      // Status (starting/working/leaving)
}
```

---

## 🧭 NodeEvent & TaskEvent

```go
type NodeEvent struct {
    Type NodeEventType // "put", "delete", or "changed"
    Node Node
}

type TaskMetaEvent struct {
    Type TaskEventType // "put" or "delete"
    Task TaskMeta
}
```

---

## ❓ Frequently Asked Questions (FAQ) / Usage Suggestions
1. **Why can't I directly AddTask after MarkTaskDeleted?**
   - When you call `MarkTaskDeleted("task-name")`, the system marks that task name as "deleted" in the registry to prevent dirty data and duplicate scheduling.
   - If you want to add the same task again, it is recommended to use `ForceAddTask` or `ForceCleanupTask`, which can thoroughly clean up old traces and ensure task status consistency.

2. **What is the difference between ForceAddTask and normal AddTask?**
   - `AddTask` checks whether the task already exists or has been deleted; if it exists, it reports an error to prevent duplication.
   - `ForceAddTask` automatically cleans up related traces in the registry (including deleted marks, execution records, etc.), thoroughly overwriting the same task, suitable for "forced reset" scenarios.

3. **What is the difference between dynamic tasks and static tasks?**
   - Static tasks: Managed locally, suitable for pre-defined scheduled tasks in code, automatically restored after node restart.
   - Dynamic tasks: Depend on registry synchronization, suitable for background dynamic dispatch, operation control, etc., supporting runtime addition, deletion, and modification.

4. **How to completely clean up all traces of a task in the registry?**
   - Call the registry's `ForceCleanupTask(ctx, taskName)` method, which can simultaneously delete task metadata, deleted marks, and last execution time.
   - Suitable for scenarios that require "complete reset" of task status.

5. **Why is there a "deleted task synchronization" mechanism?**
   - In a distributed environment, there may be data inconsistencies between nodes. By synchronizing the "deleted tasks" list, it can prevent deleted tasks from being rescheduled by other nodes, ensuring global consistency and idempotency.

6. **How to gracefully shut down and take nodes offline?**
   - Call the `Stop()` method, and `dcron` will automatically:
     - Update the node status to `leaving`
     - Stop the local scheduler
     - Unregister node information
     - Release resources to ensure tasks are not rescheduled.

7. **What registries are supported? How to switch?**
   - Supports Consul, Redis, Etcd, Zookeeper.
   - Just initialize the corresponding registry instance and pass it to `NewDcron`, no need to modify business code.

8. **How to listen for changes in nodes or tasks?**
   - You can use interfaces like `WatchNodes`, `WatchTaskEvent`, etc., to listen for events such as node online/offline, task addition/deletion/modification, suitable for monitoring and automated operation and maintenance extensions.

9. **How to customize the execution logic of dynamic tasks?**
   - Inject a custom handling function through `WithTaskRunFunc`, all dynamic tasks will call back to this function, facilitating unified processing and extension.

10. **How to choose task distribution strategies?**
    - `StrategyConsistent` (consistent hashing): Suitable for scenarios with frequent node changes and large task volumes, distributing more evenly.
    - `StrategyHashSharding` (average distribution): Suitable for stable node counts and fewer tasks, simple and efficient.
    - `StrategyHashSlot` (hash slot): Distributes tasks across multiple slots, ensuring that each node receives as many slots as possible evenly.
    - `StrategyRange` (range distribution): Distributes based on the range of task names, suitable for ordered tasks.
    - `StrategyWeighted` (weighted distribution): Distributes tasks based on the weight of nodes, suitable for scenarios where different nodes have uneven processing capabilities.
    - `StrategyRoundRobin` (round-robin): Distributes tasks to nodes in a round-robin manner, ensuring that each node receives tasks evenly.

---

## 📝 Summary

This project demonstrates how to build a reliable distributed scheduled task system through various registries. You can choose the appropriate registry implementation based on your architecture while flexibly adding static and dynamic tasks. All Registry implementations unify the interface, making it easy to extend and replace.

🔧 **Recommended Uses**:
- Unified scheduling of scheduled tasks in microservices
- Task load balancing in high availability scenarios
- Dynamic dispatch of scheduled tasks (e.g., background management console)

---