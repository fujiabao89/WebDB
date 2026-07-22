# API 与执行服务

这里是 WebDB 的 Go 模块化单体入口。当前 P0-01 仅实现 `GET /health`；数据库 Adapter、SQL 策略、连接池和审计能力将在后续 P0 任务中加入。按照安全边界，未来也只有 API 服务可以连接目标 PostgreSQL/MySQL，浏览器不得直连数据库或接收数据库凭证。

## 工具链

- Go 1.26.0，以 `go.mod` 为唯一版本来源；CI 通过 `go-version-file: apps/api/go.mod` 读取。
- 默认监听 `API_PORT`，未设置时为 `8080`。
- Docker `dev` target 使用 `golang:1.26-bookworm`，保留 shell；`prod` target 使用 distroless nonroot，不含 shell。

## 本地运行

从仓库根目录在单独终端启动 API；该命令会持续前台运行，停止时按 `Ctrl+C`：

```bash
go -C apps/api run ./cmd/server
```

启动后可访问 `http://127.0.0.1:8080/health`。

## 本地验证

以下命令均从仓库根目录执行，不依赖前台服务器：

```bash
go -C apps/api test ./...
go -C apps/api vet ./...
test -z "$(gofmt -l apps/api)"
go -C apps/api test -tags=integration ./internal/adapter/...
go -C apps/api test -tags=integration ./internal/metadata/...
```

Docker 镜像验证同样从仓库根目录执行：

```bash
docker build --target dev -t webdb-api:dev apps/api
docker build --target prod -t webdb-api:prod apps/api
```
