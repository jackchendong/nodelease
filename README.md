# nodelease

[English](README.en.md) | 简体中文

`nodelease` 是一个基于 Redis 的 Go `worker_id` 租约库。使用者只需提供服务名和 Redis 客户端，即可从指定范围内随机获取一个当前未被占用的 ID。库会自动续租，并在进程退出或停止续租后自动回收 ID。

适用于 Snowflake ID 生成器、分布式任务执行器，以及其他需要为运行实例分配短整数标识的场景。

> 当前仓库处于初始阶段。本文档定义建议的公开 API 和行为约定，后续实现应以此为契约。

仓库地址：[github.com/jackchendong/nodelease](https://github.com/jackchendong/nodelease)

## 特性

- 同一服务的在线实例不会获得重复 ID
- 使用服务名隔离不同应用的 ID 空间
- 支持自定义 ID 范围和随机分配
- 支持自动续租、手动续租和主动释放
- 租约到期后自动回收 ID
- 支持 `context.Context`

## 安装

Go 库不需要发布到单独的包注册平台。代码推送到 Git 仓库后即可直接安装：

```bash
go get github.com/jackchendong/nodelease@latest
```

然后导入：

```go
import "github.com/jackchendong/nodelease"
```

项目的 `go.mod` 模块地址应声明为 `github.com/jackchendong/nodelease`。

私有仓库需要配置 `GOPRIVATE`，并通过 SSH key、credential helper 或访问令牌完成 Git 认证：

```bash
go env -w GOPRIVATE=git.example.com/your-team/*
go get git.example.com/your-team/nodelease@latest
```

本地开发可以在调用方的 `go.mod` 中临时替换模块路径：

```go
replace github.com/jackchendong/nodelease => ../nodelease
```

## 快速开始

下面以 [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis) 为例：

```go
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/jackchendong/nodelease"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	lease, err := nodelease.New(nodelease.Config{
		ServiceName:   "order-service",
		Redis:         rdb,
		MinWorkerID:   0,
		MaxWorkerID:   1023,
		TTL:           30 * time.Second,
		RenewInterval: 10 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}

	workerID, err := lease.Acquire(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("worker_id: %d", workerID)

	select {
	case <-ctx.Done():
	case err := <-lease.Lost():
		if !errors.Is(err, context.Canceled) {
			log.Printf("worker_id lease lost: %v", err)
		}
	}

	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lease.Release(releaseCtx); err != nil {
		log.Printf("release lease: %v", err)
	}
}
```

## API

### `New(config Config) (*Lease, error)`

创建租约并校验配置。`New` 不访问 Redis；调用 `Acquire` 后才申请 ID。

```go
type Config struct {
	ServiceName   string                // 必填；服务级 ID 空间
	Redis         redis.UniversalClient // 必填；已创建的 go-redis 客户端
	MinWorkerID   int64                 // 默认 0
	MaxWorkerID   int64                 // 默认 1023，范围包含两端
	TTL           time.Duration         // 默认 30 秒
	RenewInterval time.Duration         // 默认 TTL / 3
	DisableRenew  bool                  // 默认 false，即自动续租
	Namespace     string                // 默认 "nodelease"
}
```

配置要求：`ServiceName` 和 `Redis` 不能为空；`0 <= MinWorkerID <= MaxWorkerID`；`TTL > 0`；启用自动续租时 `0 < RenewInterval < TTL`。

### `(*Lease).Acquire(ctx) (int64, error)`

随机申请一个可用 ID。成功后启动自动续租；重复调用返回当前持有的 ID。范围已满时返回 `ErrNoWorkerIDAvailable`。分配必须在 Redis 中原子执行。

### `(*Lease).Renew(ctx) error`

手动续租。续租时校验随机租约令牌，只有当前持有者才能延长 TTL。租约已过期或被替换时返回 `ErrLeaseLost`。

### `(*Lease).Release(ctx) error`

停止自动续租并释放 ID。释放时校验令牌，避免旧实例误删新持有者的租约。该方法具备幂等性。

### `(*Lease).WorkerID() (int64, bool)`

返回当前 ID 和是否持有租约。

### `(*Lease).Lost() <-chan error`

返回租约丢失通知通道。续租确认租约过期、所有权变化，或无法在 TTL 到期前确认续租成功时，通过该通道报告 `ErrLeaseLost`。调用方收到通知后必须停止使用旧 ID。

## 范围与隔离

范围包含两端，例如 `1` 到 `4` 可能返回 `1`、`2`、`3` 或 `4`。相同 `ServiceName` 的实例应使用相同范围。不同服务拥有独立空间，可以安全复用数字：

```text
order-service   -> worker_id 7
payment-service -> worker_id 7
```

## 租约语义

每次成功分配都会生成不可预测的令牌：

```text
申请 ID -> 持有租约 -> 定期续租 -> 主动释放
                         |
                         +-> 进程退出或断网 -> TTL 到期 -> ID 可再次分配
```

自动续租不能消除长时间 GC 暂停、网络分区或 Redis 故障的风险。收到 `ErrLeaseLost` 后，应停止使用旧 `worker_id`，并根据业务策略退出进程或重新申请。

## Redis 键设计

建议每个租约使用一个 key：

```text
{namespace}:{{serviceName}}:worker:{workerId}
nodelease:{order-service}:worker:37
```

花括号是 Redis Cluster 哈希标签。key 的值是随机令牌，TTL 是剩余租约时间。

- 申请：`SET key token NX PX ttl`
- 续租：Lua 脚本校验 token 后执行 `PEXPIRE`
- 释放：Lua 脚本校验 token 后执行 `DEL`

续租和释放必须原子执行，不能使用 `GET` 后再单独执行写命令。

## 错误处理

库应提供可由 `errors.Is` 判断的哨兵错误：

- `ErrInvalidConfig`：配置不合法
- `ErrNoWorkerIDAvailable`：范围内所有 ID 已被占用
- `ErrLeaseLost`：租约已失效或所有权发生变化
- Redis 错误保留原始原因，便于 `errors.Is` / `errors.As` 判断

## 发布版本

不需要上传到包注册平台，只需创建符合语义化版本的 Git 标签：

```bash
git tag v0.1.0
git push origin v0.1.0
go get github.com/jackchendong/nodelease@v0.1.0
```

稳定 API 通常从 `v1.0.0` 开始。`v2` 及以上需要在模块路径末尾添加主版本，例如 `github.com/jackchendong/nodelease/v2`。

## 运维建议

- 不同环境使用不同 `Namespace`。
- `TTL` 应明显大于预期网络抖动、GC 和调度暂停时间。
- 续租间隔建议不超过 `TTL / 3`。
- 监控 ID 耗尽、续租失败和租约丢失事件。
- Redis 数据丢失或故障转移可能导致租约状态回退；严格唯一性场景需同时评估 Redis 持久化和高可用配置。

## License

待定。
