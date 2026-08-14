import { defineConfig, devices } from "@playwright/test";

const isCI = Boolean(process.env.CI);
const useSyntheticWebServer = process.env.PLAYWRIGHT_SYNTHETIC_WEB_SERVER === "1";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  globalTimeout: isCI ? 10 * 60_000 : undefined,
  fullyParallel: false,
  forbidOnly: isCI,
  failOnFlakyTests: isCI,
  retries: isCI ? 1 : 0,
  workers: isCI ? 1 : undefined,
  reporter: [["html", { open: "never" }]],
  snapshotPathTemplate: "{testDir}/{testFilePath}-snapshots/{arg}-{platform}{ext}",
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:3000",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "off",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 900 },
        colorScheme: "dark",
      },
    },
  ],
  // WEB-37's synthetic visual test keeps its isolated Vite fixture. The real
  // @smoke path never enables this and Compose is managed explicitly outside Playwright.
  webServer: useSyntheticWebServer
    ? {
        command: "npm run dev -- --host 127.0.0.1",
        port: 5173,
        reuseExistingServer: false,
        env: {
          VITE_WEBDB_WORKSPACE_ID: "workspace-e2e",
          VITE_API_BASE_URL: "/api/v1",
        },
      }
    : undefined,
});
