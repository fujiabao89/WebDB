import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { WebDbApi } from "./api/client";
import type { ConnectionDto } from "./api/contracts";

const sensitiveResponseCanary = "synthetic-secret-ref-must-not-render";

const api: WebDbApi = {
  connections: vi.fn().mockResolvedValue([
    { id: "pg", name: "Synthetic PostgreSQL", engine: "postgresql", environment: "staging", database: "app", secret_ref: sensitiveResponseCanary } as unknown as ConnectionDto,
  ]),
  schemas: vi.fn().mockResolvedValue([{ name: "public", catalog: "app" }]),
  tables: vi.fn().mockResolvedValue([{ schema: "public", name: "users", type: "TABLE" }]),
  columns: vi.fn().mockResolvedValue([{ name: "id", ordinal: 1, native_type: "uuid", nullable: false, has_default: false }]),
  execute: vi.fn().mockResolvedValue({
    data: {
      columns: [
        { name: "nullable", wire_type: "text" },
        { name: "integer", wire_type: "int" },
        { name: "enabled", wire_type: "boolean" },
        { name: "amount", wire_type: "decimal" },
        { name: "ratio", wire_type: "float" },
        { name: "created_on", wire_type: "date" },
        { name: "at_time", wire_type: "time" },
        { name: "created_at", wire_type: "timestamp" },
        { name: "observed_at", wire_type: "timestamptz" },
        { name: "payload", wire_type: "json_text" },
        { name: "blob", wire_type: "binary" },
        { name: "record_id", wire_type: "uuid" },
      ],
      rows: [[null, "9007199254740993", true, "9007199254740993.25", 1.5, "2026-08-10", "12:34:56", "2026-08-10T12:34:56", "2026-08-10T12:34:56Z", '{"safe":true}', "AQI=", "f16d3e77-6d91-449f-9427-4f070a12e1d8"]],
      returned_rows: 1,
      total_returned: 1,
    },
    meta: {
      page: { page_size: 100, has_more: false },
      audit: { state: "recorded", audit_event_id: "audit-1", execution_id: "execution-1", trace_id: "trace-1", outcome: "succeeded" },
    },
  }),
  nextPage: vi.fn(),
};

