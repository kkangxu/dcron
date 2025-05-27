
# 📚 `dcron` 分布式定时任务系统使用文档

`dcron` 是一个基于多种注册中心（如 Consul、Redis、Etcd、ZooKeeper）实现的分布式定时任务系统。它支持静态任务和动态任务的管理，并提供服务节点的发现与负载均衡机制，旨在为微服务架构中的定时任务调度提供高效、可靠的解决方案。

---

## 🧩 功能特性

- **多种注册中心支持**：支持 Consul、Redis、Etcd 和 Zookeeper 等多种注册中心，用户可以根据需求选择合适的实现。
- **静态与动态任务管理**：支持静态任务（预先定义）和动态任务（运行时添加），灵活应对不同场景的需求。
- **节点变化监听**：提供 Poll 模式和 Watch 模式相结合，确保任务调度的及时性。
- **任务调度策略**：支持多种任务分配策略，包括一致性哈希、平均分配、哈希槽、范围分配、加权分配和轮询等，优化任务在节点间的分配。
- **节点状态管理**：提供节点状态管理功能，支持节点的启动、工作和退出状态。
- **动态任务的灵活性**：动态任务可以设置为一次性任务（OneShot），并支持自定义数据（Payload）。
- **错误处理机制**：支持任务执行错误的回调处理，确保任务执行的可靠性。
- **已删除任务的同步**：动态任务删除后，系统会自动同步到注册中心的已删除任务列表，避免脏数据。
- **分布式锁机制**：通过 CanRunTask 接口实现分布式锁，确保任务在集群中只被执行一次。
- **任务生命周期管理**：支持任务执行后自动删除或清理，满足一次性任务和临时任务的需求。

---

## 📦 安装依赖
在使用 `dcron` 之前，请确保安装以下依赖：
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
## 🧰 快速开始

### 1. 初始化注册中心客户端（以 Consul 为例）

首先，您需要初始化一个注册中心客户端。以下是使用 Consul 的示例代码：

```go
import (
    "github.com/kkangxu/dcron"
    "github.com/hashicorp/consul/api"
)

config := api.DefaultConfig()
config.Address = "localhost:8500" // Consul 地址
client, err := api.NewClient(config)
if err != nil {
    log.Fatalf("创建 Consul 客户端失败: %v", err)
}
registry := dcron.NewConsulRegistry(client)
```

### 2. 创建 Dcron 实例

接下来，您可以创建一个 `Dcron` 实例并配置选项：

```go
dc := dcron.NewDcron(
    registry,
    dcron.WithStrategy(dcron.StrategyConsistent), // 使用一致性哈希策略
    dcron.WithCronOptions(cron.WithSeconds()),    // 设置为秒级精度
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
        fmt.Println("执行动态任务:", task.Name, "Payload:", task.Payload)
        return nil
    }),
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
        fmt.Printf("任务 %s 执行出错: %v\n", task.Name, err)
    }),
)
```

### 3. 添加静态任务

您可以通过以下方式添加静态任务：

```go
err := dc.AddFunc("static-task", "*/5 * * * * *", func() error {
    fmt.Println("每5秒执行一次静态任务")
    return nil
})
if err != nil {
    log.Fatal(err)
}
```

#### 🚩 强制添加任务（无视已存在、已删除等限制）

有时你需要无视任务是否已存在、已被删除、已执行等限制，强制添加任务，可以使用 ForceAddTask/ForceAddFunc/ForceAddOneShotTask/ForceAddOneShotFunc：

```go
// 强制添加普通任务
_ = dc.ForceAddFunc("force-task", "*/3 * * * * *", func() error {
    fmt.Println("强制添加的任务，每3秒执行一次")
    return nil
})

// 强制添加一次性任务
_ = dc.ForceAddOneShotFunc("force-oneshot", "*/10 * * * * *", func() error {
    fmt.Println("强制添加的一次性任务")
    return nil
})
```

> 这些方法会直接覆盖同名任务，无视已存在、已删除、已执行等限制。

### 4. 添加动态任务

动态任务的添加示例如下：

```go
err := dc.AddTaskMeta(dcron.TaskMeta{
    Name:       "dynamic-task",
    CronFormat: "*/10 * * * * *", // 每10秒执行一次
    Payload:    "hello from dynamic task",
})
if err != nil {
    log.Fatal(err)
}
```

### 5. 启动服务

最后，启动 `Dcron` 服务：

```go
if err := dc.Start(); err != nil {
    log.Fatal(err)
}
```

---

## 🧰 注册中心示例

以下是使用不同注册中心的示例代码，展示如何初始化客户端、创建 `Dcron` 实例并添加任务。

#### Consul Registry 示例

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

#### Redis Registry 示例

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

#### Etcd Registry 示例

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

