# 共享契约

存放前端与 API 共用、受版本控制的 TypeScript 契约。当前导出：

- API 健康响应和标准错误 `HealthResponse`、`ApiError`
- PostgreSQL/MySQL 引擎类型和连接测试请求/结果
- SQL 执行请求、状态、分页结果与列元数据
- 追加式审计事件的基础类型

除健康响应外，连接、执行、分页和审计类型目前是 P0 后续任务使用的占位接口，尚不代表对应 API 已实现。契约变更必须保持兼容；涉及 API、权限或安全语义的变更需要同步任务卡、测试及相关 ADR。

## 验证

```bash
cd packages/contracts
npm ci
npm run typecheck
npm test
```

`npm test` 当前为 P0-01 占位入口，后续任务必须用真实契约测试替换或扩充。
