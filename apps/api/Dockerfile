# P0 API 多阶段构建
#
# 目标：
#   docker build --target dev  -t webdb-api:dev  .   # 开发/Compose
#   docker build --target prod -t webdb-api:prod .   # 生产
#
# 说明：
#   - dev  使用 golang:1.26-bookworm，包含 shell 与调试工具
#   - prod 使用 distroless nonroot，最小攻击面，不含 shell

# ============================================================
# 构建阶段（dev 与 prod 共用）
# ============================================================
FROM golang:1.26-bookworm AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download 2>/dev/null || true
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/api ./cmd/server

# ============================================================
# dev — Compose 开发环境（含 shell，支持 docker healthcheck）
# ============================================================
FROM golang:1.26-bookworm AS dev

COPY --from=builder /bin/api /bin/api

EXPOSE 8080
ENTRYPOINT ["/bin/api"]

# ============================================================
# prod — 生产环境（distroless nonroot，不含 shell）
# ============================================================
FROM gcr.io/distroless/static-debian12:nonroot AS prod

COPY --from=builder /bin/api /bin/api

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/bin/api"]
