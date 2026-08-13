import { createHash } from "node:crypto";
import { existsSync, lstatSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
import { inflateRawSync } from "node:zlib";

const MAX_FILES = 20_000;
const MAX_EXPANDED_BYTES = 512 * 1024 * 1024;
const ZIP_EOCD_SIGNATURE = 0x06054b50;
const ZIP_CENTRAL_SIGNATURE = 0x02014b50;
const ZIP_LOCAL_SIGNATURE = 0x04034b50;

function artifactRoots(args) {
  if (args.length < 3 || args[0] !== "--values-file") throw new Error("invalid arguments");
  return { valuesFile: resolve(args[1]), roots: args.slice(2).map((root) => resolve(root)) };
}

function concreteNeedles(valuesFile) {
  const values = readFileSync(valuesFile, "utf8")
    .split(/\r?\n/u)
    .map((value) => value.trim())
    .filter(Boolean);
  if (values.length === 0) throw new Error("empty values file");

  const encoded = new Set();
  for (const value of values) {
    encoded.add(value);
    encoded.add(encodeURIComponent(value));
    encoded.add(Buffer.from(value, "utf8").toString("base64"));
    encoded.add(JSON.stringify(value).slice(1, -1));
  }
  return [...encoded].map((value) => Buffer.from(value, "utf8"));
}

function filesUnder(root) {
  if (!existsSync(root)) return [];
  const result = [];
  const pending = [root];
  while (pending.length > 0) {
    const current = pending.pop();
    const stat = lstatSync(current);
    if (stat.isSymbolicLink()) throw new Error("symbolic link in artifact");
    if (stat.isDirectory()) {
      for (const entry of readdirSync(current)) pending.push(resolve(current, entry));
    } else if (stat.isFile()) {
      result.push(current);
    }
  }
  return result;
}

function endOfCentralDirectory(bytes) {
  const lowerBound = Math.max(0, bytes.length - 65_557);
  for (let offset = bytes.length - 22; offset >= lowerBound; offset -= 1) {
    if (bytes.readUInt32LE(offset) === ZIP_EOCD_SIGNATURE) return offset;
  }
  throw new Error("zip directory missing");
}

function zipEntries(bytes) {
  const eocd = endOfCentralDirectory(bytes);
  if (bytes.readUInt16LE(eocd + 4) !== 0 || bytes.readUInt16LE(eocd + 6) !== 0) {
    throw new Error("multi-disk zip unsupported");
  }

  const entryCount = bytes.readUInt16LE(eocd + 10);
  let offset = bytes.readUInt32LE(eocd + 16);
  const entries = [];

  for (let index = 0; index < entryCount; index += 1) {
    if (bytes.readUInt32LE(offset) !== ZIP_CENTRAL_SIGNATURE) throw new Error("invalid zip directory");
    const flags = bytes.readUInt16LE(offset + 8);
    const method = bytes.readUInt16LE(offset + 10);
    const compressedSize = bytes.readUInt32LE(offset + 20);
    const expandedSize = bytes.readUInt32LE(offset + 24);
    const nameLength = bytes.readUInt16LE(offset + 28);
    const extraLength = bytes.readUInt16LE(offset + 30);
    const commentLength = bytes.readUInt16LE(offset + 32);
    const localOffset = bytes.readUInt32LE(offset + 42);

    if ((flags & 1) !== 0 || compressedSize === 0xffffffff || expandedSize === 0xffffffff) {
      throw new Error("encrypted or zip64 artifact unsupported");
    }
    if (bytes.readUInt32LE(localOffset) !== ZIP_LOCAL_SIGNATURE) throw new Error("invalid local zip entry");
    const localNameLength = bytes.readUInt16LE(localOffset + 26);
    const localExtraLength = bytes.readUInt16LE(localOffset + 28);
    const dataOffset = localOffset + 30 + localNameLength + localExtraLength;
    const compressed = bytes.subarray(dataOffset, dataOffset + compressedSize);
    if (compressed.length !== compressedSize) throw new Error("truncated zip entry");

    let expanded;
    if (method === 0) expanded = Buffer.from(compressed);
    else if (method === 8) expanded = inflateRawSync(compressed, { maxOutputLength: MAX_EXPANDED_BYTES });
    else throw new Error("unsupported zip compression");
    if (expanded.length !== expandedSize) throw new Error("zip size mismatch");
    entries.push(expanded);

    offset += 46 + nameLength + extraLength + commentLength;
  }
  return entries;
}

function embeddedResources(bytes) {
  const text = bytes.toString("latin1");
  const resources = [];
  const dataUrl = /data:[^,\s]{1,256};base64,([A-Za-z0-9+/=\r\n]{4,})/gu;
  for (const match of text.matchAll(dataUrl)) {
    const encoded = match[1].replace(/[\r\n]/gu, "");
    if (encoded.length % 4 === 0) resources.push(Buffer.from(encoded, "base64"));
  }
  return resources;
}

function isZip(bytes) {
  return bytes.length >= 4 && bytes.readUInt32LE(0) === ZIP_LOCAL_SIGNATURE;
}

export function scanPlaywrightArtifacts(valuesFile, roots) {
  const needles = concreteNeedles(valuesFile);
  const queue = roots.flatMap(filesUnder).map((file) => readFileSync(file));
  const seen = new Set();
  let expandedBytes = 0;

  while (queue.length > 0) {
    const bytes = queue.pop();
    const digest = createHash("sha256").update(bytes).digest("hex");
    if (seen.has(digest)) continue;
    seen.add(digest);
    expandedBytes += bytes.length;
    if (seen.size > MAX_FILES || expandedBytes > MAX_EXPANDED_BYTES) throw new Error("artifact limit exceeded");

    if (needles.some((needle) => bytes.indexOf(needle) !== -1)) return false;
    if (isZip(bytes)) queue.push(...zipEntries(bytes));
    queue.push(...embeddedResources(bytes));
  }
  return true;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { valuesFile, roots } = artifactRoots(process.argv.slice(2));
    console.log(`safe_to_upload=${scanPlaywrightArtifacts(valuesFile, roots)}`);
  } catch {
    console.log("safe_to_upload=false");
  }
}
