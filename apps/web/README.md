# Web 前端

这里是 WebDB 的 React + TypeScript 前端。当前 P0-01 提供最小健康页，展示 API 的 `/health` 状态；完整工作台将在 P0-06 实现。前端只调用受限 API，绝不保存数据库密码或直接连接目标数据库。

## 运行方式与端口

- 宿主机直接运行 `npm run dev`：Vite 监听 `0.0.0.0:5173`，仅用于前端 UI 开发；默认 API 代理目标 `api:8080` 只在 Compose 网络中可解析。
- Docker Compose：宿主 `127.0.0.1:3000` 映射到容器 5173。
- Vite 和生产 nginx 都将 `/api/*` 去掉 `/api` 前缀后代理到 API。
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
npm run build
```

`npm test` 当前是 P0-01 占位入口，真实组件与端到端测试由 P0-06 补充。
