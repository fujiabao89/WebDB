# P0 Web 前端多阶段构建
#
# 目标：
#   docker build --target dev  -t webdb-web:dev  .   # Vite 开发服务器
#   docker build --target prod -t webdb-web:prod .   # nginx 静态服务
#
# 不含 API 密钥或数据库凭证。

# ============================================================
# dev — Vite 开发服务器（HMR，容器内监听 0.0.0.0:5173）
# ============================================================
FROM node:22-alpine AS dev

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .

EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]

# ============================================================
# 生产构建阶段
# ============================================================
FROM node:22-alpine AS builder

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# ============================================================
# prod — nginx 静态服务（不含 shell 以外工具）
# ============================================================
FROM nginx:1.28-alpine AS prod

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /src/dist /usr/share/nginx/html

EXPOSE 80
