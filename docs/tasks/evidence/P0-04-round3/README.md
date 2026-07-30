# P0-04 Round 3 可复核证据

本目录持久化 WEB-13 Round 3 Spike 的最小可复核材料。内容仅来自仓库外隔离
环境，不包含构建二进制、Go module cache、真实凭证、数据库数据或生产日志。

## 目录

- `harness/`：实际执行的 Go harness、测试、fuzz target、固定 `go.mod/go.sum`
  与 fuzz seed。
- `raw/`：测试、fuzz、构建、`go vet`、module graph 与许可证复核的原始输出。
- `verify_licenses.go`：使用本机 `GOMODCACHE` 重新计算 75 个外部模块的许可证
  SHA256，并核对 module graph、TSV 状态与禁用许可证计数。
- `archive-sha256.txt`：除自身外，本目录所有持久化文件的 SHA256。

`raw/module-list.json` 和 `raw/round3-license-recheck.tsv` 中仅对本机绝对路径做了
机械脱敏：

- harness 根目录替换为 `<HARNESS_DIR>`；
- Go module cache 根目录替换为 `<GOMODCACHE>`。

测试输出、模块版本、许可证类型、许可证哈希和判定结果未修改。报告中的原始文件
SHA256 保留为隔离运行产物的身份记录；本目录脱敏副本使用
`archive-sha256.txt` 单独固定。

## 复核命令

```powershell
Set-Location harness
go mod download all
go test -count=1 -v ./...
go test -run=^$ -fuzz=FuzzECMLexer -fuzztime=30s
go test -run=^$ -fuzz=FuzzMySQLPipeline -fuzztime=30s
go test -run=^$ -fuzz=FuzzPGPipeline -fuzztime=30s
go vet ./...
$env:GOOS='windows'; $env:GOARCH='amd64'; go build ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
Remove-Item Env:GOOS, Env:GOARCH
GOPROXY=off go list -m all

Set-Location ..
go run verify_licenses.go
```

预期：

- `go test`、三个 fuzz、`go vet` 和两个目标平台构建均返回 0；
- module graph 为 76 行（1 个 main module + 75 个外部 module）；
- `verify_licenses.go` 输出 `PASS: 75/75 licenses verified`。

空的 `build-*.log` 与 `go-vet.log` 表示对应命令成功且未产生标准输出；
`raw/exit-codes.txt` 保存全部命令的退出码。
