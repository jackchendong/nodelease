---
name: golang-tooling
description: 使用 Go 工具链格式化、分析、验证、诊断和测量 nodelease。
---

# Go 工具链

存在仓库脚本、Make target 或已配置 linter 时优先使用它们。

## 基础检查

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

- `gofmt` 只针对明确修改的 Go 文件，不用仓库级机械格式化制造无关变化。
- 迭代时先运行受影响包和具体测试，完成前再扩大范围。
- 使用 `go list` 确认包和依赖，不根据目录猜测。
- 用 `-run`、`-count` 和 `-timeout` 诊断测试；不要永久增加超时掩盖死锁。
- Redis 集成测试应有明确的启动要求、隔离策略和超时。

## 性能

优化前先用代表性 benchmark 或 profile 测量。根据症状选择 CPU、heap、mutex、block 或 trace，保持正确性和可读性优先。随机分配在大范围下的内存使用和耗尽扫描延迟值得专门测量，但没有证据时不做臆测优化。

## 报告

准确说明实际运行的命令和结果，区分本次改动失败、已有失败、缺少 `go.mod`、真实 Redis 不可用以及未安装工具。无法运行的检查不能写成已通过。
