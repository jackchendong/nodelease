---
name: golang-testing
description: 编写和审查 nodelease 的单元、集成、并发、模糊、示例和基准测试。
---

# nodelease 测试

## 单元测试

- 测试可观察行为和不变量，不断言内部 goroutine 数量或非契约字段。
- 共享设置适合表驱动测试；显著不同的生命周期场景使用独立测试。
- 用 `errors.Is` / `errors.As` 判断错误分类，不依赖完整错误文本。
- 在最窄边界注入随机源、时钟或 Redis 命令执行器，使边界和失败路径确定可测。
- helper 使用 `t.Helper()`；资源获取后立即通过 `t.Cleanup()` 注册清理。

## 必测场景

- 默认值和每一项非法配置。
- 范围两端、单元素范围、耗尽、服务隔离和并发唯一性。
- 获取成功、Redis 失败、context 取消和结果不确定。
- 正常续租、临时失败后恢复、token 不匹配、TTL 到期和只通知一次。
- 正常释放、重复释放、过期后释放、旧 token 释放和续租/释放竞态。
- ID 范围及 duration 的溢出、截断和最小边界。

## 集成与并发

- fake 不足以证明 Redis 原子性。Lua、TTL、`SET NX PX` 和并发竞争使用真实 Redis 集成测试。
- 每个测试使用唯一 namespace/service，并可靠清理；不得依赖开发者已有数据或测试执行顺序。
- 常规单元测试不得依赖公网、个人凭据或长时间 sleep。
- 并发相关改动运行 `go test -race`，并直接断言退出、取消和无重复 ID；race 通过不等于没有死锁。

## 模糊与基准

对 service/namespace 编码、范围计算和 key 构建可使用模糊测试，断言无冲突、无 panic、资源有界。只有性能结论会影响设计时才增加 benchmark，并把准备工作排除在计时外。
