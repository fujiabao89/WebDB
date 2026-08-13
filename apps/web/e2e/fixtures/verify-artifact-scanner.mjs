import { randomBytes } from "node:crypto";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deflateRawSync } from "node:zlib";

import { scanPlaywrightArtifacts } from "./scan-playwright-artifacts.mjs";

function traceZip(name, contents) {
  const filename = Buffer.from(name, "utf8");
  const compressed = deflateRawSync(contents);
  const local = Buffer.alloc(30);
  local.writeUInt32LE(0x04034b50, 0);
  local.writeUInt16LE(20, 4);
  local.writeUInt16LE(8, 8);
  local.writeUInt32LE(0, 14);
  local.writeUInt32LE(compressed.length, 18);
  local.writeUInt32LE(contents.length, 22);
  local.writeUInt16LE(filename.length, 26);

  const localRecord = Buffer.concat([local, filename, compressed]);
  const central = Buffer.alloc(46);
  central.writeUInt32LE(0x02014b50, 0);
  central.writeUInt16LE(20, 4);
  central.writeUInt16LE(20, 6);
  central.writeUInt16LE(8, 10);
  central.writeUInt32LE(0, 16);
  central.writeUInt32LE(compressed.length, 20);
  central.writeUInt32LE(contents.length, 24);
  central.writeUInt16LE(filename.length, 28);

  const centralRecord = Buffer.concat([central, filename]);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(1, 8);
  eocd.writeUInt16LE(1, 10);
  eocd.writeUInt32LE(centralRecord.length, 12);
  eocd.writeUInt32LE(localRecord.length, 16);
  return Buffer.concat([localRecord, centralRecord, eocd]);
}

const root = mkdtempSync(join(tmpdir(), "webdb-playwright-artifact-scan-"));

try {
  const canary = `webdb-artifact-canary-${randomBytes(24).toString("hex")}`;
  const values = join(root, "values.txt");
  const safe = join(root, "safe.txt");
  const direct = join(root, "attachment.txt");
  const traceDirectory = join(root, "trace");
  const trace = join(traceDirectory, "trace.zip");
  const htmlDirectory = join(root, "html");
  const html = join(htmlDirectory, "report.html");

  writeFileSync(values, canary, { encoding: "utf8", mode: 0o600 });
  writeFileSync(safe, "synthetic field names only: password secret_ref KEK", "utf8");
  writeFileSync(direct, canary, "utf8");
  mkdirSync(traceDirectory);
  writeFileSync(trace, traceZip("trace-entry.txt", Buffer.from(canary, "utf8")));
  mkdirSync(htmlDirectory);
  writeFileSync(html, `data:application/octet-stream;base64,${Buffer.from(canary).toString("base64")}`, "utf8");

  const passed =
    scanPlaywrightArtifacts(values, [safe]) &&
    !scanPlaywrightArtifacts(values, [direct]) &&
    !scanPlaywrightArtifacts(values, [trace]) &&
    !scanPlaywrightArtifacts(values, [html]);
  if (!passed) throw new Error("artifact scanner self-test failed");
  console.log("artifact_scan_self_test=passed");
} catch {
  console.log("artifact_scan_self_test=failed");
  process.exitCode = 1;
} finally {
  rmSync(root, { recursive: true, force: true });
}