#### ZooKeeper Registry 示例

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
		log.Fatal(err)
	}
}
```

---

## 🧭 动态任务删除与同步

- 删除动态任务时，调用 `MarkTaskDeleted("task-name")`，会自动同步到注册中心的已删除任务列表，避免脏数据。

## 📝 生命周期与节点状态

- 启动时节点状态为 `starting`
- 注册成功后更新为 `working`
- 节点退出时状态更新为 `leaving`
- 节点信息彻底删除

---

## ⚙️ 可选配置项（Option）
`Option` 是一个函数类型，用于配置 `Dcron` 实例的选项。以下是可用的选项函数：

```go
type Option func(*dcron)
```

| Option | 描述                     |
|--------|------------------------|
| `WithStrategy(StrategyConsistent)` | 设置任务分配策略：一致性哈希         |
| `WithStrategy(StrategyHashSharding)` | 设置任务分配策略：平均分配          |
| `WithStrategy(StrategyHashSlot)` | 设置任务分配策略：哈希槽           |
| `WithStrategy(StrategyRange)` | 设置任务分配策略：范围分配          |
| `WithStrategy(StrategyWeighted)` | 设置任务分配策略：加权分配          |
| `WithStrategy(StrategyRoundRobin)` | 设置任务分配策略：轮询            |
| `WithAssigner(Assigner)` | 设置自定义任务分配器或者dcron 内置的分配器      |
| `WithCronOptions(...)` | 自定义 Cron 配置（如秒级精度）     |
| `WithTaskRunFunc(handler)` | 设置动态任务执行函数             |
| `WithErrHandler(handler ErrHandler)` | 设置任务执行错误处理函数。          |
| `WithLogger(log Logger)` | 设置自定定义日志，如果没有设置就使用默认日志 |

---

## 🧾 错误处理

可通过 `WithErrHandler` 注入任务执行错误处理函数：

```go
dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
    logger.Infof("任务 %s 执行出错: %v", task.Name, err)
})
```

---

## 🧾 动态任务统一处理函数

可通过 `WithTaskRunFunc` 注入动态任务执行函数：

```go
dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
    fmt.Println("执行动态任务:", task.Name, "Payload:", task.Payload)
    return nil
}),
```

---

## 🛠️ 动态任务 API

```go
// 添加动态任务
err := dc.AddTaskMeta(TaskMeta{
    Name:       "task-name",
    CronFormat: "*/5 * * * * *",
    Payload:    "demo payload",
})

