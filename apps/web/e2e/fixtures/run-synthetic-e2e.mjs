import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const playwrightCli = require.resolve("@playwright/test/cli");
const result = spawnSync(
  process.execPath,
  [playwrightCli, "test", "--grep-invert", "@smoke"],
  {
    env: {
      ...process.env,
      PLAYWRIGHT_BASE_URL: "http://127.0.0.1:5173",
      PLAYWRIGHT_SYNTHETIC_WEB_SERVER: "1",
    },
    stdio: "inherit",
  },
);

if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
