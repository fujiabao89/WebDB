# P0-03-followup：查询结果类型规范化

> 状态：Ready｜风险：Medium｜依赖：P0-03｜Owner：fujiabao89｜建议实现者：Claude Code｜独立审查：Codex｜Issue：[#15](https://github.com/fujiabao89/WebDB/issues/15)｜完成期限：P0-06 公开结果 API 前

## 目标与范围

基于 `database/sql.ColumnTypes` 区分 MySQL 文本与二进制列，将文本结果规范化为 `string`，同时保留二进制数据的 `[]byte` 语义，使 PostgreSQL/MySQL 的结果契约在进入公开 API 前保持一致。

不实现 UI 展示、SQL 安全裁决、导出或新的公开 API。若需要改变共享 API 契约，必须先提交契约提案并同步测试与文档。

## 验收标准

| 验收项 | 证据 |
| --- | --- |
| MySQL 文本类型返回 `string`，二进制类型继续返回防御性复制的 `[]byte` | 类型矩阵单元测试与双引擎集成测试 |
| `NULL`、数值、布尔、时间及二进制列不发生回归 | PG/MySQL 类型回归矩阵 |
| `MaxCellBytes`/`MaxPageBytes` 按真实字节数计算，转换不会绕过限制 | 大单元格和大页面边界测试 |
| 分页 token 保存的排序值与规范化结果类型兼容 | 首页面与 NextPage 回归测试 |
| Adapter README 记录两引擎结果类型支持矩阵 | 文档审查 |

## 交接要求

测试固定数据必须为合成数据。不得把文本统一强转为字符串而破坏二进制列，也不得将驱动原始缓冲区直接暴露给调用方。
