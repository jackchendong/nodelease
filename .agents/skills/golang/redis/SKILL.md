---
name: golang-redis-lease
description: 实现和审查 nodelease 的 Redis 分配、Lua 脚本、TTL、令牌、Cluster key 和故障语义。
---

# Redis 租约

## 原子性与所有权

- 申请使用 `SET key token NX PX ttl` 或等价原子脚本。
- 续租必须在一个 Lua 脚本中比较 token 并执行 `PEXPIRE`。
- 释放必须在一个 Lua 脚本中比较 token 并执行 `DEL`。
- 禁止用 `GET` 后接独立写操作实现 compare-and-set；命令之间可能发生过期和重新分配。
- token 使用 `crypto/rand`，不得复用或根据 service、ID、时间推导。

## Key 与 Cluster

- key 格式必须稳定、无歧义，并为同一服务使用一致的 Redis Cluster hash tag。
- 校验或编码 namespace 与 service name，特别处理冒号、花括号、空字符串、超长值和可能造成同 key 的输入。
- Lua 脚本只操作通过 `KEYS` 传入的 key，不动态拼接未声明 key；Cluster 模式下多 key 必须在同一 slot。
- 正常分配流程不能依赖 `KEYS` 或全库 `SCAN`。

## 分配算法

- 随机化候选顺序以降低热点，但在有限范围内必须有界并能可靠判断耗尽。
- 处理闭区间宽度计算、整数溢出、单元素范围和巨大范围，不创建无界切片。
- 并发申请的唯一性来自 Redis 原子写，不来自进程内 mutex。
- context 取消、Redis 错误和范围耗尽必须是可区分结果。

## TTL 与故障

- 检查 `time.Duration` 转 Redis 毫秒时的截断、最小正值和溢出。
- 续租间隔必须小于 TTL，并为暂时错误留出重试窗口。
- 客户端超时可能发生在服务端已执行命令之后；不得武断地回滚或删除一个所有权不确定的 key。
- Redis 重启、复制延迟和故障转移可能导致状态回退。文档不得承诺超出单个 Redis 一致性配置所能保证的全局唯一性。

## 验证

fake 可测试候选选择和错误映射；真实 Redis 集成测试用于验证 Lua 返回值、TTL、过期、并发 `SET NX` 和实际客户端行为。Cluster 专属行为需要相应环境或明确记录未验证。
