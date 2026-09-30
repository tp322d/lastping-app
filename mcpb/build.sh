#!/usr/bin/env bash
# Builds the Claude Desktop extension, dist/mcpb/lastping-<version>.mcpb, and a
# copy named lastping.mcpb (the version-less name the download link on
# lastping.dev points at through releases/latest/download).
#
#   mcpb/build.sh v0.2.0                                   # unsigned
#   MCPB_SIGNING_CERT='<PEM>' MCPB_SIGNING_KEY='<PEM>' mcpb/build.sh v0.2.0
#
# Releases are signed; local builds may be unsigned. The release workflow sets
# MCPB_REQUIRE_SIGNATURE=1, and then the script exits 1 before building
# anything unless both secrets are set. Without that variable, and with
# neither secret set, the bundle is built unsigned and the script says so.
#
# The release certificate is self-signed. Claude Desktop shows the extension
# as unverified, because no CA vouches for the certificate; the signature
# still proves the bundle came from the release pipeline and was not altered
# after it was signed.
#
# With both secrets set the bundle is signed, and the build fails unless the
# signature verifies:
#   - the key must belong to the first certificate in MCPB_SIGNING_CERT;
#   - openssl must verify the PKCS#7 signature over the bundle's zip bytes,
#     and the signer must be that certificate.
# Those checks do not ask whether anyone trusts the certificate, which is
# right for the self-signed one. MCPB_CERT_CA_ISSUED stays for a future
# certificate issued by a public CA: set it to 1 for one, and the build
# also verifies the chain against the system CA store (MCPB_CA_PATH, default
# /etc/ssl/certs) and requires the Code Signing extended key usage.
#
# MCPB_SIGNING_CERT may carry intermediate certificates after the signing
# certificate; they go into the signature as the chain. With only one of the
# two secrets set the script exits 1: that is a misconfiguration, not a
# choice.
#
# Needs Go, Node.js (npm, npx) and, to sign, openssl. Runs on Linux or macOS.
set -euo pipefail

MCPB_CLI='@anthropic-ai/mcpb@2.1.2'
YAUZL='yauzl@3.4.0'
MAKEFAT='github.com/randall77/makefat@v0.0.0-20260406194835-1b91746796b7'

version="${1:-${VERSION:-}}"
version="${version#v}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: mcpb/build.sh <version, e.g. v0.2.0>" >&2
  exit 1
fi

sign=0
if [[ -n "${MCPB_SIGNING_CERT:-}" && -n "${MCPB_SIGNING_KEY:-}" ]]; then
  sign=1
elif [[ -n "${MCPB_SIGNING_CERT:-}" || -n "${MCPB_SIGNING_KEY:-}" ]]; then
  echo "error: set both MCPB_SIGNING_CERT and MCPB_SIGNING_KEY to sign, or neither to build unsigned" >&2
  exit 1
fi
if (( ! sign )) && [[ "${MCPB_REQUIRE_SIGNATURE:-}" == "1" ]]; then
  echo "error: MCPB_REQUIRE_SIGNATURE=1 but MCPB_SIGNING_CERT and MCPB_SIGNING_KEY are not set; a release never ships an unsigned bundle" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/mcpb"
stage="$out/stage"
rm -rf "$out"
mkdir -p "$stage/server"

secrets="$(mktemp -d)"
trap 'rm -rf "$secrets"' EXIT
if (( sign )); then
  (
    umask 077
    # One file per certificate: the first signs, the rest are the chain.
    printf '%s\n' "$MCPB_SIGNING_CERT" | awk -v dir="$secrets" '
      /-----BEGIN CERTIFICATE-----/ { n++ }
      n { print > (dir "/cert-" n ".pem") }'
    printf '%s\n' "$MCPB_SIGNING_KEY" > "$secrets/key.pem"
  )
  # Nothing downloaded below (makefat, the mcpb CLI and its dependencies)
  # inherits the secrets through the environment.
  unset MCPB_SIGNING_CERT MCPB_SIGNING_KEY
  chain=("$secrets"/cert-*.pem)
  if [[ ! -f "${chain[0]}" ]]; then
    echo "error: MCPB_SIGNING_CERT holds no PEM certificate" >&2
    exit 1
  fi
  # A key that does not belong to the certificate still produces a signature
  # block; refuse it before building anything.
  if ! cert_pub="$(openssl x509 -noout -pubkey -in "${chain[0]}")" ||
     ! key_pub="$(openssl pkey -pubout -in "$secrets/key.pem")" ||
     [[ "$cert_pub" != "$key_pub" ]]; then
    echo "error: MCPB_SIGNING_KEY does not belong to the first certificate in MCPB_SIGNING_CERT" >&2
    exit 1
  fi
