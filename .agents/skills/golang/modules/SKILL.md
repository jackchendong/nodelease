---
name: golang-modules
description: 管理 nodelease 的 Go 模块、go-redis 依赖、构建、版本和发布兼容性。
---

# 模块与版本

- 模块路径固定为 `github.com/jackchendong/nodelease`；创建 `go.mod` 时必须使用该路径。
- 使用 `go.mod` 声明的 Go 版本，不默认采用本机最新特性。
- 使用 Go 命令修改依赖，不手工编辑 `go.sum`。
- Redis 客户端使用 `github.com/redis/go-redis/v9`，除非明确决定迁移，否则不引入第二套客户端。
- 公共库应控制传递依赖；标准库或现有依赖足够时不增加新模块。

常用命令：

```bash
go get github.com/redis/go-redis/v9@<version>
go mod tidy
go mod verify
go list -m all
```

不要执行与任务无关的 `go get -u ./...`，不要为本地便利提交 `replace`。运行 `go mod tidy` 后检查所有增删，避免意外提高最低 Go 版本或移除构建标签下的依赖。

## 发布

- 使用语义化 Git 标签，首个稳定兼容契约从 `v1.0.0` 开始。
- `v2` 及以上模块路径和 import 路径必须增加 `/vN`。
- 打标签前运行全量测试、race、vet、build，确认 README 示例和 Go doc 与实现一致。
- 未经用户明确要求，不创建或推送标签与 Release。
