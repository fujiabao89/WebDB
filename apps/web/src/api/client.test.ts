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
    expect(consoleSpy).not.toHaveBeenCalled();
  });

  it("does not turn an aborted request into a server query_cancelled response", async () => {
    const controller = new AbortController();
    const fetcher = vi.fn().mockRejectedValue(new DOMException("Aborted", "AbortError"));
    const client = createApiClient({ baseUrl: "/api/v1", fetcher });

    await expect(client.connections("workspace-1", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
});
