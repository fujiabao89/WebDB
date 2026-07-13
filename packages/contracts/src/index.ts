// WebDB 共享契约 — P0 基础类型
// 各模块导入此包以共享请求/响应类型、错误码和审计事件定义

/** API 健康检查响应 */
export interface HealthResponse {
  status: "ok" | "degraded";
  version: string;
  time: string;
}

/** 标准 API 错误响应 */
export interface ApiError {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}

/** P0 支持的数据库引擎 */
export type DbEngine = "postgresql" | "mysql";

/** 连接测试请求 */
export interface ConnectionTestRequest {
  engine: DbEngine;
  host: string;
  port: number;
  database: string;
  /** 连接配置引用，不包含明文密码 */
  secretRef: string;
}

/** 连接测试结果 */
export interface ConnectionTestResult {
  success: boolean;
  engine: DbEngine;
  serverVersion?: string;
  error?: ApiError;
  durationMs: number;
}

/** SQL 执行请求 */
export interface ExecuteRequest {
  connectionId: string;
  sql: string;
  /** 游标/keyset 分页 token */
  cursor?: string;
  /** 每页最大行数，默认 500 */
  maxRows?: number;
}

/** SQL 执行结果状态 */
export type ExecutionStatus = "pending" | "running" | "completed" | "failed" | "cancelled";

/** SQL 执行结果 */
export interface ExecuteResult {
  executionId: string;
  status: ExecutionStatus;
  columns?: ColumnInfo[];
  rows?: Record<string, unknown>[];
  rowCount?: number;
  cursor?: string;
  durationMs: number;
  error?: ApiError;
}

/** 列元数据 */
export interface ColumnInfo {
  name: string;
  dataType: string;
  nullable: boolean;
}

/** 审计事件 */
export interface AuditEvent {
  id: string;
  workspaceId: string;
  actorId: string;
  action: string;
  resource: string;
  metadata: Record<string, unknown>;
  occurredAt: string;
}
