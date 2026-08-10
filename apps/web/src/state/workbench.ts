import type { AuditReceiptDto, QueryPageDto, QueryResultDto, SchemaDto } from "../api/contracts";

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
    case "nextPageStarted":
      return { ...state, execution: { status: "loading-next-page" }, error: undefined };
    case "executionSucceeded": {
      const previousResult = state.execution.status === "loading-next-page" ? state.result : undefined;
      return {
        ...state,
        execution: { status: "succeeded" },
        result: previousResult
          ? {
              ...action.result,
              rows: [...previousResult.rows, ...action.result.rows],
              returned_rows: previousResult.returned_rows + action.result.returned_rows,
            }
          : action.result,
        audit: action.audit,
        error: undefined,
        nextPageToken: action.page.has_more ? action.page.next_page_token : undefined,
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
