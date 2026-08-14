import { expect, test } from "@playwright/test";

const databasePorts = new Set(["3306", "5432", "5433"]);

test("@smoke Web 与 API 代理可用", async ({ page }) => {
  const requestUrls: string[] = [];
  const responses: Array<{ status: number; url: string }> = [];

  page.on("request", (request) => requestUrls.push(request.url()));
  page.on("response", (response) => {
    responses.push({ status: response.status(), url: response.url() });
  });

  const navigationResponse = await page.goto("/");

  expect(navigationResponse).not.toBeNull();
  expect(navigationResponse?.ok()).toBe(true);
  await expect(page.getByRole("main")).toBeVisible();

  const health = await page.evaluate(async () => {
    const response = await fetch("/api/health", { credentials: "same-origin" });
    return {
      body: (await response.json()) as { status?: unknown },
      status: response.status,
      url: response.url,
    };
  });

  expect(health.status).toBe(200);
  expect(health.body.status).toBe("ok");
  expect(new URL(health.url).origin).toBe(new URL(page.url()).origin);
  await expect
    .poll(() => responses.find((response) => new URL(response.url).pathname === "/api/health")?.status)
    .toBe(200);

  const databaseRequestCount = requestUrls.filter((url) => databasePorts.has(new URL(url).port)).length;
  expect(databaseRequestCount).toBe(0);
});
