# P0 Web 前端多阶段构建：npm build → nginx 静态服务
# 不含 API 密钥或数据库凭证

# ---- 构建阶段 ----
FROM node:22-alpine AS builder

WORKDIR /src
COPY package.json package-lock.json* ./
RUN npm ci

COPY . .
RUN npm run build

# ---- 运行阶段 ----
FROM nginx:1.28-alpine

COPY --from=builder /src/dist /usr/share/nginx/html

# 默认 nginx 配置：只服务静态文件，无代理
EXPOSE 80

HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://localhost:80/ || exit 1