fi

build() {
  local goos="$1" goarch="$2" dst="$3"
  (cd "$root" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags '-s -w' -o "$dst" ./cmd/lastping-mcp)
}

# A binary server has no shell to pick an architecture at launch, so macOS
# gets one universal binary holding both slices.
build darwin amd64 "$out/lastping-mcp-darwin-amd64"
build darwin arm64 "$out/lastping-mcp-darwin-arm64"
(cd "$root" && go run "$MAKEFAT" "$stage/server/lastping-mcp" \
  "$out/lastping-mcp-darwin-amd64" "$out/lastping-mcp-darwin-arm64")
chmod 0755 "$stage/server/lastping-mcp"
build windows amd64 "$stage/server/lastping-mcp.exe"

# The checked-in manifest carries a placeholder version; the tag decides.
node -e '
  const fs = require("fs");
  const [src, dst, v] = process.argv.slice(1);
  const m = JSON.parse(fs.readFileSync(src, "utf8"));
  m.version = v;
  fs.writeFileSync(dst, JSON.stringify(m, null, 2) + "\n");
' "$root/mcpb/manifest.json" "$stage/manifest.json" "$version"
cp "$root/mcpb/icon.png" "$stage/icon.png"

bundle="$out/lastping-$version.mcpb"
npx -y "$MCPB_CLI" validate "$stage/manifest.json"
npx -y "$MCPB_CLI" pack "$stage" "$bundle"

# Claude Desktop opens bundles with a strict zip reader, which rejects any
# byte after the end of central directory record that the record's comment
# length does not declare. Every bundle, signed or not, is checked for that
# rule and opened with yauzl, the same kind of reader, before it ships.
yauzl_dir="$(mktemp -d)"
trap 'rm -rf "$secrets" "$yauzl_dir"' EXIT
npm install --prefix "$yauzl_dir" --no-save --no-audit --no-fund --silent "$YAUZL" >/dev/null
strict_check() {
  node "$root/mcpb/zip.mjs" check "$1"
  node "$root/mcpb/zip.mjs" open "$1" "$yauzl_dir"
}

if (( ! sign )); then
  strict_check "$bundle"
  cp "$bundle" "$out/lastping.mcpb"
  echo "warning: MCPB_SIGNING_CERT and MCPB_SIGNING_KEY are not set; the bundle is UNSIGNED" >&2
  echo "built $bundle and $out/lastping.mcpb (unsigned)"
  exit 0
fi

