import { createHash } from "node:crypto";
import { existsSync, lstatSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { extname, resolve } from "node:path";
import { inflateRawSync } from "node:zlib";

const MAX_FILES = 20_000;
const MAX_EXPANDED_BYTES = 512 * 1024 * 1024;
const MAX_RESOURCE_BYTES = 64 * 1024 * 1024;
const MAX_NESTING_DEPTH = 64;
const ZIP_EOCD_SIGNATURE = 0x06054b50;
const ZIP_CENTRAL_SIGNATURE = 0x02014b50;
const ZIP_LOCAL_SIGNATURE = 0x04034b50;
const ZIP_MARKERS = [
  Buffer.from([0x50, 0x4b, 0x03, 0x04]),
  Buffer.from([0x50, 0x4b, 0x01, 0x02]),
  Buffer.from([0x50, 0x4b, 0x05, 0x06]),
];

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

function* filesUnder(root) {
  if (!existsSync(root)) return;
  const pending = [root];
  while (pending.length > 0) {
    const current = pending.pop();
    const stat = lstatSync(current);
    if (stat.isSymbolicLink()) throw new Error("symbolic link in artifact");
    if (stat.isDirectory()) {
      for (const entry of readdirSync(current)) pending.push(resolve(current, entry));
    } else if (stat.isFile()) {
      yield { path: current, size: stat.size };
    }
  }
}

function endOfCentralDirectory(bytes) {
  if (bytes.length < 22) return null;
  const lowerBound = Math.max(0, bytes.length - 65_557);
  for (let offset = bytes.length - 22; offset >= lowerBound; offset -= 1) {
    if (
      bytes.readUInt32LE(offset) === ZIP_EOCD_SIGNATURE &&
      offset + 22 + bytes.readUInt16LE(offset + 20) === bytes.length
    ) {
      return offset;
    }
  }
  return null;
}

function zipLayout(bytes) {
  const eocd = endOfCentralDirectory(bytes);
  if (eocd === null) return null;
  if (bytes.readUInt16LE(eocd + 4) !== 0 || bytes.readUInt16LE(eocd + 6) !== 0) {
    throw new Error("multi-disk zip unsupported");
  }

  const diskEntryCount = bytes.readUInt16LE(eocd + 8);
  const entryCount = bytes.readUInt16LE(eocd + 10);
  const centralSize = bytes.readUInt32LE(eocd + 12);
  const declaredCentralOffset = bytes.readUInt32LE(eocd + 16);
  if (diskEntryCount !== entryCount || entryCount === 0xffff || centralSize === 0xffffffff) {
    throw new Error("zip64 artifact unsupported");
  }

  const centralOffset = eocd - centralSize;
  const prefixLength = centralOffset - declaredCentralOffset;
  if (centralOffset < 0 || prefixLength < 0 || centralOffset + centralSize !== eocd) {
    throw new Error("invalid zip directory bounds");
  }
  if (entryCount > 0 && bytes.readUInt32LE(centralOffset) !== ZIP_CENTRAL_SIGNATURE) {
    throw new Error("invalid zip directory");
  }
  return { centralOffset, centralSize, entryCount, prefixLength, eocd };
}

function* zipEntries(bytes, layout, limits, account, coveredRanges) {
  let offset = layout.centralOffset;

  for (let index = 0; index < layout.entryCount; index += 1) {
    if (offset + 46 > layout.centralOffset + layout.centralSize) {
      throw new Error("truncated zip directory");
    }
    if (bytes.readUInt32LE(offset) !== ZIP_CENTRAL_SIGNATURE) throw new Error("invalid zip directory");
    coveredRanges.push([offset, offset + 46]);
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
    const nextOffset = offset + 46 + nameLength + extraLength + commentLength;
    if (nextOffset > layout.centralOffset + layout.centralSize) throw new Error("invalid zip directory entry");
    account(expandedSize);

    const adjustedLocalOffset = localOffset + layout.prefixLength;
    if (adjustedLocalOffset + 30 > layout.centralOffset) throw new Error("invalid local zip entry bounds");
    if (bytes.readUInt32LE(adjustedLocalOffset) !== ZIP_LOCAL_SIGNATURE) {
      throw new Error("invalid local zip entry");
    }
    if (bytes.readUInt16LE(adjustedLocalOffset + 8) !== method) throw new Error("zip method mismatch");
    const localNameLength = bytes.readUInt16LE(adjustedLocalOffset + 26);
    const localExtraLength = bytes.readUInt16LE(adjustedLocalOffset + 28);
    const dataOffset = adjustedLocalOffset + 30 + localNameLength + localExtraLength;
    const compressed = bytes.subarray(dataOffset, dataOffset + compressedSize);
    if (compressed.length !== compressedSize || dataOffset + compressedSize > layout.centralOffset) {
      throw new Error("truncated zip entry");
    }
    coveredRanges.push([adjustedLocalOffset, adjustedLocalOffset + 30]);
    coveredRanges.push([dataOffset, dataOffset + compressedSize]);

    let expanded;
    if (method === 0) expanded = Buffer.from(compressed);
    else if (method === 8) {
      const inflated = inflateRawSync(compressed, {
        info: true,
        maxOutputLength: Math.min(expandedSize + 1, limits.maxResourceBytes),
      });
      if (inflated.engine.bytesWritten !== compressedSize) throw new Error("zip deflate trailing data");
      expanded = inflated.buffer;
    }
    else throw new Error("unsupported zip compression");
    if (expanded.length !== expandedSize) throw new Error("zip size mismatch");
    yield expanded;

    offset = nextOffset;
  }
  if (offset !== layout.centralOffset + layout.centralSize) throw new Error("zip directory size mismatch");
}

function* uncoveredZipRegions(bytes, layout, coveredRanges) {
  const ranges = [
    ...coveredRanges,
    [layout.eocd, layout.eocd + 22],
  ].sort((left, right) => left[0] - right[0]);
  let coveredUntil = 0;
  for (const [start, end] of ranges) {
    if (start < 0 || end < start || end > bytes.length) throw new Error("invalid zip coverage");
    if (start > coveredUntil) yield bytes.subarray(coveredUntil, start);
    coveredUntil = Math.max(coveredUntil, end);
  }
  if (coveredUntil < bytes.length) yield bytes.subarray(coveredUntil);
}

function* embeddedResources(bytes, account) {
  const text = bytes.toString("latin1");
  const dataUrl = /data:[^,\s]{1,256};base64,([A-Za-z0-9+/=\r\n]{4,})/gu;
  for (const match of text.matchAll(dataUrl)) {
    const encoded = match[1].replace(/[\r\n]/gu, "");
    if (encoded.length % 4 === 0) {
      const padding = encoded.endsWith("==") ? 2 : encoded.endsWith("=") ? 1 : 0;
      const decodedSize = (encoded.length / 4) * 3 - padding;
      account(decodedSize);
      const resource = Buffer.from(encoded, "base64");
      if (resource.length !== decodedSize) throw new Error("invalid embedded resource");
      yield resource;
    }
  }
}

function limitsFrom(options) {
  const limits = {
    maxFiles: options.maxFiles ?? MAX_FILES,
    maxExpandedBytes: options.maxExpandedBytes ?? MAX_EXPANDED_BYTES,
    maxResourceBytes: options.maxResourceBytes ?? MAX_RESOURCE_BYTES,
  };
  if (
    !Number.isSafeInteger(limits.maxFiles) ||
    !Number.isSafeInteger(limits.maxExpandedBytes) ||
    !Number.isSafeInteger(limits.maxResourceBytes) ||
    limits.maxFiles < 1 ||
    limits.maxExpandedBytes < 1 ||
    limits.maxResourceBytes < 1
  ) {
    throw new Error("invalid artifact limits");
  }
  return limits;
}

function hasZipMarker(bytes) {
  return ZIP_MARKERS.some((marker) => bytes.indexOf(marker) !== -1);
}

export function scanPlaywrightArtifacts(valuesFile, roots, options = {}) {
  const needles = concreteNeedles(valuesFile);
  const limits = limitsFrom(options);
  const seen = new Set();
  let expandedBytes = 0;
  let fileCount = 0;

  function account(size) {
    if (!Number.isSafeInteger(size) || size < 0 || size > limits.maxResourceBytes) {
      throw new Error("artifact resource limit exceeded");
    }
    fileCount += 1;
    expandedBytes += size;
    if (fileCount > limits.maxFiles || expandedBytes > limits.maxExpandedBytes) {
      throw new Error("artifact limit exceeded");
    }
  }

  function scanResource(bytes, zipExpected, depth) {
    if (depth > MAX_NESTING_DEPTH) throw new Error("artifact nesting limit exceeded");
    const digest = createHash("sha256").update(bytes).digest("hex");
    if (seen.has(digest)) return true;
    seen.add(digest);

    if (needles.some((needle) => bytes.indexOf(needle) !== -1)) return false;
    const layout = zipLayout(bytes);
    if (layout === null && (zipExpected || hasZipMarker(bytes))) throw new Error("zip directory missing");
    if (layout !== null) {
      const coveredRanges = [];
      for (const entry of zipEntries(bytes, layout, limits, account, coveredRanges)) {
        if (!scanResource(entry, false, depth + 1)) return false;
      }
      for (const region of uncoveredZipRegions(bytes, layout, coveredRanges)) {
        if (!scanResource(region, false, depth + 1)) return false;
      }
    }
    for (const resource of embeddedResources(bytes, account)) {
      if (!scanResource(resource, false, depth + 1)) return false;
    }
    return true;
  }

  for (const root of roots) {
    for (const file of filesUnder(root)) {
      account(file.size);
      const bytes = readFileSync(file.path);
      if (bytes.length !== file.size) throw new Error("artifact changed while scanning");
      if (!scanResource(bytes, extname(file.path).toLowerCase() === ".zip", 0)) return false;
    }
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