// 移除动态任务
err := dc.MarkTaskDeleted("task-name")
```

---

## 🔒 分布式锁机制

dcron 使用注册中心提供的分布式锁机制确保任务在集群中只被执行一次。这通过 `CanRunTask` 接口实现：

```go
// CanRunTask 确定特定任务是否可以在给定时间执行
// 它使用分布式锁确保集群中只有一个节点执行任务实例
CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error)
```

当节点尝试执行任务时，它会首先检查该任务在当前时间点是否已被其他节点执行。这种机制特别适用于：

1. 节点变化频繁的场景
2. 需要严格保证任务执行一次的场景
3. 任务执行时间敏感的场景

---

## 📊 API 描述

| 方法名 | 描述                                                                               |
|--|----------------------------------------------------------------------------------|
| AddTask(name, cron, tasker) | 添加静态任务                                                                           |
| AddFunc(name, cron, func) | 添加静态任务（函数）                                                                       |
| AddOneShotTask(name, cron, tasker) | 添加一次性任务                                                                          |
| AddOneShotFunc(name, cron, func) | 添加一次性任务（函数）                                                                      |
| AddTaskMeta(meta) | 添加动态任务                                                                           |
| ForceAddTask(name, cron, tasker) | 强制添加静态任务（忽略已存在/已删除状态）                                                            |
| ForceAddFunc(name, cron, func) | 强制添加静态任务（函数）                                                                     |
| ForceAddOneShotTask(name, cron, tasker) | 强制添加一次性任务                                                                        |
| ForceAddOneShotFunc(name, cron, func) | 强制添加一次性任务（函数）                                                                    |
| ForceAddTaskMeta(meta) | 强制添加动态任务（忽略已存在/已删除状态）                                                            |
| ReuseDeletedTask(name) | 重新使用被标记为已删除的任务                                                                   |
| MarkTaskDeleted(name) | 任务标记为已删除。标记保存在注册中心。服务重启或者重新Addxxx API, 也不会成功，也不会被执行。如果要添加相同的任务，请使用 ForceXXX API。 |
| CleanupTask(name) | 完全清理任务的所有数据。如果服务重启，Addxxx/Forcexxx API 运行，会导致任务重新加入。                             |
| Start() | 启动服务                                                                             |
| Stop() | 停止服务                                                                             |
| GetNodeID() | 获取当前节点 ID                                                                        |
| GetAllTasks() | 获取所有任务名称                                                                         |
| GetMyselfRunningTasks() | 获取当前节点正在运行的任务名称                                                                  |
| ForceCleanupAllTasks(ctx) | 强制清理所有任务和已删除任务。!!!危险!!! 仅用于测试，只能由一个节点使用。                                         |

---

## 📦 TaskMeta 结构体

```go
type TaskMeta struct {
    Name                   string `json:"name"`                                // 任务名称
    CronFormat             string `json:"cron_format"`                         // cron 格式
    OneShot                bool   `json:"one_shot,omitempty"`                  // 一次性任务标志
    ExecutedAndMarkDeleted bool   `json:"executed_and_mark_deleted,omitempty"` // 任务执行后，删除任务并标记为已删除
    ExecutedAndCleanup     bool   `json:"executed_and_cleanup,omitempty"`      // 任务执行后，完全删除任务数据
    Payload                string `json:"payload,omitempty"`                   // 负载
}
```

---

## 📝 Node 结构体

```go
type Node struct {
    ID         string     `json:"id"`          // 节点 ID
    IP         string     `json:"ip"`          // IP 地址
    Hostname   string     `json:"hostname"`    // 主机名
    LastAlive  time.Time  `json:"last_alive"`  // 最后心跳时间
    CreateTime time.Time  `json:"create_time"` // 创建时间
    Status     NodeStatus `json:"status"`      // 状态（starting/working/leaving）
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

## ❓ 常见问题（FAQ）/使用建议
1. **为什么 MarkTaskDeleted 后不能直接 AddTask？**
    - 当你调用 `MarkTaskDeleted("task-name")` 后，系统会在注册中心为该任务名打上"已删除"标记，防止脏数据和重复调度。
    - 如果你想再次添加同名任务，推荐使用 `ForceAddTask` 或 `ForceCleanupTask`，这样可以彻底清理旧痕迹，保证任务状态一致。

2. **ForceAddTask 和普通 AddTask 有什么区别？**
    - `AddTask` 会检测任务是否已存在或已被删除，若存在则报错，防止重复。
    - `ForceAddTask` 会自动清理注册中心相关痕迹（包括已删除标记、执行记录等），彻底覆盖同名任务，适合"强制重置"场景。

3. **动态任务和静态任务的区别？**
    - 静态任务：本地管理，适合代码中预定义的定时任务，节点重启后会自动恢复。
    - 动态任务：依赖注册中心同步，适合后台动态下发、运维控制等场景，支持运行时增删改。

4. **如何彻底清理一个任务的所有注册中心痕迹？**
    - 调用注册中心的 `ForceCleanupTask(ctx, taskName)` 方法，可以同时删除任务元数据、已删除标记和最后执行时间。
    - 适用于需要"完全重置"任务状态的场景。

5. **为什么要有"已删除任务同步"机制？**
    - 分布式环境下，节点间可能存在数据不一致。通过同步"已删除任务"列表，可以防止已被删除的任务被其他节点重新调度，保证全局一致性和幂等性。

6. **如何优雅停机和节点下线？**
    - 调用 `Stop()` 方法，dcron 会自动：
        - 更新节点状态为 `leaving`
        - 停止本地调度器
        - 注销节点信息
        - 释放资源，保证任务不会重复调度

7. **支持哪些注册中心？如何切换？**
    - 支持 Consul、Redis、Etcd、Zookeeper。
    - 只需初始化对应的 registry 实例，传入 `NewDcron` 即可，无需修改业务代码。

8. **如何监听节点或任务的变化？**
    - 可通过 `WatchNodes`、`WatchTaskEvent` 等接口，监听节点上下线、任务增删改等事件，适合做监控、自动化运维等扩展。

9. **如何自定义动态任务的执行逻辑？**
    - 通过 `WithTaskRunFunc` 注入自定义处理函数，所有动态任务都会回调到该函数，便于统一处理和扩展。

10. **任务分配策略如何选择？**
    - `StrategyConsistent`（一致性哈希）：适合节点频繁变动、任务量大场景，分配更均匀。
    - `StrategyHashSharding`（平均分配）：适合节点数量稳定、任务量较少场景，简单高效。
    - `StrategyHashSlot`（哈希槽）：将任务分配到多个槽中，确保每个节点获得的槽数尽可能均匀。
    - `StrategyRange`（范围分配）：根据任务名称的范围进行分配，适合有序任务的场景。
    - `StrategyWeighted`（加权分配）：根据节点的权重进行任务分配，适合不同节点处理能力不均的场景。
    - `StrategyRoundRobin`（轮询）：通过轮询的方式将任务分配给节点，确保每个节点均匀接收任务。

---

## 📝 总结

这个项目展示了如何通过多种注册中心构建一个可靠的分布式定时任务系统。你可以根据你的架构选择合适的注册中心实现，同时灵活地添加静态任务和动态任务。所有的 Registry 实现都统一了接口，便于扩展和替换。

🔧 **推荐用途**：
- 微服务中定时任务的统一调度
- 高可用场景下的任务负载均衡
- 动态下发定时任务（例如后台管理控制台）

---
