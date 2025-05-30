
# 📚 `dcron` 分布式定时任务系统使用文档

`dcron` 是一个基于多种注册中心（如 Consul、Redis、Etcd、ZooKeeper）实现的分布式定时任务系统。它支持静态任务和动态任务的管理，并提供服务节点的发现与负载均衡机制，旨在为微服务架构中的定时任务调度提供高效、可靠的解决方案。

---

## 导航

*   [功能特性](#-功能特性)
*   [安装依赖](#-安装依赖)
*   [快速开始](#-快速开始)
    *   [1. 初始化注册中心客户端](#1-初始化注册中心客户端以-consul-为例)
    *   [2. 创建 Dcron 实例](#2-创建-dcron-实例)
    *   [3. 添加静态任务](#3-添加静态任务)
    *   [4. 添加动态任务](#4-添加动态任务)
    *   [5. 启动服务](#5-启动服务)
*   [注册中心示例](#-注册中心示例)
    *   [Consul Registry 示例](#consul-registry-示例)
    *   [Redis Registry 示例](#redis-registry-示例)
    *   [Etcd Registry 示例](#etcd-registry-示例)
    *   [ZooKeeper Registry 示例](#zookeeper-registry-示例)
*   [核心概念与机制](#-核心概念与机制)
    *   [动态任务删除与同步](#动态任务删除与同步)
    *   [生命周期与节点状态](#生命周期与节点状态)
    *   [分布式锁机制](#分布式锁机制)
*   [配置与 API 参考](#-配置与-api-参考)
    *   [可选配置项 (Option)](#可选配置项-option)
    *   [错误处理](#错误处理)
    *   [动态任务统一处理函数](#动态任务统一处理函数)
    *   [动态任务 API](#动态任务-api)
    *   [主要 API 描述](#主要-api-描述)
    *   [TaskMeta 结构体](#taskmeta-结构体)
    *   [Node 结构体](#node-结构体)
    *   [NodeEvent & TaskEvent](#nodeevent--taskevent)
*   [常见问题 (FAQ) / 使用建议](#-常见问题-faq--使用建议)
*   [总结](#-总结)

---

## 🧩 功能特性

*   **✨ 多种注册中心支持**：无缝对接 Consul、Redis、Etcd 和 Zookeeper，灵活选择，轻松切换。
*   **🚀 静态与动态任务管理**：既支持代码中预定义的静态任务，也支持运行时动态添加、删除和修改的任务，满足各种调度需求。
*   **👀 智能节点变化监听**：结合 Poll 和 Watch 模式，实时感知节点变化，确保任务调度的高可用和及时性。
*   **🎯 丰富任务调度策略**：内置一致性哈希、平均分配、哈希槽、范围分配、加权分配和轮询等多种策略，优化任务分配，提升系统负载均衡能力。
*   **🚦 精细节点状态管理**：清晰管理节点从启动 (`starting`)、工作 (`working`) 到退出 (`leaving`) 的完整生命周期。
*   **🔧 灵活的动态任务配置**：动态任务支持一次性执行 (OneShot) 并可携带自定义数据 (Payload)。
*   **🛡️ 可靠错误处理机制**：提供任务执行错误的回调处理，保障任务执行的稳定性和可追溯性。
*   **🔄 已删除任务智能同步**：动态任务删除后，自动同步至注册中心已删除列表，有效防止脏数据和任务重跑。
*   **🔒 内置分布式锁**：通过 `CanRunTask` 接口实现分布式锁，确保同一任务在集群中仅被执行一次，避免并发冲突。
*   **♻️ 完善任务生命周期管理**：支持任务执行后自动删除或清理，特别适合一次性任务和临时性任务场景。

---

## 📦 安装依赖

在使用 `dcron` 之前，请确保您的项目中已安装以下依赖：

```bash
# Consul Registry (如果您选择 Consul)
go get github.com/hashicorp/consul/api

# Redis Registry (如果您选择 Redis)
go get github.com/redis/go-redis/v9

# Etcd Registry (如果您选择 Etcd)
go get go.etcd.io/etcd/client/v3

# ZooKeeper Registry (如果您选择 ZooKeeper)
go get github.com/go-zookeeper/zk

# Cron Scheduler (核心调度库)
go get github.com/robfig/cron/v3
```

--- 

## 🧰 快速开始

本节将以 Consul 作为注册中心，引导您快速上手 `dcron`。

### 1. 初始化注册中心客户端（以 Consul 为例）

首先，您需要初始化一个注册中心客户端。以下是使用 Consul 的示例代码：

```go
import (
    "log"
    "github.com/kkangxu/dcron"
    "github.com/hashicorp/consul/api"
)

func main() {
    // 配置 Consul 客户端
    config := api.DefaultConfig()
    config.Address = "localhost:8500" // Consul 服务地址
    client, err := api.NewClient(config)
    if err != nil {
        log.Fatalf("创建 Consul 客户端失败: %v", err)
    }

    // 基于 Consul 客户端创建 dcron Registry
    registry := dcron.NewConsulRegistry(client)
    
    // ... 接下来创建 Dcron 实例
}
```

> **提示**：其他注册中心（Redis, Etcd, ZooKeeper）的初始化方式类似，请参考 [注册中心示例](#-注册中心示例) 部分。

### 2. 创建 Dcron 实例

接下来，您可以创建一个 `Dcron` 实例并配置相关选项：

```go
import (
    "fmt"
    "log"
    "github.com/kkangxu/dcron"
    "github.com/robfig/cron/v3" // 用于 cron Options
    // ... 其他 import
)

// ... 在 main 函数中继续

// 创建 Dcron 实例
dc := dcron.NewDcron(
    registry, // 上一步创建的 registry 实例
    dcron.WithStrategy(dcron.StrategyConsistent), // 设置任务分配策略为一致性哈希
    dcron.WithCronOptions(cron.WithSeconds()),    // 设置 Cron 表达式支持秒级精度
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error { // 设置动态任务的统一执行函数
        fmt.Println("执行动态任务:", task.Name, "Payload:", task.Payload)
        return nil
    }),
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) { // 设置任务执行错误处理函数
        fmt.Printf("任务 %s 执行出错: %v\n", task.Name, err)
    }),
)
```

### 3. 添加静态任务

静态任务通常在代码中定义，随服务启动而加载。

```go
// ... 在 main 函数中继续

// 添加一个每5秒执行一次的静态任务
err = dc.AddFunc("static-task-example", "*/5 * * * * *", func() error {
    fmt.Println("每5秒执行一次静态任务 (static-task-example)")
    return nil
})
if err != nil {
    log.Fatalf("添加静态任务失败: %v", err)
}
```

#### 🚩 强制添加任务（无视限制）

如果您需要无视任务是否已存在、已被删除等限制，强制添加或覆盖任务，可以使用 `ForceAdd` 系列方法：

```go
// 强制添加一个普通任务，即使已存在或已删除也会覆盖
_ = dc.ForceAddFunc("force-task-example", "*/3 * * * * *", func() error {
    fmt.Println("强制添加的任务 (force-task-example)，每3秒执行一次")
    return nil
})

// 强制添加一个一次性任务
_ = dc.ForceAddOneShotFunc("force-oneshot-example", "*/10 * * * * *", func() error {
    fmt.Println("强制添加的一次性任务 (force-oneshot-example)")
    return nil // 任务执行后会自动清理
})
```
> **注意**：`ForceAdd` 系列方法会直接覆盖同名任务，并清除其在注册中心的相关记录（如已删除标记）。请谨慎使用。

### 4. 添加动态任务

动态任务可以在服务运行时通过 API 添加，其元数据存储在注册中心。

```go
// ... 在 main 函数中继续

// 添加一个动态任务，每10秒执行一次
err = dc.AddTaskMeta(dcron.TaskMeta{
    Name:       "dynamic-task-example",
    CronFormat: "*/10 * * * * *",
    Payload:    "来自动态任务的问候",
})
if err != nil {
    log.Fatalf("添加动态任务失败: %v", err)
}
```
动态任务将由 `WithTaskRunFunc` 中设置的处理器执行。

### 5. 启动服务

最后，启动 `Dcron` 服务开始任务调度：

```go
// ... 在 main 函数中继续

log.Println("Dcron 服务即将启动...")
if err := dc.Start(); err != nil {
    log.Fatalf("Dcron 服务启动失败: %v", err)
}

// 阻塞主 goroutine，或者根据您的应用逻辑处理
select {}
```

---

## 🧰 注册中心示例

`dcron` 支持多种注册中心。以下是如何为每种支持的注册中心初始化客户端并创建 `Dcron` 实例的示例。

#### Consul Registry 示例

```go
package main

import (
    "github.com/robfig/cron/v3"
    "github.com/kkangxu/dcron"
    "github.com/hashicorp/consul/api"
    "log"
    "fmt"
)

func main() {
    // 1. 配置 Consul 客户端
    config := api.DefaultConfig()
    config.Address = "localhost:8500" // Consul 服务地址
    client, err := api.NewClient(config)
    if err != nil {
        log.Fatalf("创建 Consul 客户端失败: %v", err)
    }
    
    // 2. 创建 Dcron 实例，使用 Consul Registry
    dc := dcron.NewDcron(dcron.NewConsulRegistry(client), dcron.WithStrategy(dcron.StrategyConsistent), dcron.WithCronOptions(cron.WithSeconds()))
    
    // 3. 添加静态任务示例
    err = dc.AddFunc("consul-static-task", "*/5 * * * * *", func() error {
        log.Println("Consul Registry: 执行静态任务 (consul-static-task) 每5秒一次")
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // 4. 添加动态任务示例 (可选)
    err = dc.AddTaskMeta(dcron.TaskMeta{
        Name:       "consul-dynamic-task",
        CronFormat: "*/10 * * * * *",
        Payload:    "Hello from Consul dynamic task",
    })
    if err != nil {
        log.Printf("添加动态任务 consul-dynamic-task 失败: %v", err) // 非致命错误，可以选择记录日志
    }
    
    // 5. 启动服务
    log.Println("Dcron (Consul) 服务启动中...")
    if err := dc.Start(); err != nil {
        log.Fatal(err)
    }
}
```

#### Redis Registry 示例

```go
package main

import (
    "github.com/robfig/cron/v3"
    // "context" // 如果您的 Redis 操作需要 context
    "github.com/kkangxu/dcron"
    "github.com/redis/go-redis/v9"
    "log"
    "fmt"
)

func main() {
    // 1. 初始化 Redis 客户端
    rdb := redis.NewClient(&redis.Options{
        Addr: "localhost:6379", // Redis 服务地址
    })
    
    // 2. 创建 Dcron 实例，使用 Redis Registry
    dc := dcron.NewDcron(dcron.NewRedisRegistry(rdb), dcron.WithStrategy(dcron.StrategyConsistent), dcron.WithCronOptions(cron.WithSeconds()))
    
    // 3. 添加静态任务示例
    err := dc.AddFunc("redis-static-task", "*/10 * * * * *", func() error {
        log.Println("Redis Registry: 执行静态任务 (redis-static-task) 每10秒一次")
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // 4. 启动服务
    log.Println("Dcron (Redis) 服务启动中...")
    if err := dc.Start(); err != nil {
        log.Fatal(err)
    }
}
```

#### Etcd Registry 示例

```go
package main

import (
    "github.com/robfig/cron/v3"
    "github.com/kkangxu/dcron"
    "go.etcd.io/etcd/client/v3"
    "log"
    "time"
    "fmt"
)

func main() {
    // 1. 初始化 Etcd 客户端
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   []string{"localhost:2379"}, // Etcd 服务地址
        DialTimeout: 5 * time.Second,
    })
    if err != nil {
        log.Fatalf("连接 Etcd 失败: %v", err)
    }
    defer cli.Close() // 确保客户端关闭
    
    // 2. 创建 Dcron 实例，使用 Etcd Registry
    dc := dcron.NewDcron(dcron.NewEtcdRegistry(cli), dcron.WithStrategy(dcron.StrategyConsistent), dcron.WithCronOptions(cron.WithSeconds()))
    
    // 3. 添加静态任务示例
    err = dc.AddFunc("etcd-static-task", "*/3 * * * * *", func() error {
        log.Println("Etcd Registry: 执行静态任务 (etcd-static-task) 每3秒一次")
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // 4. 启动服务
    log.Println("Dcron (Etcd) 服务启动中...")
    if err := dc.Start(); err != nil {
        log.Fatal(err)
    }
}
```

#### ZooKeeper Registry 示例

```go
package main

import (
    "github.com/robfig/cron/v3"
    "github.com/kkangxu/dcron"
    "github.com/go-zookeeper/zk"
    "log"
    "time"
    "fmt"
)

func main() {
    // 1. 连接 ZooKeeper
    conn, _, err := zk.Connect([]string{"127.0.0.1:2181"}, time.Second*5) // ZooKeeper 服务地址
    if err != nil {
        log.Fatalf("连接 ZooKeeper 失败: %v", err)
    }
    // defer conn.Close() // 通常 zk.Conn 由 dcron 内部管理其生命周期，除非您有特殊需求
    
    // 2. 创建 Dcron 实例，使用 ZooKeeper Registry
    dc := dcron.NewDcron(dcron.NewZookeeperRegistry(conn), dcron.WithStrategy(dcron.StrategyConsistent), dcron.WithCronOptions(cron.WithSeconds()))
    
    // 3. 添加静态任务示例
    err = dc.AddFunc("zk-static-task", "*/2 * * * * *", func() error {
        log.Println("ZooKeeper Registry: 执行静态任务 (zk-static-task) 每2秒一次")
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // 4. 启动服务
    log.Println("Dcron (ZooKeeper) 服务启动中...")
    if err := dc.Start(); err != nil {
        log.Fatal(err)
    }
}
```

---

## 🧭 核心概念与机制

### 动态任务删除与同步

当您需要删除一个动态任务时，应调用 `dc.MarkTaskDeleted("task-name")`。
此操作会将任务标记为“已删除”状态，并将此状态同步到注册中心。这样做的好处是：

*   **防止脏数据**：确保所有节点都知道该任务已被删除。
*   **避免重复调度**：其他节点不会再尝试调度已标记为删除的任务。

如果后续需要重新启用同名任务，建议使用 `ForceAddTaskMeta` 或先调用 `CleanupTask` 清理旧标记后再添加。

### 生命周期与节点状态

`dcron` 中的服务节点具有明确的生命周期状态：

*   **`starting`**：节点启动初始化阶段。
*   **`working`**：节点成功注册到注册中心并开始正常工作（监听任务、参与调度等）。
*   **`leaving`**：节点准备关闭或下线，会进行资源清理和状态更新。

节点退出时，其在注册中心的信息会被妥善处理，确保不会影响集群的正常运作。

### 分布式锁机制

`dcron` 的核心设计之一是确保任务在分布式环境中的**幂等性执行**，即同一个任务在同一时间点只被集群中的一个节点执行。\这是通过内置的分布式锁机制实现的，主要依赖于 `Registry` 接口中的 `CanRunTask` 方法：

```go
// CanRunTask 检查指定的任务在给定的执行时间点是否可以被当前节点执行。
// 它内部会尝试获取该任务在该时间点的分布式锁。
// 返回值：(是否可以运行, 错误信息)
CanRunTask(ctx context.Context, taskName string, execTime time.Time) (bool, error)
```

**工作原理简述**：

1.  当一个节点准备执行某个任务时，它会先调用 `CanRunTask`。
2.  `CanRunTask` 会尝试在注册中心为 `taskName` + `execTime` 这个组合创建一个短暂的、唯一的标记（即获取锁）。
3.  如果成功获取锁，表示当前节点可以执行该任务。执行完毕后，通常会释放锁（或锁自动过期）。
4.  如果获取锁失败（通常意味着其他节点已经获取了该任务在该时间点的执行权），则当前节点不会执行该任务。

**为什么这很重要？**

*   **避免重复执行**：在分布式系统中，多个节点可能同时满足任务的触发条件。没有分布式锁，同一个任务可能被多个节点重复执行，导致数据不一致或其他意外行为。
*   **保证数据一致性**：对于需要修改共享资源的任务，确保只有一个执行者至关重要。
*   **适用于多种场景**：
    *   节点频繁加入或离开集群。
    *   对任务执行的唯一性有严格要求的业务。
    *   任务执行时间非常敏感，不允许延迟或因冲突而失败。

`dcron` 将分布式锁的实现细节封装在各个 `Registry` 的具体实现中，用户无需关心底层细节，只需确保选择的注册中心支持可靠的分布式锁即可（`dcron` 提供的所有内置 Registry 均支持）。

---

## ⚙️ 配置与 API 参考

### 可选配置项 (Option)

`dcron` 实例通过一系列 `Option` 函数进行配置。`Option` 是一个函数类型 `func(*dcron)`。

| Option                               | 描述                                                                 | 默认行为/备注                                     |
| ------------------------------------ | -------------------------------------------------------------------- | ------------------------------------------------- |
| `WithStrategy(strategy AssignerStrategy)` | 设置任务分配策略。可选值：                                               | 默认为 `StrategyConsistent` (一致性哈希)            |
|                                      | `StrategyConsistent` (一致性哈希)                                        |                                                   |
|                                      | `StrategyHashSharding` (平均分配)                                      |                                                   |
|                                      | `StrategyHashSlot` (哈希槽)                                            |                                                   |
|                                      | `StrategyRange` (范围分配)                                             |                                                   |
|                                      | `StrategyWeighted` (加权分配)                                          | 需配合节点权重                                      |
|                                      | `StrategyRoundRobin` (轮询)                                            |                                                   |
| `WithAssigner(assigner Assigner)`    | 设置自定义的任务分配器，或使用 `dcron` 内置的分配器实例。                    | 如果设置此项，会覆盖 `WithStrategy` 的效果。          |
| `WithCronOptions(...)`               | 自定义 `github.com/robfig/cron/v3` 的配置，如设置秒级精度 `cron.WithSeconds()` | 默认不带秒级精度。                                  |
| `WithTaskRunFunc(handler TaskRunFunc)` | 设置动态任务的统一执行函数。`TaskRunFunc` 类型为 `func(*TaskMeta) error`。 | **必须设置**，否则动态任务无法执行。                  |
| `WithErrHandler(handler ErrHandler)`   | 设置任务执行发生错误时的处理函数。`ErrHandler` 类型为 `func(*TaskMeta, error)`。 | 默认会将错误打印到日志。                              |
| `WithLogger(log Logger)`             | 设置自定义日志记录器，需实现 `dcron.Logger` 接口。                       | 默认使用内置的基于 `log` 包的简单日志记录器。         |

**示例：**
```go
import "github.com/robfig/cron/v3"

dc := dcron.NewDcron(
    registry,
    dcron.WithStrategy(dcron.StrategyRoundRobin), // 使用轮询策略
    dcron.WithCronOptions(cron.WithParser(cron.NewParser(
        cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow, // 支持可选的秒字段
    ))),
    // ... 其他 options
)
```

### 错误处理

您可以通过 `WithErrHandler` 选项注入自定义的任务执行错误处理函数。这对于集中记录错误、发送告警或执行特定的错误恢复逻辑非常有用。

```go
dc := dcron.NewDcron(
    registry,
    dcron.WithErrHandler(func(task *dcron.TaskMeta, err error) {
        // 使用您项目的日志系统记录错误
        fmt.Errorf("任务 '%s' (Payload: %s) 执行失败: %v", task.Name, task.Payload, err)
        // 此处可以添加告警逻辑，例如发送邮件或 webhook
    }),
    // ... 其他 options
)
```
如果没有通过 `WithErrHandler` 设置自定义处理器，`dcron` 默认会将错误信息打印到其内部日志（如果配置了 `WithLogger`，则使用自定义 logger；否则使用标准 `log` 包）。

### 动态任务统一处理函数

所有动态任务（通过 `AddTaskMeta` 或 `ForceAddTaskMeta` 添加的任务）的实际执行逻辑都由通过 `WithTaskRunFunc` 选项设置的函数来处理。这提供了一个集中的地方来管理动态任务的执行。

```go
dc := dcron.NewDcron(
    registry,
    dcron.WithTaskRunFunc(func(task *dcron.TaskMeta) error {
        fmt.Printf("开始执行动态任务: %s\n", task.Name)
        fmt.Printf("任务 Cron 表达式: %s\n", task.CronFormat)
        fmt.Printf("任务 Payload: %s\n", task.Payload)
        
        // 根据 task.Name 或 task.Payload 执行不同的业务逻辑
        switch task.Name {
        case "send-email-report":
            // 执行发送邮件报告的逻辑
            fmt.Println("正在发送邮件报告...")
        case "cleanup-temp-files":
            // 执行清理临时文件的逻辑
            fmt.Println("正在清理临时文件...")
        default:
            fmt.Printf("未知的动态任务类型: %s\n", task.Name)
        }
        
        // 如果任务执行成功，返回 nil
        // 如果任务执行失败，返回具体的 error
        return nil
    }),
    // ... 其他 options
)
```
**重要**：如果您计划使用动态任务，**必须**通过 `WithTaskRunFunc` 提供一个处理器，否则动态任务将无法被执行。

### 动态任务 API

以下是管理动态任务的主要 API 方法（通过 `dcron` 实例调用）：

*   **`AddTaskMeta(meta TaskMeta) error`**
    添加一个新的动态任务。如果任务已存在或已被标记为删除，则会返回错误。
    ```go
    err := dc.AddTaskMeta(dcron.TaskMeta{
        Name:       "new-dynamic-task",
        CronFormat: "0 * * * *", // 每小时执行一次
        Payload:    "{"report_type":"hourly"}",
    })
    ```

*   **`ForceAddTaskMeta(meta TaskMeta) error`**
    强制添加一个动态任务。如果任务已存在或已被标记为删除，它将覆盖现有任务并清除相关标记。
    ```go
    err := dc.ForceAddTaskMeta(dcron.TaskMeta{
        Name:       "new-dynamic-task", // 可以与上面同名
        CronFormat: "0 */2 * * *", // 每两小时执行一次
        Payload:    "{"report_type":"bi_hourly"}",
    })
    ```

*   **`MarkTaskDeleted(name string) error`**
    将指定的动态任务标记为已删除。任务不会立即从系统中移除，但不会再被调度执行。此状态会同步到注册中心。
    ```go
    err := dc.MarkTaskDeleted("new-dynamic-task")
    ```

### 主要 API 描述

下表列出了 `dcron` 实例提供的主要方法及其描述。

| 方法名                               | 参数                                     | 返回类型     | 描述                                                                                                                               |
| ------------------------------------ | ---------------------------------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `AddTask`                            | `name string, cronExpr string, tasker Tasker` | `error`      | 添加一个静态任务，`Tasker` 是一个包含 `Run() error` 方法的接口。                                                                         |
| `AddFunc`                            | `name string, cronExpr string, cmd func() error` | `error`      | 添加一个静态任务，执行逻辑由传入的函数提供。                                                                                             |
| `AddOneShotTask`                     | `name string, cronExpr string, tasker Tasker` | `error`      | 添加一个一次性静态任务。任务执行一次后会自动移除。                                                                                         |
| `AddOneShotFunc`                     | `name string, cronExpr string, cmd func() error` | `error`      | 添加一个一次性静态任务（函数形式）。任务执行一次后会自动移除。                                                                                       |
| `AddTaskMeta`                        | `meta TaskMeta`                          | `error`      | 添加一个动态任务。任务元数据存储在注册中心。如果任务已存在或已删除，会报错。                                                                       |
| `ForceAddTask`                       | `name string, cronExpr string, tasker Tasker` | `error`      | 强制添加静态任务。忽略任务是否已存在或已被删除的状态，直接覆盖。                                                                                   |
| `ForceAddFunc`                       | `name string, cronExpr string, cmd func() error` | `error`      | 强制添加静态任务（函数形式）。                                                                                                         |
| `ForceAddOneShotTask`                | `name string, cronExpr string, tasker Tasker` | `error`      | 强制添加一次性静态任务。                                                                                                               |
| `ForceAddOneShotFunc`                | `name string, cronExpr string, cmd func() error` | `error`      | 强制添加一次性静态任务（函数形式）。                                                                                                     |
| `ForceAddTaskMeta`                   | `meta TaskMeta`                          | `error`      | 强制添加动态任务。忽略任务是否已存在或已被删除的状态，直接覆盖。                                                                                   |
| `ReuseDeletedTask`                   | `name string`                            | `error`      | 重新使用一个之前被 `MarkTaskDeleted` 标记为已删除的任务。此操作会清除删除标记。                                                                      |
| `MarkTaskDeleted`                    | `name string`                            | `error`      | 将指定的动态任务标记为已删除。此状态会同步到注册中心，任务将不再被调度。要重新添加同名任务，需使用 `ForceAddTaskMeta` 或先 `CleanupTask`。                     |
| `CleanupTask`                        | `ctx context.Context, name string`       | `error`      | **彻底清理**指定任务的所有相关数据，包括其元数据、已删除标记以及在注册中心的其他痕迹。如果服务重启后通过 `Add` 系列 API 重新添加，任务会像新任务一样加入。 |
| `Start`                              |                                          | `error`      | 启动 `dcron` 服务，开始任务调度和节点注册。                                                                                              |
| `Stop`                               |                                          | `error`      | 停止 `dcron` 服务，会注销节点、停止调度器并释放资源。                                                                                              |
| `GetNodeID`                          |                                          | `string`     | 获取当前 `dcron` 服务实例的节点 ID。                                                                                                   |
| `GetAllTasks`                        |                                          | `[]string`   | 获取当前节点已知的所有任务名称列表（包括静态和动态）。                                                                                             |
| `GetMyselfRunningTasks`              |                                          | `[]string`   | 获取当前节点正在运行（即已分配给当前节点且处于调度周期内）的任务名称列表。                                                                               |
| `ForceCleanupAllTasks`               | `ctx context.Context`                    | `error`      | **!!! 极度危险操作 !!!** 强制清理注册中心内**所有**任务的元数据和已删除任务标记。**仅用于测试环境，且必须确保只有一个节点执行此操作，否则可能导致数据混乱。** |

### TaskMeta 结构体

`TaskMeta` 用于定义动态任务的元数据。

```go
type TaskMeta struct {
    Name                   string `json:"name"`                                // 任务的唯一名称
    CronFormat             string `json:"cron_format"`                         // Cron 表达式 (例如 "*/5 * * * * *")
    OneShot                bool   `json:"one_shot,omitempty"`                  // 标记是否为一次性任务。如果为 true，任务执行一次后其行为取决于下面两个字段。
    ExecutedAndMarkDeleted bool   `json:"executed_and_mark_deleted,omitempty"` // 如果 OneShot 为 true，此字段为 true 时，任务执行一次后会被标记为已删除。
    ExecutedAndCleanup     bool   `json:"executed_and_cleanup,omitempty"`      // 如果 OneShot 为 true，此字段为 true 时，任务执行一次后会被彻底清理 (调用 CleanupTask)。此选项优先于 ExecutedAndMarkDeleted。
    Payload                string `json:"payload,omitempty"`                   // 传递给任务执行函数的自定义数据 (通常为 JSON 字符串)。
}
```

### Node 结构体

`Node` 代表分布式集群中的一个服务节点。

```go
type Node struct {
    ID         string     `json:"id"`          // 节点的唯一 ID (通常自动生成)
    IP         string     `json:"ip"`          // 节点的 IP 地址
    Hostname   string     `json:"hostname"`    // 节点的主机名
    LastAlive  time.Time  `json:"last_alive"`  // 节点最后一次心跳时间，用于健康检查
    CreateTime time.Time  `json:"create_time"` // 节点的注册创建时间
    Status     NodeStatus `json:"status"`      // 节点当前状态: "starting", "working", 或 "leaving"
}

// NodeStatus 定义节点状态的类型
type NodeStatus string

const (
    NodeStatusStarting NodeStatus = "starting"
    NodeStatusWorking  NodeStatus = "working"
    NodeStatusLeaving  NodeStatus = "leaving"
)
```

### NodeEvent & TaskEvent

`dcron` 允许您通过 `Registry` 接口监听节点变化和任务元数据变化事件。

```go
// NodeEvent 表示一个节点变化事件
type NodeEvent struct {
    Type NodeEventType // 事件类型: NodeEventTypePut (新增/更新), NodeEventTypeDelete (删除),  NodeEventTypeChanged (watch node 启动后，发送该事件)
    Node Node          // 相关的节点信息
}

// NodeEventType 定义节点事件的类型
type NodeEventType string

const (
    NodeEventTypePut    NodeEventType = "put"    // 节点新增或信息更新
    NodeEventTypeDelete NodeEventType = "delete" // 节点删除
    NodeEventTypeChanged NodeEventType = "changed" // watch node 启动后，发送该事件
)

// TaskMetaEvent 表示一个任务元数据变化事件
type TaskMetaEvent struct {
    Type TaskEventType // 事件类型: TaskEventTypePut (新增/更新), TaskEventTypeDelete (删除)
    Task TaskMeta      // 相关的任务元数据
}

// TaskEventType 定义任务事件的类型
type TaskEventType string

const (
    TaskEventTypePut    TaskEventType = "put"    // 任务新增或元数据更新
    TaskEventTypeDelete TaskEventType = "delete" // 任务被标记为删除 (MarkTaskDeleted)
)
```

您可以通过 `Registry` 接口的 `WatchNodes(ctx context.Context) (<-chan []NodeEvent, error)` 和 `WatchTaskEvent(ctx context.Context) (<-chan []TaskMetaEvent, error)` 方法来获取这些事件的 channel，从而在您的应用中响应集群状态的变化。

---

## ❓ 常见问题 (FAQ) / 使用建议

1.  **问：为什么调用 `MarkTaskDeleted` 后，不能直接通过 `AddTaskMeta` 再次添加同名任务？**
    答：当您调用 `MarkTaskDeleted("task-name")` 后，系统会在注册中心为该任务名记录一个“已删除”的标记。这样做是为了防止在分布式环境中，由于网络延迟或其他原因，已被删除的任务被其他节点错误地重新调度。这个标记起到了一个“墓碑”的作用。
    如果您确实希望重新使用这个任务名，有以下几种推荐方式：
    *   **使用 `ForceAddTaskMeta`**：这个方法会忽略所有已存在的标记（包括“已删除”标记），强制用新的元数据覆盖或创建任务。
    *   **先调用 `CleanupTask` 再调用 `AddTaskMeta`**：`CleanupTask("task-name")` 会彻底清除该任务名在注册中心的所有痕迹（包括元数据和“已删除”标记）。之后，您就可以像添加一个全新的任务一样使用 `AddTaskMeta`。
    *   **使用 `ReuseDeletedTask`**：这个方法专门用于“复活”一个已被标记为删除的任务，它会清除删除标记，使得任务可以被重新调度（如果其元数据仍在）。

2.  **问：`ForceAddTask` 系列方法和普通的 `Add` 系列方法有什么核心区别？**
    答：核心区别在于对已存在任务或标记的处理方式：
    *   **`Add` 系列方法** (如 `AddTask`, `AddTaskMeta`)：在添加任务前会进行检查。如果同名任务已存在，或者已被标记为删除，这些方法通常会返回错误，以防止意外覆盖或与旧状态冲突。
    *   **`ForceAdd` 系列方法** (如 `ForceAddTask`, `ForceAddTaskMeta`)：正如其名，“强制”执行。它们会忽略任务是否已存在或已被删除的当前状态，直接用新的配置创建或覆盖任务。这通常意味着它们会先清理掉注册中心中与该任务名相关的任何旧记录（包括元数据、已删除标记、可能的执行锁等），然后再写入新任务的信息。这使得它们非常适合需要“强制重置”或“确保最新配置生效”的场景。

3.  **问：动态任务和静态任务应该如何选择和使用？**
    答：
    *   **静态任务**：
        *   **定义方式**：通常在代码中通过 `AddFunc` 或 `AddTask` 直接定义。
        *   **生命周期**：随服务实例启动而加载，随服务实例停止而停止。其定义是硬编码在应用中的。
        *   **管理**：修改静态任务通常需要重新编译和部署代码。
        *   **适用场景**：适合那些逻辑固定、不常变更、且与应用核心功能紧密相关的定时任务，例如：定期的日志切割、系统健康检查、固定的数据同步等。
    *   **动态任务**：
        *   **定义方式**：通过 `AddTaskMeta` API 在运行时添加，其元数据（如 Cron 表达式、Payload）存储在注册中心。
        *   **生命周期**：独立于服务实例的部署。一旦添加到注册中心，集群中的 `dcron` 节点就会发现并根据分配策略进行调度。可以通过 API 进行增、删、改。
        *   **管理**：更加灵活，可以通过外部系统（如管理后台、运维脚本）动态控制任务的启停和配置。
        *   **适用场景**：
            *   需要由运营或用户在后台动态创建和管理的任务（例如，用户自定义的定时提醒、营销活动相关的临时任务）。
            *   需要频繁调整执行时间或参数的任务。
            *   临时性的、一次性的数据处理或运维操作。

4.  **问：如何才能彻底清理一个任务在注册中心的所有痕迹？**
    答：调用 `dcron` 实例的 `CleanupTask(ctx context.Context, taskName string)` 方法。这个方法会：
    *   删除任务的元数据（`TaskMeta`）。
    *   清除该任务的“已删除”标记（如果存在）。
    *   尝试清理与该任务相关的其他潜在数据，如最后执行时间记录、分布式锁标记等（具体取决于注册中心的实现）。
        调用此方法后，`taskName` 在注册中心将不再有任何关联信息，可以被视为一个全新的可用任务名。
        如果您需要一次性清理所有任务（**非常危险，仅限测试！**），可以使用 `ForceCleanupAllTasks(ctx context.Context)`。

5.  **问：为什么 `dcron` 需要“已删除任务同步”机制？**
    答：在分布式系统中，各个节点的状态同步可能存在延迟。如果没有一个明确的“已删除任务”列表并在各节点间同步，可能会发生以下情况：
    *   一个节点删除了任务 A，但其他节点可能由于网络延迟或拉取信息不及时，仍然认为任务 A 存在并尝试调度它。
    *   如果被删除的任务在被重新添加之前，旧的实例仍在某些节点上运行，可能导致冲突或非预期的行为。
        通过在注册中心维护一个“已删除任务列表”（或对任务元数据打上删除标记），并让所有 `dcron` 节点都能感知到这个列表，可以确保：
    *   **全局一致性**：所有节点对哪些任务是“已删除”状态达成共识。
    *   **防止“僵尸”任务**：避免已被删除的任务被错误地重新激活或执行。
    *   **幂等性保障**：与分布式锁机制配合，进一步增强任务调度的准确性。

6.  **问：如何实现 `dcron` 服务的优雅停机和节点下线？**
    答：当您的应用准备关闭时，应该调用 `dcron` 实例的 `Stop()` 方法。这个方法会执行以下操作以确保优雅停机：
    1.  **更新节点状态**：将当前节点在注册中心的状态更新为 `leaving`。这通知其他节点该节点即将下线，它们在进行任务重分配时会考虑到这一点。
    2.  **停止本地调度器**：停止 `cron` 调度器，不再触发新的任务执行。正在执行的任务通常会允许其执行完毕（但这取决于任务本身的实现是否能响应中断信号）。
    3.  **注销节点信息**：从注册中心移除当前节点的注册信息。
    4.  **释放资源**：关闭与注册中心的连接（如果适用），释放其他内部资源。
        通过执行这些步骤，可以最大限度地减少因节点下线导致的任务中断或重复调度问题，保证集群的平稳运行。

7.  **问：`dcron` 支持哪些注册中心？如果我想从 Consul 切换到 Redis，需要修改很多代码吗？**
    答：`dcron` 目前内置支持 Consul、Redis、Etcd 和 ZooKeeper。
    切换注册中心非常简单，因为 `dcron` 的核心逻辑与 `Registry` 接口解耦。您主要需要做的改动是：
    1.  **修改依赖**：确保您的 `go.mod` 中包含了目标注册中心的客户端库（例如，从 `github.com/hashicorp/consul/api` 切换到 `github.com/redis/go-redis/v9`）。
    2.  **修改初始化代码**：在创建 `Dcron` 实例时，传入新的 `Registry` 实现。例如：
        ```go
        // 原 Consul 初始化
        // import "github.com/hashicorp/consul/api"
        // consulClient, _ := api.NewClient(api.DefaultConfig())
        // registry := dcron.NewConsulRegistry(consulClient)

        // 新 Redis 初始化
        import "github.com/redis/go-redis/v9"
        rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
        registry := dcron.NewRedisRegistry(rdb) // 只需要改这里

        dc := dcron.NewDcron(registry, /* ... options ... */)
        ```
    您的任务定义（静态任务的 `AddFunc` 调用、动态任务的 `TaskMeta` 结构）和 `dcron` 的其他配置选项（如 `WithStrategy`, `WithErrHandler` 等）通常无需任何修改。

8.  **问：我如何监听集群中节点或任务的变化，例如实现一个监控仪表盘？**
    答：`Registry` 接口提供了 `Watch` 机制来监听这些变化：
    *   **监听节点变化**：`registry.WatchNodes(ctx context.Context) (<-chan []NodeEvent, error)`
        这个方法返回一个 channel，当集群中的节点加入、离开或状态更新时，您会通过这个 channel 收到 `NodeEvent` 事件的切片。
    *   **监听任务元数据变化**：`registry.WatchTaskEvent(ctx context.Context) (<-chan []TaskMetaEvent, error)`
        类似地，这个方法返回一个 channel，当动态任务被添加、修改或标记为删除时，您会收到 `TaskMetaEvent` 事件的切片。

    您可以在您的应用中启动一个 goroutine 来消费这些 channel 中的事件，并根据事件类型更新您的监控仪表盘、发送通知或执行其他自动化运维操作。
    ```go
    // 示例：监听节点事件
    go func() {
        nodeEventsChan, err := registry.WatchNodes(context.Background()) // 使用合适的 context
        if err != nil {
            log.Printf("无法监听节点事件: %v", err)
            return
        }
        for events := range nodeEventsChan {
            for _, event := range events {
                log.Printf("节点事件: 类型=%s, 节点ID=%s, 状态=%s", event.Type, event.Node.ID, event.Node.Status)
                // 在这里更新您的监控系统
            }
        }
    }()
    ```

9.  **问：如何为所有动态任务自定义统一的执行逻辑？**
    答：通过 `dcron.WithTaskRunFunc(handler func(*dcron.TaskMeta) error)` 选项。您需要提供一个函数，该函数接受一个 `*dcron.TaskMeta` 参数并返回一个 `error`。当任何动态任务到达其执行时间点并被当前节点选中执行时，`dcron` 就会调用您提供的这个 `handler` 函数，并将该任务的 `TaskMeta` 信息传递给它。
    在这个 `handler` 函数内部，您可以：
    *   通过 `task.Name` 来区分不同的动态任务。
    *   通过 `task.Payload` 获取传递给该任务的自定义数据。
    *   执行相应的业务逻辑。
    *   返回 `nil` 表示任务成功执行，返回 `error` 表示任务执行失败（这将触发通过 `WithErrHandler` 设置的错误处理器）。
        这是实现动态任务调度功能的核心扩展点。

10. **问：`dcron` 提供了多种任务分配策略，我应该如何选择？**
    答：选择哪种策略取决于您的具体需求和集群特性：
    *   **`StrategyConsistent` (一致性哈希)**：
        *   **优点**：当节点数量发生变化（增加或减少）时，只会影响到少量任务的重新分配，大多数任务的分配保持稳定。节点间的任务分配相对均匀。
        *   **适用场景**：节点可能频繁变动（例如弹性伸缩的集群）、任务数量较大、希望任务分配尽可能平滑过渡的场景。
    *   **`StrategyHashSharding` (平均分配 / 哈希取模)**：
        *   **优点**：实现简单，计算开销小。在节点数量固定时，任务分配非常均匀。
        *   **缺点**：当节点数量变化时，绝大多数任务的分配都会改变，可能导致较大的任务迁移波动。
        *   **适用场景**：节点数量相对稳定、任务量适中、对节点变动时的任务迁移不敏感的场景。
    *   **`StrategyHashSlot` (哈希槽)**：
        *   **优点**：将任务映射到固定数量的槽位上，再将槽位分配给节点。节点增删时，只需迁移少量槽位及其上的任务，比纯哈希取模更平滑。可以实现更细粒度的负载均衡。
        *   **适用场景**：希望在节点变动时有比哈希取模更好的稳定性，同时保持较好均匀性的场景，类似于 Redis Cluster 的槽位思想。
    *   **`StrategyRange` (范围分配)**：
        *   **优点**：根据任务名称的排序和范围将连续的任务块分配给节点。适合任务名称具有某种有序性或业务关联性的场景。
        *   **缺点**：如果任务名称分布不均，可能导致节点负载不均。
        *   **适用场景**：任务可以按名称逻辑分段处理的场景，例如按字母顺序或数字范围划分任务。
    *   **`StrategyWeighted` (加权分配)**：
        *   **优点**：允许为不同节点设置不同的权重，处理能力强的节点可以分配更多任务。
        *   **缺点**：需要预先评估并配置各节点的权重。
        *   **适用场景**：集群中各节点处理能力不均衡（例如，机器配置不同）的场景。
    *   **`StrategyRoundRobin` (轮询)**：
        *   **优点**：简单公平，依次将任务分配给每个节点。
        *   **缺点**：不考虑任务本身的特性或节点的当前负载。如果任务执行时间差异很大，可能导致某些节点空闲而另一些节点繁忙。
        *   **适用场景**：任务执行时间相近、节点处理能力均等、希望最简单公平分配的场景。

    **选择建议**：
    *   对于大多数通用场景，**`StrategyConsistent` (一致性哈希)** 是一个不错的默认选择，因为它在动态环境和负载均衡方面表现均衡。
    *   如果您的节点非常稳定，可以考虑 **`StrategyHashSharding`** 或 **`StrategyRoundRobin`** 以求简单。
    *   如果对节点变动时的任务迁移有较高要求，且希望比一致性哈希有更细致的控制（或有类似 Redis Cluster 的经验），可以考虑 **`StrategyHashSlot`**。
    *   特定业务场景（如按名分区、节点异构）则对应选择 **`StrategyRange`** 或 **`StrategyWeighted`**。

---

## 📝 总结

`dcron` 项目旨在提供一个强大、灵活且易于扩展的分布式定时任务解决方案。通过支持多种主流注册中心和丰富的任务管理功能，它可以帮助您在微服务架构中高效、可靠地调度和管理定时任务。

🔧 **推荐用途**：

*   微服务架构中各类定时任务的统一调度与管理。
*   需要高可用和负载均衡的定时任务场景。
*   通过后台管理界面或运维脚本动态下发、控制和监控定时任务。
*   实现一次性、周期性或临时的自动化运维和数据处理作业。

我们鼓励您根据项目的实际需求，选择合适的注册中心和配置选项，并充分利用 `dcron` 提供的各项功能。如果您有任何问题或建议，欢迎通过 [GitHub Issues](https://github.com/kkangxu/dcron/issues) (假设项目地址) 与我们交流。

---
---