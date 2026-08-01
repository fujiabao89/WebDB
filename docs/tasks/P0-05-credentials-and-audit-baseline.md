# P0-05：凭证与审计基线

> 状态：Ready｜风险：High｜依赖：P0-02、P0-04、ADR-006、ADR-010、ADR-013｜建议实现者：Claude Code｜独立审查：Codex
>
> **子任务**：
> - [WEB-21](https://linear.app/webdb/issue/WEB-21)：P0-05A 凭证与审计方案、威胁模型及 Owner Gate（当前任务）
> - [WEB-22](https://linear.app/webdb/issue/WEB-22)：P0-05B 凭证信封加密、版本轮换与 Adapter 接入（被 WEB-21 阻塞）
> - [WEB-23](https://linear.app/webdb/issue/WEB-23)：P0-05C 追加式审计、脱敏、故障策略与最终验收（被 WEB-21、WEB-22 阻塞）
>
> **方案文档**：[P0-05-proposal-credentials-and-audit.md](P0-05-proposal-credentials-and-audit.md)
> **威胁模型**：[P0-05-threat-model.md](P0-05-threat-model.md)
> **ADR**：[ADR-017](../adr/ADR-017-p0-credential-envelope-audit-failure.md)（已接受）

## 目标与范围

实现连接凭证信封加密/引用、轮换版本预留、日志脱敏与追加式审计。覆盖连接测试、执行、拒绝、取消和策略相关事件。

不接入企业 KMS、对象存储审计归档、生产数据导出或真实 KEK。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| API、浏览器响应、数据库、日志和审计正文均不含明文密码/KEK | canary 敏感信息扫描（`execution/sensitive_audit_test.go`、`credentials/sensitive_test.go`、`metadata/audit_metadata_test.go`） |
| 每次关键操作记录 actor、工作区、连接、动作、结果摘要、时间和 execution/trace ID | 审计集成测试（E1-E16 事件 + 关联正确性） |
| 审计普通业务路径不能更新/删除；敏感值仅记录脱敏摘要 | DB 拒绝 UPDATE/DELETE/TRUNCATE 集成测试；强类型 metadata fail-closed 校验 |
| 加密/解密和审计写入失败显式失败并告警，不静默降级 | 故障注入测试（AUDIT-01~04 + `$SECURITY_ALERT`） |

## 实施状态（WEB-23，2026-08-01）

- E1-E16 追加式审计已接入连接/凭证/SQL 执行生命周期（`metadata.AuditMetadata` 强类型 + `AppendAudit` fail-closed）。
- execution 审计感知管线（`execution.Pipeline`）实现执行前 fail-closed、执行后 `audit_failed` 且 execution 保持终态。
- `$SECURITY_ALERT` 独立安全告警通道覆盖凭证解密失败、未知 KEK 版本、审计写入失败。
- 单元测试、PostgreSQL metadata/adapter 集成测试、fuzz、vet、Windows/Linux 构建通过；race 由 GitHub Actions 覆盖。

## Owner Gate 状态（WEB-21）

| 决策项 | 状态 |
| --- | --- |
| D1-D15 Owner 决策包 | ✅ 已批准（2026-08-01, fujiabao89） |
| ADR-017 | 已接受 |
| 威胁模型 | 已批准 |
| 测试矩阵 | 已编制（待 WEB-22/WEB-23 执行） |
| WEB-22 阻塞 | 已解除 |
| WEB-23 阻塞 | 待 WEB-22 完成后解除 |

## 升级条件

密钥轮换、保留期、审计字段或日志平台选择发生变化时，先更新 ADR/威胁模型。
