# 📚 `dcron` Distributed Cron System Documentation

`dcron` is a distributed cron system based on multiple registries (such as Consul, Redis, Etcd, ZooKeeper). It supports the management of both static and dynamic tasks, providing service node discovery and load balancing mechanisms, aimed at offering an efficient and reliable solution for scheduled tasks in microservices architectures.

---

## Navigation

* [Features](#-features)
* [Installation](#-installation)
* [Quick Start](#-quick-start)
    * [1. Initialize Registry Client (Consul Example)](#1-initialize-registry-client-consul-example)
    * [2. Create Dcron Instance](#2-create-a-dcron-instance)
    * [3. Add Static Tasks](#3-add-static-tasks)
    * [4. Add Dynamic Tasks](#4-add-dynamic-tasks)
    * [5. Start the Service](#5-start-the-service)
* [Registry Examples](#-registry-examples)
    * [Consul Registry Example](#consul-registry-example)
    * [Redis Registry Example](#redis-registry-example)
    * [Etcd Registry Example](#etcd-registry-example)
    * [ZooKeeper Registry Example](#zookeeper-registry-example)
* [Core Concepts & Mechanisms](#-core-concepts--mechanisms)
    * [Dynamic Task Deletion and Synchronization](#dynamic-task-deletion--synchronization)
    * [Lifecycle and Node States](#lifecycle--node-states)
    * [Distributed Lock Mechanism](#distributed-lock-mechanism)
* [Configuration and API Reference](#-configuration--api-reference)
    * [Optional Configuration Items (Option)](#optional-configuration-items-option)
    * [Error Handling](#error-handling)
    * [Dynamic Task Unified Handling Function](#dynamic-task-unified-handling-function)
    * [Dynamic Task API](#dynamic-task-api)
    * [Main API Descriptions](#main-api-descriptions)
    * [TaskMeta Structure](#taskmeta-structure)
    * [Node Structure](#node-structure)
    * [NodeEvent & TaskEvent](#nodeevent--taskevent)
* [Frequently Asked Questions (FAQ) / Usage Suggestions](#-frequently-asked-questions-faq--usage-suggestions)
* [Summary](#-summary)

---

## 🧩 Features

* **✨ Multi-Registry Support**: Seamlessly integrates with Consul, Redis, Etcd, and ZooKeeper, allowing for flexible selection and easy switching.
* **🚀 Static and Dynamic Task Management**: Supports both statically defined tasks in code and dynamically added, deleted, or modified tasks at runtime to meet various scheduling needs.
* **👀 Intelligent Node Change Monitoring**: Combines Poll and Watch modes to perceive node changes in real-time, ensuring high availability and timeliness of task scheduling.
* **🎯 Rich Task Scheduling Strategies**: Built-in strategies such as consistent hashing, average distribution, hash slots, range allocation, weighted distribution, and round-robin to optimize task distribution and enhance system load balancing capabilities.
* **🚦 Fine-Grained Node State Management**: Clearly manages the complete lifecycle of nodes from starting (`starting`), working (`working`), to leaving (`leaving`).
* **🔧 Flexible Dynamic Task Configuration**: Dynamic tasks support one-time execution (OneShot) and can carry custom data (Payload).
* **🛡️ Reliable Error Handling Mechanism**: Provides callbacks for task execution errors to ensure stability and traceability of task execution.
* **🔄 Intelligent Synchronization of Deleted Tasks**: Automatically synchronizes deleted dynamic tasks to the registry's deleted list, effectively preventing dirty data and task re-execution.
* **🔒 Built-in Distributed Lock**: Implements distributed locks through the `CanRunTask` interface to ensure that the same task is executed only once in the cluster, avoiding concurrency conflicts.
* **♻️ Complete Task Lifecycle Management**: Supports automatic deletion or cleanup after task execution, especially suitable for one-time and temporary task scenarios.

---

## 📦 Installation

Before using `dcron`, ensure that the following dependencies are installed in your project:

```bash
# Consul Registry (if you choose Consul)
go get github.com/hashicorp/consul/api

# Redis Registry (if you choose Redis)
go get github.com/redis/go-redis/v9

# Etcd Registry (if you choose Etcd)
go get go.etcd.io/etcd/client/v3

# ZooKeeper Registry (if you choose ZooKeeper)
go get github.com/go-zookeeper/zk

# Cron Scheduler (core scheduling library)
go get github.com/robfig/cron/v3
```

---

## 🧰 Quick Start

This section will guide you through quickly getting started with `dcron` using Consul as the registry.

### 1. Initialize Registry Client (Consul Example)

First, you need to initialize a registry client. Here is an example using Consul:

```go
import (
    "log"
    "github.com/kkangxu/dcron"
    "github.com/hashicorp/consul/api"
)

func main() {
    // Configure Consul client
    config := api.DefaultConfig()
    config.Address = "localhost:8500" // Consul service address
    client, err := api.NewClient(config)
    if err != nil {
        log.Fatalf("Failed to create Consul client: %v", err)
    }

    // Create dcron Registry based on Consul client
    registry := dcron.NewConsulRegistry(client)
    
    // ... Next, create a Dcron instance
}
```

> **Tip**: The initialization methods for other registries (Redis, Etcd, ZooKeeper) are similar; please refer to the [Registry Examples](#-registry-examples) section.

### 2. Create a Dcron Instance

Next, you can create a `Dcron` instance and configure related options:

```go
import (
    "fmt"
    "log"
    "github.com/kkangxu/dcron"
    "github.com/robfig/cron/v3" // For cron Options
    // ... Other imports
)

// ... Continue in the main function

// Create Dcron instance
dc := dcron.NewDcron(
    registry, // The registry instance created in the previous step
    dcron.WithStrategy(dcron.StrategyConsistent), // Set task assignment strategy to consistent hashing
    dcron.WithCronOptions(cron.WithSeconds()),    // Set Cron expression to support second-level precision
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error { // Set unified execution function for dynamic tasks
        fmt.Println("Executing dynamic task:", task.Name, "Payload:", task.Payload)
        return nil
    }),
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) { // Set error handling function for task execution
        fmt.Printf("Task %s execution error: %v\n", task.Name, err)
    }),
)
```

### 3. Add Static Tasks

Static tasks are usually defined in code and loaded when the service starts.

```go
// ... Continue in the main function

// Add a static task that runs every 5 seconds
err = dc.AddFunc("static-task-example", "*/5 * * * * *", func() error {
    fmt.Println("Static task runs every 5 seconds (static-task-example)")
    return nil
})
if err != nil {
    log.Fatalf("Failed to add static task: %v", err)
}
```

#### 🚩 Force Add Tasks (Ignoring Restrictions)

If you need to ignore whether a task already exists or has been deleted, you can use the `ForceAdd` series of methods to forcefully add or overwrite tasks:

```go
// Forcefully add a normal task, even if it already exists or has been deleted
_ = dc.ForceAddFunc("force-task-example", "*/3 * * * * *", func() error {
    fmt.Println("Forcefully added task (force-task-example), runs every 3 seconds")
    return nil
})

// Forcefully add a one-time task
_ = dc.ForceAddOneShotFunc("force-oneshot-example", "*/10 * * * * *", func() error {
    fmt.Println("Forcefully added one-time task (force-oneshot-example)")
    return nil // The task will be automatically cleaned up after execution
})
```
> **Note**: The `ForceAdd` series of methods will directly overwrite tasks with the same name and clear their related records in the registry (such as deleted markers). Please use with caution.

### 4. Add Dynamic Tasks

Dynamic tasks can be added at runtime through the API, with their metadata stored in the registry.

```go
// ... Continue in the main function

// Add a dynamic task that runs every 10 seconds
err = dc.AddTaskMeta(dcron.TaskMeta{
    Name:       "dynamic-task-example",
    CronFormat: "*/10 * * * * *",
    Payload:    "Greetings from the dynamic task",
})
if err != nil {
    log.Fatalf("Failed to add dynamic task: %v", err)
}
```
Dynamic tasks will be executed by the handler set in `WithTaskRunFunc`.

### 5. Start the Service

Finally, start the `Dcron` service to begin task scheduling:

```go
// ... Continue in the main function

log.Println("Dcron service is about to start...")
if err := dc.Start(); err != nil {
    log.Fatalf("Failed to start Dcron service: %v", err)
}

// Block the main goroutine, or handle according to your application logic
select {}
```

---

## 🧰 Registry Examples

`dcron` supports multiple registries. Below are examples of how to initialize clients for each supported registry and create a `Dcron` instance.

### Consul Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"github.com/hashicorp/consul/api"
	"log"
	"fmt"
)

func main() {
	// 1. Configure Consul client
	config := api.DefaultConfig()
	config.Address = "localhost:8500" // Consul service address
	client, err := api.NewClient(config)
	if err != nil {
		log.Fatalf("Failed to create Consul client: %v", err)
	}

	// 2. Create Dcron instance using Consul Registry
	dc := dcron.NewDcron(dcron.NewConsulRegistry(client), dcron.WithStrategy(dcron.StrategyConsistent))

	// 3. Add static task example
	err = dc.AddFunc("consul-static-task", "*/5 * * * * *", func() error {
		log.Println("Consul Registry: Executing static task (consul-static-task) every 5 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
    
    // 4. Add dynamic task example (optional)
    err = dc.AddTaskMeta(dcron.TaskMeta{
        Name:       "consul-dynamic-task",
        CronFormat: "*/10 * * * * *",
        Payload:    "Hello from Consul dynamic task",
    })
    if err != nil {
        log.Printf("Failed to add dynamic task consul-dynamic-task: %v", err) // Non-fatal error, can choose to log
    }

	// 5. Start the service
	log.Println("Dcron (Consul) service is starting...")
	if err := dc.Start(); err != nil {
		log.Fatal(err)
	}
    
    // Keep the service running
    select{}
}
```

### Redis Registry Example

```go
package main

import (
	// "context" // If your Redis operations require context
	"github.com/kkangxu/dcron"
	"github.com/redis/go-redis/v9"
	"log"
	"fmt"
)

func main() {
	// 1. Initialize Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379", // Redis service address
	})

	// 2. Create Dcron instance using Redis Registry
	dc := dcron.NewDcron(dcron.NewRedisRegistry(rdb), dcron.WithStrategy(dcron.StrategyConsistent))

	// 3. Add static task example
	err := dc.AddFunc("redis-static-task", "*/10 * * * * *", func() error {
		log.Println("Redis Registry: Executing static task (redis-static-task) every 10 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Start the service
	log.Println("Dcron (Redis) service is starting...")
	if err := dc.Start(); err != nil {
		log.Fatal(err);
	}
    
    select{}
}
```

### Etcd Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"go.etcd.io/etcd/client/v3"
	"log"
	"time"
	"fmt"
)

func main() {
	// 1. Initialize Etcd client
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:2379"}, // Etcd service address
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to Etcd: %v", err)
	}
	defer cli.Close() // Ensure the client is closed

	// 2. Create Dcron instance using Etcd Registry
	dc := dcron.NewDcron(dcron.NewEtcdRegistry(cli), dcron.WithStrategy(dcron.StrategyConsistent))

	// 3. Add static task example
	err = dc.AddFunc("etcd-static-task", "*/3 * * * * *", func() error {
		log.Println("Etcd Registry: Executing static task (etcd-static-task) every 3 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Start the service
	log.Println("Dcron (Etcd) service is starting...")
	if err := dc.Start(); err != nil {
		log.Fatal(err);
	}
    
    select{}
}
```

### ZooKeeper Registry Example

```go
package main

import (
	"github.com/kkangxu/dcron"
	"github.com/go-zookeeper/zk"
	"log"
	"time"
	"fmt"
)

func main() {
	// 1. Connect to ZooKeeper
	conn, _, err := zk.Connect([]string{"127.0.0.1:2181"}, time.Second*5) // ZooKeeper service address
	if err != nil {
		log.Fatalf("Failed to connect to ZooKeeper: %v", err)
	}
	// defer conn.Close() // Typically, zk.Conn is managed internally by dcron, unless you have special needs

	// 2. Create Dcron instance using ZooKeeper Registry
	dc := dcron.NewDcron(dcron.NewZookeeperRegistry(conn), dcron.WithStrategy(dcron.StrategyConsistent))

	// 3. Add static task example
	err = dc.AddFunc("zk-static-task", "*/2 * * * * *", func() error {
		log.Println("ZooKeeper Registry: Executing static task (zk-static-task) every 2 seconds")
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Start the service
	log.Println("Dcron (ZooKeeper) service is starting...")
	if err := dc.Start(); err != nil {
		log.Fatal(err);
	}
    
    select{}
}
```

---

## 🧭 Core Concepts & Mechanisms

### Dynamic Task Deletion & Synchronization

When you need to delete a dynamic task, call `dc.MarkTaskDeleted("task-name")`. This operation marks the task as "deleted" and synchronizes this state to the registry. The benefits of this approach include:

* **Preventing Dirty Data**: Ensures all nodes are aware that the task has been deleted.
* **Avoiding Duplicate Scheduling**: Other nodes will not attempt to schedule tasks that have been marked as deleted.

If you need to re-enable a task with the same name later, it is recommended to use `ForceAddTaskMeta` or first call `CleanupTask` to clear the old marker before adding it again.

### Lifecycle & Node States

In `dcron`, service nodes have clear lifecycle states:

* **`starting`**: Node is in the initialization phase.
* **`working`**: Node has successfully registered with the registry and is functioning normally (listening for tasks, participating in scheduling, etc.).
* **`leaving`**: Node is preparing to shut down or go offline, performing resource cleanup and state updates.

When a node exits, its information in the registry will be handled properly to ensure it does not affect the normal operation of the cluster.

### Distributed Lock Mechanism

One of the core designs of `dcron` is to ensure **idempotent execution** of tasks in a distributed environment, meaning that the same task is executed only once at a given time point across the cluster. This is achieved through the built-in distributed lock mechanism, primarily relying on the `CanRunTask` method in the `Registry` interface:

```go
// CanRunTask checks if the specified task can be executed by the current node at the given execution time.
// Returns: (can run, error)
CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error)
```

**How It Works**:

1. When a node is ready to execute a task, it first calls `CanRunTask`.
2. `CanRunTask` attempts to create a temporary, unique marker (i.e., acquire a lock) for the combination of `taskName` + `execTime` in the registry.
3. If it successfully acquires the lock, it indicates that the current node can execute the task. After execution, the lock is typically released (or it expires automatically).
4. If it fails to acquire the lock (usually meaning another node has already obtained the execution rights for that task at that time), the current node will not execute the task.

**Why Is This Important?**

* **Avoiding Duplicate Execution**: In a distributed system, multiple nodes may simultaneously meet the trigger conditions for a task. Without distributed locks, the same task may be executed by multiple nodes, leading to data inconsistency or other unexpected behaviors.
* **Ensuring Data Consistency**: For tasks that need to modify shared resources, it is crucial to ensure that only one executor is active.
* **Applicable to Various Scenarios**:
    * Nodes frequently join or leave the cluster.
    * Business scenarios that require strict uniqueness in task execution.
    * Tasks with very sensitive execution times that cannot afford delays or failures due to conflicts.

`dcron` encapsulates the implementation details of distributed locks within the specific implementations of each `Registry`, so users do not need to worry about the underlying details, as all built-in registries provided by `dcron` support reliable distributed locks.

---

## ⚙️ Configuration & API Reference

### Optional Configuration Items (Option)

The `dcron` instance is configured through a series of `Option` functions. An `Option` is a function type `func(*dcron)`.

| Option                               | Description                                                                 | Default Behavior/Notes                                     |
|--------------------------------------|-----------------------------------------------------------------------------|-----------------------------------------------------------|
| `WithStrategy(strategy AssignerStrategy)` | Set the task assignment strategy. Possible values:                           | Default is `StrategyConsistent` (consistent hashing)      |
|                                      | `StrategyConsistent` (consistent hashing)                                    |                                                           |
|                                      | `StrategyHashSharding` (average distribution)                               |                                                           |
|                                      | `StrategyHashSlot` (hash slots)                                            |                                                           |
|                                      | `StrategyRange` (range allocation)                                         |                                                           |
|                                      | `StrategyWeighted` (weighted distribution)                                  | Requires node weights                                      |
|                                      | `StrategyRoundRobin` (round-robin)                                        |                                                           |
| `WithAssigner(assigner Assigner)`    | Set a custom task assigner or use the built-in assigner instance from `dcron`. | If this option is set, it will override the effect of `WithStrategy`. |
| `WithCronOptions(...)`               | Customize `github.com/robfig/cron/v3` configuration, such as enabling second-level precision with `cron.WithSeconds()` | Default does not support second-level precision.           |
| `WithTaskRunFunc(handler TaskRunFunc)` | Set the unified execution function for dynamic tasks. `TaskRunFunc` type is `func(*TaskMeta) error`. | **Must be set**, otherwise dynamic tasks cannot be executed. |
| `WithErrHandler(handler ErrHandler)`   | Set the error handling function when a task execution error occurs. `ErrHandler` type is `func(*TaskMeta, error)`. | Default will print errors to logs.                         |
| `WithLogger(log Logger)`             | Set a custom logger that implements the `dcron.Logger` interface.           | Default uses a simple logger based on the standard `log` package. |

**Example:**
```go
import "github.com/robfig/cron/v3"

dc := dcron.NewDcron(
    registry,
    dcron.WithStrategy(dcron.StrategyRoundRobin), // Use round-robin strategy
    dcron.WithCronOptions(cron.WithParser(cron.NewParser(
        cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow, // Support optional second field
    ))),
    // ... Other options
)
```

### Error Handling

You can inject a custom error handling function for task execution through the `WithErrHandler` option. This is useful for centralized error logging, sending alerts, or executing specific recovery logic.

```go
import "github.com/kkangxu/dcron/logger" // Assuming you are using dcron's logger

dc := dcron.NewDcron(
    registry,
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
        // Use your project's logging system to log the error
        logger.Errorf("Task '%s' (Payload: %s) execution failed: %v", task.Name, task.Payload, err)
        // You can add alert logic here, such as sending emails or webhooks
    }),
    // ... Other options
)
```
If you do not set a custom handler through `WithErrHandler`, `dcron` will default to printing error messages to its internal logs (if a logger is configured with `WithLogger`, it will use the custom logger; otherwise, it will use the standard `log` package).

### Dynamic Task Unified Handling Function

All dynamic tasks (tasks added through `AddTaskMeta` or `ForceAddTaskMeta`) are executed by the function set through `WithTaskRunFunc`. This provides a centralized place to manage the execution of dynamic tasks.

```go
dc := dcron.NewDcron(
    registry,
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
        fmt.Printf("Starting execution of dynamic task: %s\n", task.Name)
        fmt.Printf("Task Cron expression: %s\n", task.CronFormat)
        fmt.Printf("Task Payload: %s\n", task.Payload)
        
        // Execute different business logic based on task.Name or task.Payload
        switch task.Name {
        case "send-email-report":
            // Logic for sending email report
            fmt.Println("Sending email report...")
        case "cleanup-temp-files":
            // Logic for cleaning temporary files
            fmt.Println("Cleaning temporary files...")
        default:
            fmt.Printf("Unknown dynamic task type: %s\n", task.Name)
        }
        
        // Return nil if the task executes successfully
        // Return a specific error if the task execution fails
        return nil
    }),
    // ... Other options
)
```
**Important**: If you plan to use dynamic tasks, you **must** provide a handler through `WithTaskRunFunc`, otherwise dynamic tasks will not be executed.

### Dynamic Task API

The following are the main API methods for managing dynamic tasks (called through the `dcron` instance):

* **`AddTaskMeta(meta TaskMeta) error`**
  Adds a new dynamic task. If the task already exists or has been marked as deleted, an error will be returned.
    ```go
    err := dc.AddTaskMeta(dcron.TaskMeta{
        Name:       "new-dynamic-task",
        CronFormat: "0 * * * *", // Every hour
        Payload:    "{"report_type":"hourly"}",
    })
    ```

* **`ForceAddTaskMeta(meta TaskMeta) error`**
  Forcefully adds a dynamic task. If the task already exists or has been marked as deleted, it will overwrite the existing task and clear the related markers.
    ```go
    err := dc.ForceAddTaskMeta(dcron.TaskMeta{
        Name:       "new-dynamic-task", // Can be the same as above
        CronFormat: "0 */2 * * *", // Every two hours
        Payload:    "{"report_type":"bi_hourly"}",
    })
    ```

* **`MarkTaskDeleted(name string) error`**
  Marks the specified dynamic task as deleted. The task will not be immediately removed from the system, but it will not be scheduled for execution anymore. This state will be synchronized to the registry.
    ```go
    err := dc.MarkTaskDeleted("new-dynamic-task")
    ```

### Main API Descriptions

The following table lists the main methods provided by the `dcron` instance and their descriptions.

| Method Name                          | Parameters                               | Return Type | Description                                                                                     |
|--------------------------------------|------------------------------------------|-------------|-------------------------------------------------------------------------------------------------|
| `AddTask`                            | `name string, cronExpr string, tasker Tasker` | `error`     | Adds a static task, where `Tasker` is an interface containing a `Run() error` method.         |
| `AddFunc`                            | `name string, cronExpr string, cmd func() error` | `error`     | Adds a static task, with execution logic provided by the passed function.                      |
| `AddOneShotTask`                    | `name string, cronExpr string, tasker Tasker` | `error`     | Adds a one-time static task. The task will be automatically removed after execution.           |
| `AddOneShotFunc`                    | `name string, cronExpr string, cmd func() error` | `error`     | Adds a one-time static task (function form).                                                  |
| `AddTaskMeta`                       | `meta TaskMeta`                          | `error`     | Adds a dynamic task. Task metadata is stored in the registry. If the task already exists or is deleted, an error will be returned. |
| `ForceAddTask`                      | `name string, cronExpr string, tasker Tasker` | `error`     | Forcefully adds a static task. Ignores whether the task already exists or has been deleted, directly overwriting. |
| `ForceAddFunc`                      | `name string, cronExpr string, cmd func() error` | `error`     | Forcefully adds a static task (function form).                                               |
| `ForceAddOneShotTask`               | `name string, cronExpr string, tasker Tasker` | `error`     | Forcefully adds a one-time static task.                                                       |
| `ForceAddOneShotFunc`               | `name string, cronExpr string, cmd func() error` | `error`     | Forcefully adds a one-time static task (function form).                                       |
| `ForceAddTaskMeta`                  | `meta TaskMeta`                          | `error`     | Forcefully adds a dynamic task. Ignores whether the task already exists or has been deleted, directly overwriting. |
| `ReuseDeletedTask`                  | `name string`                            | `error`     | Reuses a previously marked deleted task. This operation will clear the deleted marker.         |
| `MarkTaskDeleted`                   | `name string`                            | `error`     | Marks the specified dynamic task as deleted. This state will be synchronized to the registry, and the task will no longer be scheduled. |
| `CleanupTask`                       | `ctx context.Context, name string`      | `error`     | **Thoroughly cleans** all related data of the specified task, including its metadata, deleted markers, and other traces in the registry. If the service restarts and the task is re-added through the `Add` series of APIs, it will be treated as a new task. |
| `Start`                              |                                          | `error`     | Starts the `dcron` service, beginning task scheduling and node registration.                   |
| `Stop`                               |                                          | `error`     | Stops the `dcron` service, unregistering nodes, stopping the scheduler, and releasing resources. |
| `GetNodeID`                          |                                          | `string`    | Gets the node ID of the current `dcron` service instance.                                     |
| `GetAllTasks`                        |                                          | `[]string`  | Gets a list of all task names known to the current node (including static and dynamic).       |
| `GetMyselfRunningTasks`              |                                          | `[]string`  | Gets a list of task names currently running on the node (i.e., tasks assigned to the current node and within the scheduling period). |
| `ForceCleanupAllTasks`               | `ctx context.Context`                    | `error`     | **!!! Extremely Dangerous Operation !!!** Forcefully cleans all task metadata and deleted task markers in the registry. **Only for testing environments, and must ensure only one node performs this operation, otherwise it may lead to data confusion.** |

### TaskMeta Structure

`TaskMeta` is used to define the metadata of dynamic tasks.

```go
type TaskMeta struct {
    Name                   string `json:"name"`                                // Unique name of the task
    CronFormat             string `json:"cron_format"`                         // Cron expression (e.g., "*/5 * * * *")
    OneShot                bool   `json:"one_shot,omitempty"`                  // Indicates if it is a one-time task. If true, the behavior after execution depends on the following two fields.
    ExecutedAndMarkDeleted bool   `json:"executed_and_mark_deleted,omitempty"` // If OneShot is true, this field being true will mark the task as deleted after execution.
    ExecutedAndCleanup     bool   `json:"executed_and_cleanup,omitempty"`      // If OneShot is true, this field being true will thoroughly clean the task after execution (calling CleanupTask). This option takes precedence over ExecutedAndMarkDeleted.
    Payload                string `json:"payload,omitempty"`                   // Custom data passed to the task execution function (usually a JSON string).
}
```

### Node Structure

`Node` represents a service node in the distributed cluster.

```go
type Node struct {
    ID         string     `json:"id"`          // Unique ID of the node (usually auto-generated)
    IP         string     `json:"ip"`          // IP address of the node
    Hostname   string     `json:"hostname"`    // Hostname of the node
    LastAlive  time.Time  `json:"last_alive"`  // Last heartbeat time of the node, used for health checks
    CreateTime time.Time  `json:"create_time"` // Registration creation time of the node
    Status     NodeStatus `json:"status"`      // Current status of the node: "starting", "working", or "leaving"
}

// NodeStatus defines the type of node status
type NodeStatus string

const (
    NodeStatusStarting NodeStatus = "starting"
    NodeStatusWorking  NodeStatus = "working"
    NodeStatusLeaving  NodeStatus = "leaving"
)
```

### NodeEvent & TaskEvent

`dcron` allows you to listen for changes in node states and task metadata changes through the `Registry` interface.

```go
// NodeEvent represents a node change event
type NodeEvent struct {
    Type NodeEventType // Event type: NodeEventTypePut (add/update), NodeEventTypeDelete (delete)
    Node Node          // Related node information
}

// NodeEventType defines the type of node event
type NodeEventType string

const (
    NodeEventTypePut    NodeEventType = "put"    // Node added or updated
    NodeEventTypeDelete NodeEventType = "delete" // Node deleted
)

// TaskMetaEvent represents a task metadata change event
type TaskMetaEvent struct {
    Type TaskEventType // Event type: TaskEventTypePut (add/update), TaskEventTypeDelete (delete)
    Task TaskMeta      // Related task metadata
}

// TaskEventType defines the type of task event
type TaskEventType string

const (
    TaskEventTypePut    TaskEventType = "put"    // Task added or metadata updated
    TaskEventTypeDelete TaskEventType = "delete" // Task marked as deleted (MarkTaskDeleted)
)
```

You can obtain these events' channels through the `WatchNodes(ctx context.Context) (<-chan []NodeEvent, error)` and `WatchTaskEvent(ctx context.Context) (<-chan []TaskMetaEvent, error)` methods of the `Registry` interface, allowing your application to respond to changes in the cluster's state.

---

## ❓ Frequently Asked Questions (FAQ) / Usage Suggestions

1. **Q: Why can't I directly add a task with the same name after calling `MarkTaskDeleted`?**  
   A: When you call `MarkTaskDeleted("task-name")`, the system records a "deleted" marker for that task name in the registry. This prevents other nodes from mistakenly scheduling a task that has been deleted due to network delays or other reasons. This marker acts as a "tombstone."
   If you need to reuse this task name, you can:
    * **Use `ForceAddTaskMeta`**: This method will ignore all existing markers (including "deleted" markers) and forcefully overwrite or create the task.
    * **Call `CleanupTask` first, then `AddTaskMeta`**: `CleanupTask("task-name")` will thoroughly clear all traces of that task name in the registry, including metadata and "deleted" markers. After that, you can use `AddTaskMeta` as if it were a brand new task.
    * **Use `ReuseDeletedTask`**: This method is specifically designed to "revive" a task that has been marked as deleted, clearing the deletion marker so that the task can be rescheduled (if its metadata still exists).

2. **Q: What is the core difference between `ForceAddTask` series methods and regular `Add` series methods?**  
   A: The core difference lies in how they handle existing tasks or markers:
    * **`Add` series methods** (like `AddTask`, `AddTaskMeta`): These methods check for existing tasks before adding. If a task with the same name exists or has been marked as deleted, they typically return an error to prevent accidental overwriting or conflicts with old states.
    * **`ForceAdd` series methods** (like `ForceAddTask`, `ForceAddTaskMeta`): As the name suggests, these methods "force" execution. They ignore whether the task already exists or has been deleted, directly creating or overwriting the task. This usually means they will first clean up any old records related to that task name (including metadata, deleted markers, and possible execution locks) before writing the new task information. This makes them very suitable for scenarios where you need to "reset" or "ensure the latest configuration takes effect."

3. **Q: How should I choose between dynamic and static tasks?**  
   A:
    * **Static Tasks**:
        * **Definition**: Typically defined in code through `AddFunc` or `AddTask` directly.
        * **Lifecycle**: Loaded with the service instance and stopped when the service instance stops. Their definitions are hardcoded in the application.
        * **Management**: Modifying static tasks usually requires recompiling and redeploying the code.
        * **Use Cases**: Suitable for tasks that are fixed, do not change often, and are closely related to core application functionality, such as periodic log rotation, system health checks, or fixed data synchronization.
    * **Dynamic Tasks**:
        * **Definition**: Added at runtime through `AddTaskMeta` API, with their metadata stored in the registry.
        * **Lifecycle**: Independent of the deployment of service instances. Once added to the registry, dynamic tasks can be discovered and scheduled by `dcron` nodes. They can be controlled through APIs for addition, deletion, and modification.
        * **Management**: More flexible, allowing for dynamic control of task execution through external systems (like management dashboards or operational scripts).
        * **Use Cases**:
            * Tasks that need to be dynamically created and managed by operations or users (e.g., user-defined reminders, temporary tasks related to marketing activities).
            * Tasks that require frequent adjustments to execution times or parameters.
            * One-time or temporary data processing or operational tasks.

4. **Q: How can I thoroughly clean up all traces of a task in the registry?**  
   A: Call the `dcron` instance's `CleanupTask(ctx context.Context, taskName string)` method. This method will:
    * Delete the task's metadata (`TaskMeta`).
    * Clear the "deleted" marker for that task (if it exists).
    * Attempt to clean up other potential data related to that task, such as last execution time records, distributed lock markers, etc. (specifics depend on the registry implementation).
      After calling this method, the `taskName` will no longer have any associated information in the registry and can be considered a fully available task name.
      If you need to clean all tasks at once (**very dangerous, only for testing!**), you can use `ForceCleanupAllTasks(ctx context.Context)`.

5. **Q: Why does `dcron` need a "deleted task synchronization" mechanism?**  
   A: In a distributed system, there may be delays in state synchronization among nodes. Without a clear "deleted task" list and synchronization across nodes, the following issues may arise:
    * A node deletes task A, but other nodes may still attempt to schedule it due to network delays or outdated information.
    * If a deleted task is re-added before its old instances are cleaned up, it may lead to conflicts or unexpected behavior.
      By maintaining a "deleted task list" in the registry and allowing all `dcron` nodes to be aware of this list, it ensures:
    * **Global Consistency**: All nodes reach a consensus on which tasks are in a "deleted" state.
    * **Preventing "Zombie" Tasks**: Avoids mistakenly reactivating or executing tasks that have been deleted.
    * **Idempotency Assurance**: Works in conjunction with the distributed lock mechanism to further enhance the accuracy of task scheduling.

6. **Q: How to gracefully shut down the `dcron` service and take nodes offline?**  
   A: When your application is ready to shut down, you should call the `Stop()` method of the `dcron` instance. This method will perform the following operations to ensure a graceful shutdown:
    1. **Update Node Status**: Change the current node's status in the registry to `leaving`. This notifies other nodes that this node is about to go offline, and they will consider this when redistributing tasks.
    2. **Stop Local Scheduler**: Stops the cron scheduler, preventing new task executions. Ongoing tasks are typically allowed to finish (but this depends on whether the task implementation can respond to interrupt signals).
    3. **Unregister Node Information**: Removes the current node's registration information from the registry.
    4. **Release Resources**: Closes connections to the registry (if applicable) and releases other internal resources.
       By executing these steps, you can minimize the impact of node shutdown on task interruptions or duplicate scheduling, ensuring smooth operation of the cluster.

7. **Q: What registries does `dcron` support? If I want to switch from Consul to Redis, will I need to change a lot of code?**  
   A: `dcron` currently supports built-in registries for Consul, Redis, Etcd, and ZooKeeper.
   Switching registries is very simple because the core logic of `dcron` is decoupled from the `Registry` interface. The main changes you need to make are:
    1. **Modify Dependencies**: Ensure that your `go.mod` includes the client library for the target registry (e.g., switch from `github.com/hashicorp/consul/api` to `github.com/redis/go-redis/v9`).
    2. **Modify Initialization Code**: When creating the `Dcron` instance, pass in the new `Registry` implementation. For example:
       ```go
       // Original Consul initialization
       // import "github.com/hashicorp/consul/api"
       // consulClient, _ := api.NewClient(api.DefaultConfig())
       // registry := dcron.NewConsulRegistry(consulClient)
 
       // New Redis initialization
       import "github.com/redis/go-redis/v9"
       rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
       registry := dcron.NewRedisRegistry(rdb) // Just change here
 
       dc := dcron.NewDcron(registry, /* ... options ... */)
       ```
   Your task definitions (static tasks' `AddFunc` calls, dynamic tasks' `TaskMeta` structure) and other `dcron` configuration options (like `WithStrategy`, `WithErrHandler`, etc.) typically do not require any modifications.

8. **Q: How can I listen for changes in nodes or tasks in the cluster, such as implementing a monitoring dashboard?**  
   A: The `Registry` interface provides a `Watch` mechanism to listen for these changes:
    * **Listen for Node Changes**: `registry.WatchNodes(ctx context.Context) (<-chan []NodeEvent, error)`
      This method returns a channel that will send `NodeEvent` slices when nodes join, leave, or update their status.
    * **Listen for Task Metadata Changes**: `registry.WatchTaskEvent(ctx context.Context) (<-chan []TaskMetaEvent, error)`
      Similarly, this method returns a channel that will send `TaskMetaEvent` slices when dynamic tasks are added, modified, or marked as deleted.

   You can start a goroutine in your application to consume these channels and update your monitoring dashboard, send notifications, or perform other automated operational tasks based on the events.
   ```go
   // Example: Listening for node events
   go func() {
       nodeEventsChan, err := registry.WatchNodes(context.Background()) // Use appropriate context
       if err != nil {
           log.Printf("Unable to listen for node events: %v", err)
           return
       }
       for events := range nodeEventsChan {
           for _, event := range events {
               log.Printf("Node event: Type=%s, NodeID=%s, Status=%s", event.Type, event.Node.ID, event.Node.Status)
               // Update your monitoring system here
           }
       }
   }()
   ```

9. **Q: How can I implement a unified execution logic for all dynamic tasks?**  
   A: Through the `dcron.WithTaskRunFunc(handler func(*dcron.TaskMeta) error)` option. You need to provide a function that takes a `*dcron.TaskMeta` parameter and returns an `error`. When any dynamic task reaches its execution time and is selected for execution by the current node, `dcron` will call the handler function you provided and pass the task's `TaskMeta` information to it.
   Inside this handler function, you can:
    * Distinguish different dynamic tasks by `task.Name`.
    * Access the custom data passed to the task via `task.Payload`.
    * Execute the corresponding business logic.
    * Return `nil` to indicate successful execution, or return an error to indicate failure (this will trigger the error handler set through `WithErrHandler`).
      This is the core extension point for implementing dynamic task scheduling functionality.

10. **Q: What task assignment strategies does `dcron` provide, and how should I choose?**  
    A: The choice of strategy depends on your specific needs and cluster characteristics:
    * **`StrategyConsistent` (Consistent Hashing)**:
        * **Advantages**: When the number of nodes changes (increased or decreased), only a small number of tasks will be redistributed, while most tasks' assignments remain stable. Task distribution among nodes is relatively even.
        * **Use Cases**: Environments where nodes may frequently change (e.g., elastic scaling clusters), a large number of tasks, and a desire for smooth task distribution transitions.
    * **`StrategyHashSharding` (Average Distribution / Hash Modulus)**:
        * **Advantages**: Simple implementation with low computational overhead. When the number of nodes is fixed, task distribution is very even.
        * **Disadvantages**: When the number of nodes changes, most tasks' assignments will change, potentially leading to significant task migration.
        * **Use Cases**: Stable node counts, moderate task volumes, and insensitivity to task migration during node changes.
    * **`StrategyHashSlot` (Hash Slots)**:
        * **Advantages**: Maps tasks to a fixed number of slots, then assigns slots to nodes. When nodes are added or removed, only a few slots and their tasks need to be migrated, resulting in smoother transitions than pure hash modulus. Can achieve finer-grained load balancing.
        * **Use Cases**: Scenarios where task distribution needs to be more stable during node changes while maintaining good uniformity, similar to the slot concept in Redis Cluster.
    * **`StrategyRange` (Range Allocation)**:
        * **Advantages**: Allocates continuous blocks of tasks to nodes based on the sorted order of task names. Suitable for tasks with some order or business correlation.
        * **Disadvantages**: If task names are unevenly distributed, it may lead to uneven node loads.
        * **Use Cases**: Tasks that can be logically partitioned by name, such as alphabetical or numerical ranges.
    * **`StrategyWeighted` (Weighted Distribution)**:
        * **Advantages**: Allows different nodes to be assigned different weights, enabling more capable nodes to handle more tasks.
        * **Disadvantages**: Requires prior assessment and configuration of each node's weight.
        * **Use Cases**: Scenarios where nodes have varying processing capabilities (e.g., different machine configurations).
    * **`StrategyRoundRobin` (Round Robin)**:
        * **Advantages**: Simple and fair, assigning tasks to each node in turn.
        * **Disadvantages**: Does not consider the characteristics of tasks or the current load of nodes. If task execution times vary significantly, some nodes may be idle while others are busy.
        * **Use Cases**: Scenarios where task execution times are similar, node capabilities are equal, and a simple fair distribution is desired.

    **Selection Recommendations**:
    * For most general scenarios, **`StrategyConsistent` (Consistent Hashing)** is a good default choice due to its balanced performance in dynamic environments and load balancing.
    * If your nodes are very stable, consider **`StrategyHashSharding`** or **`StrategyRoundRobin`** for simplicity.
    * If you require better stability during node changes and want finer control than consistent hashing, consider **`StrategyHashSlot`**.
    * Specific business scenarios (like partitioning by name or heterogeneous nodes) would correspond to **`StrategyRange`** or **`StrategyWeighted`**.

---

## 📝 Summary

The `dcron` project aims to provide a powerful, flexible, and extensible distributed cron job solution. By supporting multiple mainstream registries and rich task management features, it can help you efficiently and reliably schedule and manage cron jobs in microservices architectures.

🔧 **Recommended Uses**:
* Unified scheduling and management of various cron jobs in microservices architectures.
* Scheduled tasks that require high availability and load balancing.
* Dynamically issuing, controlling, and monitoring scheduled tasks through a backend management interface or operational scripts.
* Implementing one-time, periodic, or temporary automated operations and data processing jobs.

We encourage you to choose the appropriate registry and configuration options based on your project's actual needs and to fully utilize the features provided by `dcron`. If you have any questions or suggestions, feel free to communicate with us through [GitHub Issues](https://github.com/kkangxu/dcron/issues) (assuming project address).
