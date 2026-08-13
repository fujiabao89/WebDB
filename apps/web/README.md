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
npm run build
```

`npm test` 继续运行 Vitest 组件与状态测试，未被 Playwright 替换。

### Playwright

`@playwright/test` 精确锁定为 `1.62.1`，仅是 Apache-2.0 的开发/测试依赖。生产 nginx 镜像只复制 Vite 的 `dist` 产物，因此该依赖不进入生产运行时镜像或浏览器 bundle。

真实 Compose 健康冒烟由仓库根目录显式启动五服务环境，Playwright 不通过 `webServer.command` 管理 Compose：

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
cd apps/web
npm ci
npx playwright install chromium
npm run test:e2e:smoke
cd ../..
docker compose -f deploy/compose/docker-compose.yml down
```

默认 `PLAYWRIGHT_BASE_URL` 是 `http://127.0.0.1:3000`，可在受控环境覆盖。`npm run test:e2e` 保留 WEB-37 的隔离合成 Mock/视觉测试，并通过一次性本地 Vite fixture 运行；该脚本显式排除 `@smoke`，不会冒充真实 Compose。`e2e/health.spec.ts` 不使用路由 Mock，由 `npm run test:e2e:smoke` 在真实 Compose 上验证 Web 页面和浏览器同源 `/api/health` 代理。`npm run test:e2e:ui` 提供已启动 Compose 的本地 UI 调试入口。运行结束后的 `playwright-report/` 与 `test-results/` 已由仓库 `.gitignore` 排除。

独立 GitHub Actions workflow 使用 GitHub-hosted Ubuntu、Node 22、单 worker 和一次 CI 重试。失败时仅在递归解包检查通过后保留 report/trace 7 天；检查覆盖普通附件、嵌套 zip/`trace.zip` 及 HTML 的 base64 内嵌资源，并只匹配本次 job 生成的合成敏感值与唯一 canary。

### WEB-39 后续扩展（本任务不实现）

当前 scaffold 只证明 Web 可访问、Web → API 健康代理正常、浏览器未请求数据库端口。WEB-39 仍需补齐：

1. PostgreSQL 连接 → Schema → 只读查询 → 下一页 → audit receipt。
2. MySQL 同一主流程，以及 MySQL 可执行注释拒绝。
3. 连接/Schema DTO 不泄露 host、port、secret ref/version、created_by、workspace_id 等内部字段。
4. 页面、URL、网络、local/session storage 与 trace 不含数据库凭证、KEK、连接串或 token 内部状态。
5. DML、DDL、多语句和危险 SQL 由服务端拒绝，UI 只显示稳定脱敏错误。
6. 分页 token 篡改、重放、过期，以及撤权/策略或 generation 变化后的失效。
7. 审计失败时扣留新结果并清理旧结果。
8. 超时、取消、429、迟到响应与旧结果处理的 UI/HTTP 契约；服务端另以集成/故障注入证据证明资源归还。
9. 隐藏或明确禁用非 P0 入口。
10. 在固定 Chromium/Ubuntu 环境补充少量批准的视觉基线。
