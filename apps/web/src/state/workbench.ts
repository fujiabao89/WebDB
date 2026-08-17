import type { AuditReceiptDto, QueryPageDto, QueryResultDto, SchemaDto } from "../api/contracts";

export const MAX_RESULT_ROWS = 500;

export type ExecutionStatus = "idle" | "running" | "cancelled" | "succeeded" | "failed" | "loading-next-page";

export interface WorkbenchError {
  code: string;
  message: string;
  retryAfterSeconds?: number;
}

export interface WorkbenchState {
  selectedConnectionId?: string;
  connectionGeneration: number;
  schemas: SchemaDto[];
  schemaStatus: "idle" | "loading" | "ready" | "empty" | "error";
  result?: QueryResultDto;
  audit?: AuditReceiptDto;
  nextPageToken?: string;
  error?: WorkbenchError;
  execution: { status: ExecutionStatus };
}

export const initialWorkbenchState: WorkbenchState = {
  connectionGeneration: 0,
  schemas: [],
  schemaStatus: "idle",
  execution: { status: "idle" },
};

export type WorkbenchAction =
  | { type: "connectionSelected"; connectionId: string }
  | { type: "schemasLoading"; connectionId: string; generation: number }
  | { type: "schemasLoaded"; connectionId: string; generation: number; schemas: SchemaDto[] }
  | { type: "schemasFailed"; connectionId: string; generation: number; error: WorkbenchError }
  | { type: "runStarted"; generation: number }
  | { type: "runCancelledLocally" }
  | { type: "queryInputChanged" }
  | { type: "executionSucceeded"; result: QueryResultDto; page: QueryPageDto; audit: AuditReceiptDto }
  | { type: "nextPageStarted" }
  | { type: "executionFailed"; code: string; message: string; retryAfterSeconds?: number };

export function workbenchReducer(state: WorkbenchState, action: WorkbenchAction): WorkbenchState {
  switch (action.type) {
    case "connectionSelected": {
      if (action.connectionId === state.selectedConnectionId) return state;
      return {
        ...initialWorkbenchState,
        selectedConnectionId: action.connectionId,
        connectionGeneration: state.connectionGeneration + 1,
      };
    }
    case "schemasLoading":
      if (state.selectedConnectionId !== action.connectionId || state.connectionGeneration !== action.generation) return state;
      return { ...state, schemaStatus: "loading", schemas: [], error: undefined };
    case "schemasLoaded":
      if (state.selectedConnectionId !== action.connectionId || state.connectionGeneration !== action.generation) return state;
      return { ...state, schemas: action.schemas, schemaStatus: action.schemas.length === 0 ? "empty" : "ready" };
    case "schemasFailed":
      if (state.selectedConnectionId !== action.connectionId || state.connectionGeneration !== action.generation) return state;
      return { ...state, schemas: [], schemaStatus: "error", error: action.error };
    case "runStarted":
      if (state.execution.status === "running" || state.execution.status === "loading-next-page") return state;
      return { ...state, execution: { status: "running" }, error: undefined, result: undefined, audit: undefined, nextPageToken: undefined };
    case "runCancelledLocally":
      return { ...state, execution: { status: "cancelled" }, error: undefined, result: undefined, audit: undefined, nextPageToken: undefined };
    case "queryInputChanged":
      // 用户修改 SQL/排序列/排序方向：旧结果与分页 token 不再对应当前输入，清空以阻止"加载下一页"使用过期 token。
      return { ...state, execution: { status: "idle" }, error: undefined, result: undefined, audit: undefined, nextPageToken: undefined };
    case "nextPageStarted":
      return { ...state, execution: { status: "loading-next-page" }, error: undefined };
    case "executionSucceeded": {
      const previousResult = state.execution.status === "loading-next-page" ? state.result : undefined;
      const retainedRows = previousResult?.rows.slice(0, MAX_RESULT_ROWS);
      const rows = retainedRows && [
        ...retainedRows,
        ...action.result.rows.slice(0, Math.max(0, MAX_RESULT_ROWS - retainedRows.length)),
      ];
      const reachedResultLimit = rows !== undefined && rows.length >= MAX_RESULT_ROWS;
      return {
        ...state,
        execution: { status: "succeeded" },
        result: rows
          ? {
              ...action.result,
              rows,
              returned_rows: rows.length,
              total_returned: rows.length,
            }
          : action.result,
        audit: action.audit,
        error: undefined,
        nextPageToken: action.page.has_more && !reachedResultLimit ? action.page.next_page_token : undefined,
      };
    }
    case "executionFailed": {
      const withholdResults = action.code === "audit_failed";
      return {
        ...state,
        execution: { status: "failed" },
        error: { code: action.code, message: action.message, retryAfterSeconds: action.retryAfterSeconds },
        nextPageToken: undefined,
        result: withholdResults ? undefined : state.result,
        audit: withholdResults ? undefined : state.audit,
      };
    }
  }
}
