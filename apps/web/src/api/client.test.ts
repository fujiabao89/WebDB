import { describe, expect, it, vi } from "vitest";
import { ApiError, createApiClient } from "./client";

describe("WebDB HTTP client", () => {
  it("uses the approved execution DTO and maps 429 Retry-After without logging the request", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "rate_limited", message: "rate_limited" } }), {
        status: 429,
        headers: { "Content-Type": "application/json", "Retry-After": "17" },
      }),
    );
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(
      client.execute("workspace-1", {
        connection_id: "connection-1",
        sql: "SELECT 1",
        page_size: 100,
      }),
    ).rejects.toMatchObject({ code: "rate_limited", status: 429, retryAfterSeconds: 17 } satisfies Partial<ApiError>);

    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/workspaces/workspace-1/executions",
      expect.objectContaining({ method: "POST", signal: undefined }),
    );
    expect(JSON.parse(fetcher.mock.calls[0]?.[1]?.body as string)).toEqual({
      connection_id: "connection-1",
      sql: "SELECT 1",
      page_size: 100,
    });
    expect(consoleSpy).not.toHaveBeenCalled();
  });

  it("encodes workspace and connection identifiers as path segments", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await client.schemas("workspace-1/../workspace-2", "connection-1/../connection-2");

    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/workspaces/workspace-1%2F..%2Fworkspace-2/connections/connection-1%2F..%2Fconnection-2/schemas",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("treats a malformed successful response as an internal API failure", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("not-json", { status: 200, headers: { "Content-Type": "text/plain" } }));
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(client.connections("workspace-1")).rejects.toMatchObject({ code: "internal_error", status: 200 } satisfies Partial<ApiError>);
  });

  it("rejects a successful response whose collection data is not an array", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: null }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(client.connections("workspace-1")).rejects.toMatchObject({ code: "internal_error", status: 200 } satisfies Partial<ApiError>);
  });

  it("retains Retry-After metadata for non-429 gateway responses", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "connection_unavailable", message: "connection_unavailable" } }), {
        status: 503,
        headers: { "Content-Type": "application/json", "Retry-After": "30" },
      }),
    );
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(client.connections("workspace-1")).rejects.toMatchObject({
      code: "connection_unavailable",
      status: 503,
      retryAfterSeconds: 30,
    } satisfies Partial<ApiError>);
  });

  it("does not turn an aborted request into a server query_cancelled response", async () => {
    const controller = new AbortController();
    const fetcher = vi.fn().mockRejectedValue(new DOMException("Aborted", "AbortError"));
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(client.connections("workspace-1", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
});
