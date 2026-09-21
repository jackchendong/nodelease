---
name: golang
description: 构建、修改、审查、调试、测试和维护 nodelease Go 公共库及其 Go 模块。
metadata:
  language: go
  source: project-local
---

# nodelease Go 工程

处理 Go 代码前先阅读仓库根目录的 `AGENTS.md`、`go.mod`（如果存在）、相关实现、测试以及中英文 README。公共 API、Redis 租约语义和并发生命周期都是本库的核心契约。

## 默认工作流

1. 确认实际调用路径、租约状态、资源所有权和 Redis 命令语义。
2. 做出最小且内聚的改动，完整处理错误、取消、清理和边界情况。
3. 新增或更新面向行为的测试，缺陷修复应包含回归测试。
4. 对修改过的 Go 文件运行 `gofmt`，先运行聚焦测试，再扩大到全模块测试、race 和 vet。
5. 用户可见契约变化时同步更新 `README.md` 与 `README.en.md`。
6. 准确报告实际运行的检查、失败和环境限制。

不要通过弱化校验、吞掉错误、删除断言或跳过测试来掩盖失败。

## 按任务读取指南

| 任务 | 指南 |
|---|---|
| 设计包、公共 API、配置、错误或兼容性 | [设计](./design/SKILL.md) |
| 修改 Redis key、Lua、TTL、分配、续租或释放 | [Redis 租约](./redis/SKILL.md) |
| 添加 goroutine、channel、锁、timer、取消或生命周期 | [并发](./concurrency/SKILL.md) |
| 新增或修改测试、模糊测试、基准或竞态检查 | [测试](./testing/SKILL.md) |
| 创建或维护 go.mod、依赖、版本和构建 | [模块](./modules/SKILL.md) |
| 格式化、分析、诊断、性能测量或最终验证 | [工具链](./tooling/SKILL.md) |

## 通用规则

- 清晰度优先于技巧，不引入与当前需求无关的框架或抽象。
- 对可取消操作显式传递 `context.Context`，不保存 context。
- 使用 `%w` 保留有意义的错误原因，通过 `errors.Is` / `errors.As` 判断。
- 显式构造依赖，避免包级可变状态和隐藏初始化。
- 资源所有权必须清晰；Redis 客户端属于调用方，本库不得关闭它。
- 注释解释契约、不变量和原因，不复述代码语法。