describe("P0 workbench", () => {
  it("supports the keyboard connection → schema → run flow and renders wire values without sensitive fields", async () => {
    const user = userEvent.setup();
    render(<App api={api} workspaceId="workspace-1" />);

    await user.click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));
    await user.click(await screen.findByRole("treeitem", { name: /public/ }));
    await user.click(await screen.findByRole("treeitem", { name: /users/ }));
    await user.click(screen.getByRole("button", { name: /运行查询/ }));

    expect(await screen.findByText("9007199254740993.25")).toBeTruthy();
    expect(screen.getByText("9007199254740993")).toBeTruthy();
    expect(screen.getByText("NULL")).toBeTruthy();
    expect(screen.getByText("true")).toBeTruthy();
    expect(screen.getByText("2026-08-10T12:34:56Z")).toBeTruthy();
    expect(screen.getByText('{"safe":true}')).toBeTruthy();
    expect(screen.getByText("AQI=")).toBeTruthy();
    expect(screen.queryByText(sensitiveResponseCanary)).toBeNull();
  });

  it("announces Retry-After and keeps the SQL available after a 429", async () => {
    const rateLimited: WebDbApi = { ...api, execute: vi.fn().mockRejectedValue({ code: "rate_limited", status: 429, retryAfterSeconds: 12, message: "rate_limited" }) };
    render(<App api={rateLimited} workspaceId="workspace-1" />);
    await userEvent.setup().click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));
    fireEvent.click(screen.getByRole("button", { name: /运行查询/ }));

    expect(await screen.findByText(/12 秒后/)).toBeTruthy();
    expect(screen.getByRole("textbox", { name: /SQL 编辑器/ })).toBeTruthy();
  });

  it("renders an explicit empty connection state without inventing a fallback connection", async () => {
    const empty: WebDbApi = { ...api, connections: vi.fn().mockResolvedValue([]) };
    render(<App api={empty} workspaceId="workspace-1" />);

    expect(await screen.findByText("当前没有可用连接。" )).toBeTruthy();
    expect(screen.getByRole("button", { name: /运行查询/ })).toHaveProperty("disabled", true);
  });

  it("retries failed connection and schema loads without reloading the page", async () => {
    const retryingConnections = vi.fn()
      .mockRejectedValueOnce({ code: "connection_unavailable", message: "connection_unavailable" })
      .mockResolvedValue([{ id: "pg", name: "Synthetic PostgreSQL", engine: "postgresql", environment: "staging", database: "app" }]);
    const retryingSchemas = vi.fn()
      .mockRejectedValueOnce({ code: "connection_unavailable", message: "connection_unavailable" })
      .mockResolvedValue([{ name: "public", catalog: "app" }]);
    const retrying: WebDbApi = { ...api, connections: retryingConnections, schemas: retryingSchemas };
    const user = userEvent.setup();
    render(<App api={retrying} workspaceId="workspace-1" />);

    await user.click(await screen.findByRole("button", { name: "重试" }));
    await user.click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));
    await user.click(await screen.findByRole("button", { name: "重试" }));

    expect(await screen.findByRole("treeitem", { name: "public" })).toBeTruthy();
    expect(retryingConnections).toHaveBeenCalledTimes(2);
    expect(retryingSchemas).toHaveBeenCalledTimes(2);
  });

  it("aborts old schema transport when a different connection is selected", async () => {
    let pgSignal: AbortSignal | undefined;
    const switching: WebDbApi = {
      ...api,
      connections: vi.fn().mockResolvedValue([
        { id: "pg", name: "Synthetic PostgreSQL", engine: "postgresql", environment: "staging", database: "app" },
        { id: "mysql", name: "Synthetic MySQL", engine: "mysql", environment: "development", database: "shop" },
      ]),
      schemas: vi.fn((_workspaceId, connectionId, signal) => {
        if (connectionId === "pg") {
          pgSignal = signal;
          return new Promise<never>(() => undefined);
        }
        return Promise.resolve([{ name: "commerce", catalog: "shop" }]);
      }),
    };
    const user = userEvent.setup();
    render(<App api={switching} workspaceId="workspace-1" />);
    await user.click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));
    await user.click(await screen.findByRole("treeitem", { name: /Synthetic MySQL/ }));

    expect(pgSignal?.aborted).toBe(true);
    expect(await screen.findByRole("treeitem", { name: "commerce" })).toBeTruthy();
  });

  it("prevents duplicate runs and reports local cancellation without waiting for a server response", async () => {
    let capturedSignal: AbortSignal | undefined;
    const cancelled: WebDbApi = {
      ...api,
      execute: vi.fn((_workspaceId, _request, signal) => new Promise<never>((_, reject) => {
        capturedSignal = signal;
        signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
      })),
    };
    const user = userEvent.setup();
    render(<App api={cancelled} workspaceId="workspace-1" />);
    await user.click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));

    const run = screen.getByRole("button", { name: /运行查询/ });
    fireEvent.click(run);
    fireEvent.click(run);
    expect(cancelled.execute).toHaveBeenCalledTimes(1);

    expect(capturedSignal?.aborted).toBe(true);
    expect(await screen.findByText(/查询已取消/)).toBeTruthy();
  });

  it.each([
    ["statement_not_allowed"],
    ["connection_unavailable"],
    ["query_timeout"],
  ])("shows a safe server decision for %s", async (code) => {
    const rejected: WebDbApi = { ...api, execute: vi.fn().mockRejectedValue({ code, message: code }) };
    render(<App api={rejected} workspaceId="workspace-1" />);
    await userEvent.setup().click(await screen.findByRole("treeitem", { name: /Synthetic PostgreSQL/ }));
    fireEvent.click(screen.getByRole("button", { name: /运行查询/ }));

    expect((await screen.findByRole("alert")).textContent).toContain(code);
  });
});
