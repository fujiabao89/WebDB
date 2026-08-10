# Web 前端

这里是 WebDB 的 React + TypeScript 前端。WEB-37（P0-06E）提供最小工作台：已授权连接和 Schema 浏览、Monaco SQL 编辑器、服务端只读执行、单向分页与审计 receipt 展示。前端只调用受限 API，绝不保存数据库密码或直接连接目标数据库。

## 运行方式与端口

- 宿主机直接运行 `npm run dev`：Vite 监听 `0.0.0.0:5173`，仅用于前端 UI 开发；默认 API 代理目标 `api:8080` 只在 Compose 网络中可解析。
- Docker Compose：宿主 `127.0.0.1:3000` 映射到容器 5173。
- Vite 和生产 nginx 保持 WEB-34 的 `/api/v1/*` 前缀后代理到 API；既有 `/api/health` 单独映射为后端 `/health`。
- 生产 Docker target 使用 nginx 提供构建后的静态资源。

```bash
cd apps/web
npm ci
npm run dev
```

宿主机直接运行时可访问 `http://127.0.0.1:5173`，但 API 健康状态会不可达。需要联调 API 时，应按 [Compose 文档](../../deploy/compose/README.md) 启动完整环境并访问 `http://127.0.0.1:3000`。

## 验证

```bash
npm run lint
npm run typecheck
npm test
npm run test:e2e
npm run build
```

`npm test` 运行 Vitest 组件与状态测试；`npm run test:e2e` 运行 Playwright 浏览器测试。浏览器测试仅使用合成 Mock 数据。
