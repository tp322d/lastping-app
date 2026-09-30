// Zip helpers for mcpb/build.sh. A signed bundle is the packed zip followed by
// the signature block; strict zip readers (Claude Desktop's among them) reject
// bytes after the end of central directory record unless its comment length
// declares them. So the build declares the block as the zip comment before
// signing, and checks every bundle the way a strict reader would.
//
//   node mcpb/zip.mjs declare-comment <in.zip> <out.zip> <length>
//       copies a zip that has no comment, setting the comment length field
//   node mcpb/zip.mjs check <file>
//       fails unless the comment length equals the bytes after the record
//   node mcpb/zip.mjs open <file> <dir holding node_modules/yauzl>
//       fails unless yauzl, a strict reader, opens the zip and lists entries
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

const EOCD_SIG = 0x06054b50;
const EOCD_SIZE = 22; // the fixed part, before the comment
const MAX_COMMENT = 0xffff;

function fail(msg) {
  console.error("error: " + msg);
  process.exit(1);
}

// The last end of central directory signature in the file, searched from the
// end the way strict readers search, within the reach of a maximal comment.
function findEOCD(buf) {
  const stop = Math.max(0, buf.length - EOCD_SIZE - MAX_COMMENT);
  for (let i = buf.length - EOCD_SIZE; i >= stop; i--) {
    if (buf.readUInt32LE(i) === EOCD_SIG) return i;
  }
  return -1;
}

const [cmd, ...args] = process.argv.slice(2);
switch (cmd) {
  case "declare-comment": {
    const [src, dst, lenArg] = args;
    const len = Number(lenArg);
    if (!Number.isInteger(len) || len < 0 || len > MAX_COMMENT) {
      fail(`comment length ${lenArg} is outside 0..${MAX_COMMENT}`);
    }
    const buf = Buffer.from(readFileSync(src));
    const at = buf.length - EOCD_SIZE;
    if (at < 0 || buf.readUInt32LE(at) !== EOCD_SIG) {
      fail(`${src} does not end with an end of central directory record`);
    }
    if (buf.readUInt16LE(at + 20) !== 0) fail(`${src} already has a zip comment`);
    buf.writeUInt16LE(len, at + 20);
    writeFileSync(dst, buf);
    break;
  }
  case "check": {
    const [file] = args;
    const buf = readFileSync(file);
    const at = findEOCD(buf);
    if (at < 0) fail(`${file} has no end of central directory record`);
    const declared = buf.readUInt16LE(at + 20);
    const trailing = buf.length - at - EOCD_SIZE;
    if (declared !== trailing) {
      fail(`${file}: the zip comment length is ${declared} but ${trailing} bytes follow the end of central directory record`);
    }
    console.log(`${file}: zip comment length ${declared} matches the trailing bytes`);
    break;
  }
  case "open": {
    const [file, dir] = args;
    const yauzl = createRequire(path.join(path.resolve(dir), "noop.js"))("yauzl");
    yauzl.open(file, { lazyEntries: true }, (err, zip) => {
      if (err) fail(`${file} does not open as a zip: ${err.message}`);
      let n = 0;
      zip.on("error", (e) => fail(`${file} does not read as a zip: ${e.message}`));
      zip.on("entry", () => { n++; zip.readEntry(); });
      zip.on("end", () => {
        if (n !== zip.entryCount) fail(`${file}: read ${n} of ${zip.entryCount} entries`);
        console.log(`${file}: a strict zip reader lists ${n} entries`);
      });
      zip.readEntry();
    });
    break;
  }
  default:
    fail(`unknown command ${cmd}`);
}
