import { randomBytes } from "node:crypto";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deflateRawSync } from "node:zlib";

import { scanPlaywrightArtifacts } from "./scan-playwright-artifacts.mjs";

function traceZip(name, contents, prefixLength = 0, compressedSuffix = Buffer.alloc(0), metadata = {}) {
  const filename = Buffer.from(name, "utf8");
  const localExtra = metadata.localExtra ?? Buffer.alloc(0);
  const centralExtra = metadata.centralExtra ?? Buffer.alloc(0);
  const centralComment = metadata.centralComment ?? Buffer.alloc(0);
  const eocdComment = metadata.eocdComment ?? Buffer.alloc(0);
  const compressed = Buffer.concat([deflateRawSync(contents), compressedSuffix]);
  const local = Buffer.alloc(30);
  local.writeUInt32LE(0x04034b50, 0);
  local.writeUInt16LE(20, 4);
  local.writeUInt16LE(8, 8);
  local.writeUInt32LE(0, 14);
  local.writeUInt32LE(compressed.length, 18);
  local.writeUInt32LE(contents.length, 22);
  local.writeUInt16LE(filename.length, 26);
  local.writeUInt16LE(localExtra.length, 28);

  const localRecord = Buffer.concat([local, filename, localExtra, compressed]);
  const central = Buffer.alloc(46);
  central.writeUInt32LE(0x02014b50, 0);
  central.writeUInt16LE(20, 4);
  central.writeUInt16LE(20, 6);
  central.writeUInt16LE(8, 10);
  central.writeUInt32LE(0, 16);
  central.writeUInt32LE(compressed.length, 20);
  central.writeUInt32LE(contents.length, 24);
  central.writeUInt16LE(filename.length, 28);
  central.writeUInt16LE(centralExtra.length, 30);
  central.writeUInt16LE(centralComment.length, 32);
  central.writeUInt32LE(prefixLength, 42);

  const centralRecord = Buffer.concat([central, filename, centralExtra, centralComment]);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(1, 8);
  eocd.writeUInt16LE(1, 10);
  eocd.writeUInt32LE(centralRecord.length, 12);
  eocd.writeUInt32LE(prefixLength + localRecord.length, 16);
  eocd.writeUInt16LE(eocdComment.length, 20);
  return Buffer.concat([localRecord, centralRecord, eocd, eocdComment]);
}

function extraField(contents) {
  const header = Buffer.alloc(4);
  header.writeUInt16LE(0xcafe, 0);
  header.writeUInt16LE(contents.length, 2);
  return Buffer.concat([header, contents]);
}

function isRejected(values, roots, options) {
  try {
    return !scanPlaywrightArtifacts(values, roots, options);
  } catch {
    return true;
  }
}

const root = mkdtempSync(join(tmpdir(), "webdb-playwright-artifact-scan-"));

