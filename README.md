# nodelease

简体中文 | [English](README.en.md)

`nodelease` 是一个基于 Redis 的 Go `worker_id` 租约库。调用方提供服务名和 Redis 客户端，库会从指定范围内随机开始查找并原子申请一个可用 ID，通过自动续租维持所有权，并在停止续租后由 Redis 自动回收。

适用于 Snowflake ID 生成器、分布式任务执行器，以及其他需要为每个运行实例分配短整数标识的场景。

## 特性

- 同一服务的在线实例不会获得重复的 `worker_id`
- 使用服务名隔离不同应用的 ID 空间
- 支持包含两端的自定义 ID 范围，默认 `[0, 1023]`
- 使用加密随机起点和有界完整遍历，既分散竞争又能可靠判断耗尽
- 支持自动续租、手动续租和主动释放
- 使用随机所有权令牌，旧实例不能续租或删除新持有者的租约
- Redis Cluster 友好的 hash tag key
- 并发安全，支持 `context.Context`

## 环境要求

- Go 1.21 或更高版本
- Redis 6.0 或更高版本
- [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis)

## 安装

直接通过 Go Modules 从 GitHub 安装稳定版本，无需单独的包注册平台：

```bash
go get github.com/jackchendong/nodelease@v0.1.0
```

如需跟随最新提交，可使用：

```bash
go get github.com/jackchendong/nodelease@latest
```

然后导入：

```go
import "github.com/jackchendong/nodelease"
```

## 快速开始

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

	"github.com/jackchendong/nodelease"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	lease, err := nodelease.New(nodelease.Config{
		ServiceName: "order-service",
		Redis:       rdb,
		Range: &nodelease.IDRange{
			Min: 0,
			Max: 1023,
		},
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

	// 将 workerID 传给 Snowflake 或其他需要实例标识的组件。

	select {
	case <-ctx.Done():
	case err := <-lease.Lost():
		if errors.Is(err, nodelease.ErrLeaseLost) {
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

`nodelease` 不会关闭传入的 Redis 客户端；客户端生命周期由调用方管理。

## API

### `New(config Config) (*Lease, error)`

创建单次生命周期的租约对象并校验配置。`New` 不访问 Redis，调用 `Acquire` 时才申请 ID。

```go
type IDRange struct {
	Min int64
	Max int64
}

type Config struct {
	ServiceName   string
	Redis         redis.UniversalClient
	Range         *IDRange
	TTL           time.Duration
	RenewInterval time.Duration
	DisableRenew  bool
	Namespace     string
}
```

| 字段 | 必填 | 默认值 | 说明 |
|---|---:|---|---|
| `ServiceName` | 是 | — | 服务级 ID 空间，最长 256 字节，不能包含控制字符 |
| `Redis` | 是 | — | 已初始化的 go-redis 客户端 |
| `Range` | 否 | `[0, 1023]` | 包含两端，最多 65,536 个 ID；使用 `&IDRange{Min: 0, Max: 0}` 表示仅 ID 0 |
| `TTL` | 否 | `30s` | Redis 租约有效期，最小 `1ms` |
| `RenewInterval` | 否 | `TTL / 3` | 必须大于 0 且小于 TTL |
| `DisableRenew` | 否 | `false` | 关闭 Redis 自动续租，但仍会在本地截止时间报告租约到期 |
| `Namespace` | 否 | `nodelease` | Redis key 前缀，支持字母、数字、`-_.:`，最长 128 字节 |

配置不合法时返回可由 `errors.Is(err, nodelease.ErrInvalidConfig)` 判断的错误。

### `(*Lease).Acquire(ctx context.Context) (int64, error)`

从范围内的加密随机位置开始逐个尝试，通过 Redis `SET NX PX` 原子申请第一个可用 ID。

- 同一 `Lease` 在持有期间重复或并发调用会返回相同 ID。
- 范围已满时返回 `ErrNoWorkerIDAvailable`。
- Redis 或 context 错误保留原始原因。
- 申请成功后，`Acquire` 的 context 取消不会终止租约；租约由 `Release`、丢失或 TTL 控制。

### `(*Lease).Renew(ctx context.Context) error`

手动将当前租约延长一个完整 TTL。Lua 脚本会先校验所有权令牌。key 不存在或令牌不匹配时返回 `ErrLeaseLost`。

### `(*Lease).Release(ctx context.Context) error`

停止生命周期 goroutine，并通过令牌校验 Lua 脚本删除 Redis key。该方法可重复调用；旧租约不会删除新持有者的 key。

如果释放命令发生网络错误，本地租约仍会终止，Redis key 最迟在 TTL 到期后回收。

### `(*Lease).WorkerID() (int64, bool)`

返回当前 ID 和本地是否仍认为租约有效。`ok == false` 后不得继续使用返回过的 ID。

### `(*Lease).Lost() <-chan error`

返回租约丢失通知通道。令牌变化、本地 TTL 到期，或自动续租无法在截止时间前确认成功时，通道最多收到一次包含 `ErrLeaseLost` 的错误。正常 `Release` 不会关闭该通道。

一个 `Lease` 成功获取后是单次使用的：释放或丢失后，`Acquire` 返回 `ErrLeaseClosed`。需要新 ID 时请调用 `New` 创建新对象。

## 错误

```go
workerID, err := lease.Acquire(ctx)
switch {
case err == nil:
	log.Printf("worker_id: %d", workerID)
case errors.Is(err, nodelease.ErrNoWorkerIDAvailable):
	log.Print("worker_id range is exhausted")
case errors.Is(err, context.Canceled):
	log.Print("acquisition canceled")
default:
	log.Printf("acquire worker_id: %v", err)
}
```

- `ErrInvalidConfig`：配置不合法
- `ErrNoWorkerIDAvailable`：范围内所有 ID 已被占用
- `ErrLeaseLost`：租约已过期、所有权变化或无法及时确认续租
- `ErrLeaseClosed`：单次租约对象已经释放或丢失

## Redis key 与原子性

key 格式为：

```text
{namespace}:{base64url(serviceName)}:worker:{workerID}
```

`order-service` 的 ID `37` 默认对应：

```text
nodelease:{b3JkZXItc2VydmljZQ}:worker:37
```

花括号形成 Redis Cluster hash tag。同一服务的 key 会进入同一 slot，服务名使用无填充 Base64 URL 编码避免分隔符冲突。

- 申请：`SET key token NX PX ttl`
- 续租：Lua 原子比较 token 后执行 `PEXPIRE`
- 释放：Lua 原子比较 token 后执行 `DEL`

库不会使用 `KEYS` 或 `SCAN` 查找可用 ID。

## 保证与限制

- 唯一性建立在目标 Redis 对成功写入的保持能力上。
- 网络超时可能导致申请结果不确定；这类未续租 key 会在 TTL 后自动回收。
- Redis 数据丢失、异步复制或故障转移可能使租约状态回退。严格唯一性场景需同时配置合适的 Redis 持久化与高可用策略。
- 长时间 GC/调度暂停和网络分区可能导致租约丢失。调用方必须监听 `Lost()`，并在丢失后停止使用旧 ID。
- 相同 `ServiceName` 的所有实例应使用一致的范围、namespace 和租约策略。

## 开发与测试

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

## License

[MIT License](LICENSE)。允许任何人免费使用、复制、修改、合并、发布、分发、再许可和销售本软件，但需保留版权与许可声明。
