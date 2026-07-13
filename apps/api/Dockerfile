# P0 API 多阶段构建：构建 → 精简运行镜像
# 目标：不含 shell 的最小攻击面

# ---- 构建阶段 ----
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src
COPY go.mod ./
RUN go mod download 2>/dev/null || true
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/api ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -H -s /sbin/nologin webdb

COPY --from=builder /bin/api /bin/api

EXPOSE 8080

USER webdb

ENTRYPOINT ["/bin/api"]
