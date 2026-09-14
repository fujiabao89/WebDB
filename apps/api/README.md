# API 与执行服务

这里是 WebDB 的 Go 模块化单体入口。当前 P0 已实现 `GET /health` 与 `/api/v1` 下 6 条路由（连接列表、Schema 浏览、只读执行/续页与审计确认），以及数据库 Adapter（PostgreSQL/MySQL 双引擎连接池、Schema 拉取、SQL 透传执行与 keyset 分页）。SQL 只读策略由 P0-04 策略层（方言 AST 分类 + Policy 决策引擎）实现，凭证与审计基线由 P0-05（信封加密、轮换、追加式审计与脱敏）实现。按照安全边界，只有 API 服务可以连接目标 PostgreSQL/MySQL，浏览器不得直连数据库或接收数据库凭证。

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

## 查询结果类型规范化（WEB-10）

Adapter 在进入公开 API 前统一 PostgreSQL/MySQL 结果单元格的内部 Go 表示，为后续 HTTP wire encoding 提供稳定输入。

### MySQL

文本/二进制判定使用驱动 `ColumnType.DatabaseTypeName()`（基于 MySQL 协议字段类型 + 字符集 `binaryCollationID=63`，可区分 TEXT/BLOB、CHAR/BINARY、VARCHAR/VARBINARY），**不依据运行时值是否为 `[]byte` 猜测**：

| 分类 | DatabaseTypeName | 结果单元格 |
| --- | --- | --- |
| 文本列 | `CHAR`/`VARCHAR`/`TEXT`/`TINYTEXT`/`MEDIUMTEXT`/`LONGTEXT`/`ENUM`/`SET`/`JSON` | `string` |
| 二进制列 | `BINARY`/`VARBINARY`/`TINYBLOB`/`BLOB`/`MEDIUMBLOB`/`LONGBLOB` | 防御性复制的 `[]byte` |
| 位域/空间/向量 | `BIT`/`GEOMETRY`/`VECTOR` | `[]byte`（位域与 WKB 非文本，保持字节） |
| 其他/未知 | 其余（含 `DECIMAL`/`DATE`/时间等） | 保持驱动原始返回值（如 `DECIMAL`→`[]byte`、`FLOAT`→`float32`、`INT`→`int64`）；无法可靠判断时不转 UTF-8（fail-closed） |

### PostgreSQL

保持 pgx 现有语义，不做机械转换：`TEXT`/`VARCHAR`/`CHAR`→`string`、`BYTEA`→`[]byte`、`JSON/JSONB`→解码 Go 值、`NUMERIC`→`pgtype.Numeric`、`DATE`/`TIMESTAMP`→`time.Time`、`TIME`→`pgtype.Time`、`INT2/4/8`→`int16/int32/int64`、`BOOL`→`bool`、`UUID`→`[16]byte`、`NULL`→`nil`。

大小计算继续按实际字节数（`len(string)` 与 `len([]byte)` 一致），转换不绕过 `MaxCellBytes`/`MaxPageBytes`。类型矩阵覆盖见 `internal/adapter/type_matrix_integration_test.go`（合成种子表 `webdb_type_matrix` 由 `deploy/compose/init` 预置，`demo_reader` 仅只读 SELECT）。
