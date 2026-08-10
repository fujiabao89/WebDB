import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  use: {
    baseURL: "http://127.0.0.1:5173",
    browserName: "chromium",
    channel: "chrome",
    viewport: { width: 1440, height: 900 },
    colorScheme: "dark",
  },
  webServer: {
    command: "npm run dev -- --host 127.0.0.1",
    port: 5173,
    reuseExistingServer: !process.env.CI,
    env: {
      VITE_WEBDB_WORKSPACE_ID: "workspace-e2e",
      VITE_API_BASE_URL: "/api/v1",
    },
  },
});
