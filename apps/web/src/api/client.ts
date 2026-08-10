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
  const request = async <T>(path: string, init: RequestInit): Promise<T> => {
    const response = await fetcher(`${baseUrl}${path}`, init);
    const payload = await json(response);
    if (!response.ok) {
      const envelope = errorEnvelope(payload);
      throw new ApiError(
        envelope?.error.code ?? "internal_error",
        response.status,
        envelope?.error.message ?? "internal_error",
        response.status === 429 ? parseRetryAfter(response.headers.get("Retry-After")) : undefined,
      );
    }
    return payload as T;
  };

  const get = <T>(path: string, signal?: AbortSignal) => request<T>(path, { method: "GET", signal });
  const post = <T>(path: string, body: unknown, signal?: AbortSignal) =>
    request<T>(path, {
      method: "POST",
      signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });

  return {
    async connections(workspaceId, signal) {
      const response = await get<{ data: ConnectionDto[] }>(`/workspaces/${urlPart(workspaceId)}/connections`, signal);
      return response.data;
    },
    async schemas(workspaceId, connectionId, signal) {
      const response = await get<{ data: SchemaDto[] }>(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/schemas`, signal);
      return response.data;
    },
    async tables(workspaceId, connectionId, schema, signal) {
      const query = new URLSearchParams({ schema });
      const response = await get<{ data: TableDto[] }>(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/tables?${query}`, signal);
      return response.data;
    },
    async columns(workspaceId, connectionId, schema, table, signal) {
      const query = new URLSearchParams({ schema, table });
      const response = await get<{ data: ColumnDto[] }>(`/workspaces/${urlPart(workspaceId)}/connections/${urlPart(connectionId)}/columns?${query}`, signal);
      return response.data;
    },
    execute(workspaceId, body, signal) {
      return post<QueryResponseDto>(`/workspaces/${urlPart(workspaceId)}/executions`, body, signal);
    },
    nextPage(workspaceId, token, signal) {
      return post<QueryResponseDto>(`/workspaces/${urlPart(workspaceId)}/query-pages`, { next_page_token: token }, signal);
    },
  };
}
