export type DatabaseEngine = "postgresql" | "mysql";
export type ConnectionEnvironment = "development" | "staging" | "production";

/** Public HTTP DTOs approved for the browser. Never substitute internal Go types here. */
export interface ConnectionDto {
  id: string;
  name: string;
  engine: DatabaseEngine;
  environment: ConnectionEnvironment;
  database: string;
}

export interface SchemaDto {
  name: string;
  catalog: string;
}

export interface TableDto {
  schema: string;
  name: string;
  type: "TABLE" | "VIEW";
}

export interface ColumnDto {
  name: string;
  ordinal: number;
  native_type: string;
  nullable: boolean;
  has_default: boolean;
}

export type WireType = "int" | "decimal" | "boolean" | "float" | "date" | "time" | "timestamp" | "timestamptz" | "json_text" | "binary" | "text" | "uuid";
export type WireCell = string | number | boolean | null;

export interface ResultColumnDto {
  name: string;
  wire_type: WireType;
  data_type?: string;
}

export interface QueryResultDto {
  columns: ResultColumnDto[];
  rows: WireCell[][];
  returned_rows: number;
  total_returned: number;
}

export interface QueryPageDto {
  page_size: number;
  has_more: boolean;
  next_page_token?: string;
}

export interface AuditReceiptDto {
  state: "recorded" | "denied" | "failed" | "cancelled";
  audit_event_id: string;
  execution_id: string;
  trace_id: string;
  outcome: "succeeded" | "denied" | "failed" | "cancelled";
}

export interface ExecuteQueryRequestDto {
  connection_id: string;
  sql: string;
  page_size?: number;
  order_by?: Array<{ column: string; order: "ASC" | "DESC"; nulls_last: boolean }>;
}

export interface NextPageRequestDto {
  next_page_token: string;
}

export interface QueryResponseDto {
  data: QueryResultDto;
  meta: {
    page: QueryPageDto;
    audit: AuditReceiptDto;
  };
}

export interface ErrorEnvelopeDto {
  error: {
    code: string;
    message: string;
  };
}
