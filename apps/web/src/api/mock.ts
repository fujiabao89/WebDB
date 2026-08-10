import type { WebDbApi } from "./client";
import type { ColumnDto, ConnectionDto, QueryResponseDto, SchemaDto, TableDto } from "./contracts";

/** Synthetic-only fixture transport for tests and local development harnesses. It is never App's default transport. */
export function createMockApi(overrides: Partial<WebDbApi> = {}): WebDbApi {
  const connections: ConnectionDto[] = [
    { id: "synthetic-pg", name: "Synthetic PostgreSQL", engine: "postgresql", environment: "staging", database: "sample_app" },
    { id: "synthetic-mysql", name: "Synthetic MySQL", engine: "mysql", environment: "development", database: "sample_shop" },
  ];
  const schemas: SchemaDto[] = [{ name: "public", catalog: "sample_app" }];
  const tables: TableDto[] = [{ schema: "public", name: "orders", type: "TABLE" }];
  const columns: ColumnDto[] = [{ name: "id", ordinal: 1, native_type: "uuid", nullable: false, has_default: false }];
  const result: QueryResponseDto = {
    data: { columns: [{ name: "id", wire_type: "uuid" }], rows: [["00000000-0000-4000-8000-000000000001"]], returned_rows: 1, total_returned: 1 },
    meta: { page: { page_size: 100, has_more: false }, audit: { state: "recorded", audit_event_id: "audit-synthetic", execution_id: "execution-synthetic", trace_id: "trace-synthetic", outcome: "succeeded" } },
  };
  return {
    connections: async () => connections,
    schemas: async () => schemas,
    tables: async () => tables,
    columns: async () => columns,
    execute: async () => result,
    nextPage: async () => result,
    ...overrides,
  };
}