sign_args=(--cert "${chain[0]}" --key "$secrets/key.pem")
if (( ${#chain[@]} > 1 )); then
  sign_args+=(--intermediate "${chain[@]:1}")
fi

# mcpb sign appends its signature block after the zip and leaves the zip's
# comment length at 0, so strict readers reject the result
# (modelcontextprotocol/mcpb#278). Declaring the block as the zip comment
# after signing would change the bytes the signature covers, so the length is
# declared first: a probe signature measures the block, the zip is rewritten
# with that comment length, and that zip is signed. Should a signature's
# block come out a different length from the one declared, the zip is
# declared again with the new length and signed again.
cp "$bundle" "$secrets/packed.zip"
cp "$bundle" "$secrets/probe.mcpb"
npx -y "$MCPB_CLI" sign "$secrets/probe.mcpb" "${sign_args[@]}"
size() { wc -c < "$1" | tr -d ' '; }
block_len=$(( $(size "$secrets/probe.mcpb") - $(size "$secrets/packed.zip") ))
signed=0
for attempt in 1 2 3; do
  node "$root/mcpb/zip.mjs" declare-comment "$secrets/packed.zip" "$secrets/unsigned.zip" "$block_len"
  cp "$secrets/unsigned.zip" "$bundle"
  npx -y "$MCPB_CLI" sign "$bundle" "${sign_args[@]}"
  got=$(( $(size "$bundle") - $(size "$secrets/unsigned.zip") ))
  if (( got == block_len )); then
    signed=1
    break
  fi
  echo "the signature block is $got bytes, not the declared $block_len; signing again (attempt $attempt)" >&2
  block_len=$got
done
if (( ! signed )); then
  echo "error: the signature block length did not settle after 3 attempts" >&2
  exit 1
fi

# Verify with openssl, not `mcpb verify`: the mcpb CLI cannot check a PKCS#7
# signature at all (its library does not implement it) and reports every
# bundle unsigned. The signed file must be exactly the zip that was signed
# (the packed zip with its comment length declared), then MCPB_SIG_V1, a
# uint32 little-endian length, that many bytes of DER, and MCPB_SIG_END. Split it at those boundaries and check each part.
node -e '
  const fs = require("fs");
  const [signed, unsigned, zipOut, sigOut] = process.argv.slice(1);
  const f = fs.readFileSync(signed), z = fs.readFileSync(unsigned);
  const head = Buffer.from("MCPB_SIG_V1"), foot = Buffer.from("MCPB_SIG_END");
  const fail = (m) => { console.error("error: " + m); process.exit(1); };
  if (f.length <= z.length + head.length + 4 + foot.length) fail("the bundle carries no signature block");
  if (!f.subarray(0, z.length).equals(z)) fail("signing changed the zip bytes");
  let o = z.length;
  if (!f.subarray(o, o + head.length).equals(head)) fail("the signature block has no MCPB_SIG_V1 header");
  o += head.length;
  const n = f.readUInt32LE(o);
  o += 4;
  if (o + n + foot.length !== f.length) fail("the signature length does not match the block");
  if (!f.subarray(o + n).equals(foot)) fail("the signature block has no MCPB_SIG_END footer");
  fs.writeFileSync(zipOut, f.subarray(0, z.length));
  fs.writeFileSync(sigOut, f.subarray(o, o + n));
' "$bundle" "$secrets/unsigned.zip" "$secrets/payload.zip" "$secrets/sig.p7"

if ! openssl cms -verify -binary -inform DER -in "$secrets/sig.p7" \
  -content "$secrets/payload.zip" -noverify \
  -signer "$secrets/signer.pem" -out /dev/null; then
  echo "error: the bundle's signature does not verify" >&2
  exit 1
fi
fingerprint() { openssl x509 -noout -fingerprint -sha256 -in "$1"; }
if [[ "$(fingerprint "$secrets/signer.pem")" != "$(fingerprint "${chain[0]}")" ]]; then
  echo "error: the bundle is signed by a certificate other than the first in MCPB_SIGNING_CERT" >&2
  exit 1
fi

if [[ "${MCPB_CERT_CA_ISSUED:-}" == "1" ]]; then
  ca_path="${MCPB_CA_PATH:-/etc/ssl/certs}"
  # cms defaults to the S/MIME signing purpose, which a code signing
  # certificate need not carry, and OpenSSL 3.0 (Ubuntu 24.04) has no
  # codesign purpose. So the chain is checked for any purpose here and the
  # Code Signing usage is checked on the certificate itself below.
  if ! openssl cms -verify -binary -inform DER -in "$secrets/sig.p7" \
    -content "$secrets/payload.zip" -CApath "$ca_path" -purpose any \
    -out /dev/null; then
    echo "error: the signing certificate does not chain to a CA in $ca_path" >&2
    exit 1
  fi
  if ! openssl x509 -noout -ext extendedKeyUsage -in "${chain[0]}" |
    grep -q 'Code Signing'; then
    echo "error: the signing certificate lacks the Code Signing extended key usage" >&2
    exit 1
  fi
  echo "signature verified; the certificate chains to $ca_path"
else
  echo "signature verified; the certificate's trust was not checked (MCPB_CERT_CA_ISSUED is not 1)"
fi

strict_check "$bundle"
cp "$bundle" "$out/lastping.mcpb"
echo "built $bundle and $out/lastping.mcpb (signed)"
