import type {
  ColumnDto,
  ConnectionDto,
  ErrorEnvelopeDto,
  ExecuteQueryRequestDto,
  QueryResponseDto,
  SchemaDto,
  TableDto,
} from "./contracts";

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly retryAfterSeconds?: number;

  constructor(code: string, status: number, message: string, retryAfterSeconds?: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export interface WebDbApi {
  connections(workspaceId: string, signal?: AbortSignal): Promise<ConnectionDto[]>;
  schemas(workspaceId: string, connectionId: string, signal?: AbortSignal): Promise<SchemaDto[]>;
  tables(workspaceId: string, connectionId: string, schema: string, signal?: AbortSignal): Promise<TableDto[]>;
  columns(workspaceId: string, connectionId: string, schema: string, table: string, signal?: AbortSignal): Promise<ColumnDto[]>;
  execute(workspaceId: string, request: ExecuteQueryRequestDto, signal?: AbortSignal): Promise<QueryResponseDto>;
  nextPage(workspaceId: string, token: string, signal?: AbortSignal): Promise<QueryResponseDto>;
}

export interface ApiClientOptions {
  baseUrl: string;
  fetcher?: typeof fetch;
}

type JsonObject = Record<string, unknown>;
type DataArrayEnvelope<T> = { data: T[] };

function isRecord(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function urlPart(value: string): string {
  return encodeURIComponent(value);
}

function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined;
  const seconds = Number.parseInt(value, 10);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}

function errorEnvelope(value: unknown): ErrorEnvelopeDto | undefined {
  if (!isRecord(value) || !isRecord(value.error) || typeof value.error.code !== "string" || typeof value.error.message !== "string") return undefined;
  return { error: { code: value.error.code, message: value.error.message } };
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

function isConnection(value: unknown): value is ConnectionDto {
  return isRecord(value)
    && typeof value.id === "string"
    && typeof value.name === "string"
    && (value.engine === "postgresql" || value.engine === "mysql")
    && (value.environment === "development" || value.environment === "staging" || value.environment === "production")
    && typeof value.database === "string";
}

function isSchema(value: unknown): value is SchemaDto {
  return isRecord(value) && typeof value.name === "string" && typeof value.catalog === "string";
}

function isTable(value: unknown): value is TableDto {
  return isRecord(value)
    && typeof value.schema === "string"
    && typeof value.name === "string"
    && (value.type === "TABLE" || value.type === "VIEW");
}

function isColumn(value: unknown): value is ColumnDto {
  return isRecord(value)
    && typeof value.name === "string"
    && isNonNegativeInteger(value.ordinal)
    && typeof value.native_type === "string"
    && typeof value.nullable === "boolean"
    && typeof value.has_default === "boolean";
}

function dataArrayEnvelope<T>(isItem: (value: unknown) => value is T): (value: unknown) => value is DataArrayEnvelope<T> {
  return (value: unknown): value is DataArrayEnvelope<T> => isRecord(value) && Array.isArray(value.data) && value.data.every(isItem);
}

function isResultColumn(value: unknown): boolean {
  return isRecord(value)
    && typeof value.name === "string"
    && ["int", "decimal", "boolean", "float", "date", "time", "timestamp", "timestamptz", "json_text", "binary", "text", "uuid"].includes(value.wire_type as string)
    && (value.data_type === undefined || typeof value.data_type === "string");
}

function isWireCell(value: unknown): boolean {
  return value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value));
}

function isQueryPage(value: unknown): boolean {
  if (!isRecord(value) || !isNonNegativeInteger(value.page_size) || value.page_size === 0 || typeof value.has_more !== "boolean") return false;
  return value.has_more ? typeof value.next_page_token === "string" && value.next_page_token.length > 0 : value.next_page_token === undefined;
}

function isAuditReceipt(value: unknown): boolean {
  return isRecord(value)
    && ["recorded", "denied", "failed", "cancelled"].includes(value.state as string)
    && typeof value.audit_event_id === "string"
    && typeof value.execution_id === "string"
    && typeof value.trace_id === "string"
    && ["succeeded", "denied", "failed", "cancelled"].includes(value.outcome as string);
}

function queryResponse(value: unknown): value is QueryResponseDto {
  return isRecord(value)
    && isRecord(value.data)
    && Array.isArray(value.data.columns)
    && value.data.columns.every(isResultColumn)
    && Array.isArray(value.data.rows)
    && value.data.rows.every((row) => Array.isArray(row) && row.every(isWireCell))
    && isNonNegativeInteger(value.data.returned_rows)
    && isNonNegativeInteger(value.data.total_returned)
    && value.data.total_returned >= value.data.returned_rows
    && isRecord(value.meta)
    && isQueryPage(value.meta.page)
    && isAuditReceipt(value.meta.audit);
}

async function json(response: Response): Promise<unknown> {
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

/**
 * A deliberately small client boundary. It neither logs requests/responses nor
 * persists opaque pagination handles; callers own their in-memory lifecycle.
 */
export function createApiClient({ baseUrl, fetcher = fetch }: ApiClientOptions): WebDbApi {
  const request = async <T>(path: string, init: RequestInit, isExpectedPayload: (value: unknown) => value is T): Promise<T> => {
    const response = await fetcher(`${baseUrl}${path}`, init);
    const payload = await json(response);
    if (!response.ok) {
      const envelope = errorEnvelope(payload);
      throw new ApiError(
        envelope?.error.code ?? "internal_error",
        response.status,
        envelope?.error.message ?? "internal_error",
        parseRetryAfter(response.headers.get("Retry-After")),
      );
    }
    if (!isExpectedPayload(payload)) throw new ApiError("internal_error", response.status, "internal_error");
    return payload;
  };

  const get = <T>(path: string, signal: AbortSignal | undefined, isExpectedPayload: (value: unknown) => value is T) =>
    request<T>(path, { method: "GET", signal }, isExpectedPayload);
  const post = <T>(path: string, body: unknown, signal: AbortSignal | undefined, isExpectedPayload: (value: unknown) => value is T) =>
    request<T>(path, {
      method: "POST",
      signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }, isExpectedPayload);

  return {
    async connections(workspaceId, signal) {
      const response = await get(`/workspaces/${urlPart(workspaceId)}/connections`, signal, dataArrayEnvelope(isConnection));
      return response.data;
    },
    async schemas(workspaceId, connectionId, signal) {
      const response = await get(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/schemas`, signal, dataArrayEnvelope(isSchema));
      return response.data;
    },
    async tables(workspaceId, connectionId, schema, signal) {
      const query = new URLSearchParams({ schema });
      const response = await get(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/tables?${query}`, signal, dataArrayEnvelope(isTable));
      return response.data;
    },
    async columns(workspaceId, connectionId, schema, table, signal) {
      const query = new URLSearchParams({ schema, table });
      const response = await get(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/columns?${query}`, signal, dataArrayEnvelope(isColumn));
      return response.data;
    },
    execute(workspaceId, body, signal) {
      return post(`/workspaces/${urlPart(workspaceId)}/executions`, body, signal, queryResponse);
    },
    nextPage(workspaceId, token, signal) {
      return post(`/workspaces/${urlPart(workspaceId)}/query-pages`, { next_page_token: token }, signal, queryResponse);
    },
  };
}
