import { expect, test } from "@playwright/test";

const audit = {
  state: "recorded",
  audit_event_id: "audit-synthetic-01",
  execution_id: "execution-synthetic-01",
  trace_id: "trace-synthetic-01",
  outcome: "succeeded",
};

test("P0 keyboard workflow uses the DTO seam, cancels, pages, and renders the desktop workbench", async ({ page }) => {
  let executionCount = 0;
  let releaseSecondExecution: (() => void) | undefined;
  let cancelSecondExecution = false;
  const browserErrors: string[] = [];
  page.on("pageerror", (error) => browserErrors.push(error.message));
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const json = (data: unknown) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });

    if (path.endsWith("/connections")) return json({ data: [{ id: "pg-e2e", name: "Synthetic PostgreSQL", engine: "postgresql", environment: "staging", database: "app" }] });
    if (path.endsWith("/schemas")) return json({ data: [{ name: "public", catalog: "app" }] });
    if (path.endsWith("/tables")) return json({ data: [{ schema: "public", name: "users", type: "TABLE" }] });
    if (path.endsWith("/columns")) return json({ data: [{ name: "id", ordinal: 1, native_type: "uuid", nullable: false, has_default: false }] });
    if (path.endsWith("/executions")) {
      executionCount += 1;
      if (executionCount === 2) {
        await new Promise<void>((resolve) => { releaseSecondExecution = resolve; });
        if (cancelSecondExecution) return route.abort("failed");
      }
      return json({
        data: { columns: [{ name: "id", wire_type: "uuid" }], rows: [["9f7494b8-0c94-46ac-b7f6-1ed403d56b5e"]], returned_rows: 1, total_returned: 2 },
        meta: { page: { page_size: 100, has_more: true, next_page_token: "synthetic-memory-only-token" }, audit },
      });
    }
    if (path.endsWith("/query-pages")) {
      return json({
        data: { columns: [{ name: "id", wire_type: "uuid" }], rows: [["e2e-second-page"]], returned_rows: 1, total_returned: 2 },
        meta: { page: { page_size: 100, has_more: false }, audit: { ...audit, audit_event_id: "audit-synthetic-02", execution_id: "execution-synthetic-02" } },
      });
    }
    return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "internal_error", message: "internal_error" } }) });
  });

  await page.goto("/");
  await page.getByRole("treeitem", { name: /Synthetic PostgreSQL/ }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("treeitem", { name: "public" })).toBeVisible();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("treeitem", { name: /users/ })).toBeVisible();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  expect(browserErrors).toEqual([]);

  await page.locator(".monaco-editor .view-lines").click();
  await page.keyboard.press("End");
  await page.keyboard.type(" ");
  await page.keyboard.press("Control+Enter");
  await expect(page.getByText("9f7494b8-0c94-46ac-b7f6-1ed403d56b5e")).toBeVisible();
  await expect(page.getByRole("button", { name: /加载下一页/ })).toBeVisible();
  await page.getByRole("button", { name: /加载下一页/ }).click();
  await expect(page.getByText("e2e-second-page")).toBeVisible();
  await expect(page.getByText("synthetic-memory-only-token")).toHaveCount(0);
  expect(executionCount).toBe(1);

  await page.getByRole("tab", { name: "消息" }).click();
  await expect(page.getByLabel("服务端审计回执")).toContainText("audit-synthetic-02");
  await expect(page.getByRole("tab", { name: "消息" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "结果" }).click();
  await expect(page.getByRole("tab", { name: "结果" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("e2e-second-page")).toBeVisible();
  await expect(page).toHaveScreenshot("workbench-desktop.png", { fullPage: true, animations: "disabled" });

  await page.locator(".monaco-editor .view-lines").click();
  await page.keyboard.press("Control+Enter");
  await expect(page.getByRole("button", { name: /取消查询/ })).toBeVisible();
  await page.getByRole("button", { name: /取消查询/ }).click();
  await expect(page.getByText(/查询已取消/)).toBeVisible();
  cancelSecondExecution = true;
  releaseSecondExecution?.();
  expect(executionCount).toBe(2);
});
