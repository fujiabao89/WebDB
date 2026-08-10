import { describe, expect, it } from "vitest";
import { initialWorkbenchState, workbenchReducer } from "./workbench";

describe("workbench state machine", () => {
  it("aborts the old connection context and ignores its late schema response", () => {
    const selected = workbenchReducer(initialWorkbenchState, { type: "connectionSelected", connectionId: "pg" });
    const switched = workbenchReducer(selected, { type: "connectionSelected", connectionId: "mysql" });
    const stale = workbenchReducer(switched, {
      type: "schemasLoaded",
      connectionId: "pg",
      generation: selected.connectionGeneration,
      schemas: [{ name: "public", catalog: "app" }],
    });

    expect(stale.selectedConnectionId).toBe("mysql");
    expect(stale.schemas).toEqual([]);
    expect(stale.nextPageToken).toBeUndefined();
  });

  it("prevents duplicate runs and represents a browser abort as local cancellation", () => {
    const running = workbenchReducer(initialWorkbenchState, { type: "runStarted", generation: 1 });
    const duplicate = workbenchReducer(running, { type: "runStarted", generation: 2 });
    const cancelled = workbenchReducer(duplicate, { type: "runCancelledLocally" });

    expect(duplicate.execution.status).toBe("running");
    expect(cancelled.execution.status).toBe("cancelled");
    expect(cancelled.result).toBeUndefined();
  });

  it("discards a pagination token after invalidation and never retains withheld audit-failed data", () => {
    const withPage = workbenchReducer(initialWorkbenchState, {
      type: "executionSucceeded",
      result: { columns: [{ name: "id", wire_type: "int" }], rows: [["1"]], returned_rows: 1, total_returned: 1 },
      page: { page_size: 100, has_more: true, next_page_token: "opaque-token" },
      audit: { state: "recorded", audit_event_id: "audit-1", execution_id: "execution-1", trace_id: "trace-1", outcome: "succeeded" },
    });
    const invalid = workbenchReducer(withPage, { type: "executionFailed", code: "invalid_page_token", message: "invalid_page_token" });
    const auditFailed = workbenchReducer(withPage, { type: "executionFailed", code: "audit_failed", message: "audit_failed" });

    expect(invalid.nextPageToken).toBeUndefined();
    expect(invalid.result).toEqual(withPage.result);
    expect(auditFailed.result).toBeUndefined();
  });

  it.each(["statement_not_allowed", "multiple_statements", "forbidden"])("retains prior audited data as non-successful output after %s", (code) => {
    const succeeded = workbenchReducer(initialWorkbenchState, {
      type: "executionSucceeded",
      result: { columns: [{ name: "id", wire_type: "int" }], rows: [["1"]], returned_rows: 1, total_returned: 1 },
      page: { page_size: 100, has_more: false },
      audit: { state: "recorded", audit_event_id: "audit-1", execution_id: "execution-1", trace_id: "trace-1", outcome: "succeeded" },
    });
    const failed = workbenchReducer(succeeded, { type: "executionFailed", code, message: code });

    expect(failed.execution.status).toBe("failed");
    expect(failed.error?.code).toBe(code);
    expect(failed.result).toEqual(succeeded.result);
    expect(failed.nextPageToken).toBeUndefined();
  });
});