try {
  const canary = `webdb-artifact-canary-${randomBytes(24).toString("hex")}`;
  const values = join(root, "values.txt");
  const safe = join(root, "safe.txt");
  const direct = join(root, "attachment.txt");
  const traceDirectory = join(root, "trace");
  const trace = join(traceDirectory, "trace.zip");
  const prefixedTrace = join(traceDirectory, "prefixed-trace.zip");
  const concatenatedTrace = join(traceDirectory, "concatenated-trace.zip");
  const absoluteConcatenatedTrace = join(traceDirectory, "absolute-concatenated-trace.zip");
  const paddedConcatenatedTrace = join(traceDirectory, "padded-concatenated-trace.zip");
  const trailingZipTrace = join(traceDirectory, "trailing-zip-trace.zip");
  const trailingDeflateTrace = join(traceDirectory, "trailing-deflate-trace.zip");
  const eocdCommentTrace = join(traceDirectory, "eocd-comment-trace.zip");
  const localExtraTrace = join(traceDirectory, "local-extra-trace.zip");
  const centralExtraTrace = join(traceDirectory, "central-extra-trace.zip");
  const oversizedTrace = join(traceDirectory, "oversized-trace.zip");
  const htmlDirectory = join(root, "html");
  const html = join(htmlDirectory, "report.html");
  const secondSafe = join(root, "second-safe.txt");

  writeFileSync(values, canary, { encoding: "utf8", mode: 0o600 });
  writeFileSync(safe, "synthetic field names only: password secret_ref KEK", "utf8");
  writeFileSync(direct, canary, "utf8");
  mkdirSync(traceDirectory);
  writeFileSync(trace, traceZip("trace-entry.txt", Buffer.from(canary, "utf8")));
  writeFileSync(
    prefixedTrace,
    Buffer.concat([
      Buffer.from("MZ synthetic self-extractor prefix\n", "utf8"),
      traceZip("trace-entry.txt", Buffer.from(canary, "utf8")),
    ]),
  );
  const secretTrace = traceZip("secret-entry.txt", Buffer.from(canary, "utf8"));
  writeFileSync(
    concatenatedTrace,
    Buffer.concat([secretTrace, traceZip("safe-entry.txt", Buffer.from("safe", "utf8"))]),
  );
  writeFileSync(
    absoluteConcatenatedTrace,
    Buffer.concat([
      secretTrace,
      traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), secretTrace.length),
    ]),
  );
  const padding = Buffer.from("synthetic gap", "utf8");
  writeFileSync(
    paddedConcatenatedTrace,
    Buffer.concat([
      secretTrace,
      padding,
      traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), secretTrace.length + padding.length),
    ]),
  );
  writeFileSync(
    trailingZipTrace,
    traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), 0, secretTrace),
  );
  writeFileSync(
    trailingDeflateTrace,
    traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), 0, deflateRawSync(Buffer.from(canary, "utf8"))),
  );
  const nestedMetadata = Buffer.concat([secretTrace, Buffer.from("metadata padding", "utf8")]);
  writeFileSync(
    eocdCommentTrace,
    traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), 0, Buffer.alloc(0), {
      eocdComment: nestedMetadata,
    }),
  );
  writeFileSync(
    localExtraTrace,
    traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), 0, Buffer.alloc(0), {
      localExtra: extraField(nestedMetadata),
    }),
  );
  writeFileSync(
    centralExtraTrace,
    traceZip("safe-entry.txt", Buffer.from("safe", "utf8"), 0, Buffer.alloc(0), {
      centralExtra: extraField(nestedMetadata),
    }),
  );
  writeFileSync(oversizedTrace, traceZip("large-entry.txt", Buffer.alloc(1_024, 0x61)));
  mkdirSync(htmlDirectory);
  writeFileSync(html, `data:application/octet-stream;base64,${Buffer.from(canary).toString("base64")}`, "utf8");
  writeFileSync(secondSafe, "another safe artifact", "utf8");

  let oversizedRejected = false;
  try {
    scanPlaywrightArtifacts(values, [oversizedTrace], { maxResourceBytes: 256 });
  } catch {
    oversizedRejected = true;
  }

  let fileLimitRejected = false;
  try {
    scanPlaywrightArtifacts(values, [safe, secondSafe], { maxFiles: 1 });
  } catch {
    fileLimitRejected = true;
  }

  const passed =
    scanPlaywrightArtifacts(values, [safe]) &&
    isRejected(values, [direct]) &&
    isRejected(values, [trace]) &&
    isRejected(values, [prefixedTrace]) &&
    isRejected(values, [concatenatedTrace]) &&
    isRejected(values, [absoluteConcatenatedTrace]) &&
    isRejected(values, [paddedConcatenatedTrace]) &&
    isRejected(values, [trailingZipTrace]) &&
    isRejected(values, [trailingDeflateTrace]) &&
    isRejected(values, [eocdCommentTrace]) &&
    isRejected(values, [localExtraTrace]) &&
    isRejected(values, [centralExtraTrace]) &&
    isRejected(values, [html]) &&
    oversizedRejected &&
    fileLimitRejected;
  if (!passed) throw new Error("artifact scanner self-test failed");
  console.log("artifact_scan_self_test=passed");
} catch {
  console.log("artifact_scan_self_test=failed");
  process.exitCode = 1;
} finally {
  rmSync(root, { recursive: true, force: true });
}
