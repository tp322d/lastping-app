#!/usr/bin/env bash
# Builds the signed Claude Desktop extension, dist/mcpb/lastping-<version>.mcpb,
# and a copy named lastping.mcpb (the version-less name the download link on
# lastping.dev points at through releases/latest/download).
#
#   MCPB_SIGNING_CERT='<PEM>' MCPB_SIGNING_KEY='<PEM>' mcpb/build.sh v0.2.0
#
# MCPB_SIGNING_CERT may also carry intermediate certificates after the signing
# certificate; they are passed to `mcpb sign` as the chain.
#
# The bundle is signed or it is not built: with either secret missing the
# script exits 1 before compiling anything, so no path through it produces an
# unsigned bundle for the release job to upload. To try it locally, make a
# throwaway pair with openssl and pass it the same way.
#
# Needs Go and Node.js (npx). Runs on Linux or macOS.
set -euo pipefail

MCPB_CLI='@anthropic-ai/mcpb@2.1.2'
MAKEFAT='github.com/randall77/makefat@v0.0.0-20260406194835-1b91746796b7'

version="${1:-${VERSION:-}}"
version="${version#v}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: mcpb/build.sh <version, e.g. v0.2.0>" >&2
  exit 1
fi

if [[ -z "${MCPB_SIGNING_CERT:-}" || -z "${MCPB_SIGNING_KEY:-}" ]]; then
  echo "error: MCPB_SIGNING_CERT and MCPB_SIGNING_KEY must both be set; an unsigned bundle is never built" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/mcpb"
stage="$out/stage"
rm -rf "$out"
mkdir -p "$stage/server"

secrets="$(mktemp -d)"
trap 'rm -rf "$secrets"' EXIT
(
  umask 077
  # One file per certificate: the first signs, the rest are the chain.
  printf '%s\n' "$MCPB_SIGNING_CERT" | awk -v dir="$secrets" '
    /-----BEGIN CERTIFICATE-----/ { n++ }
    n { print > (dir "/cert-" n ".pem") }'
  printf '%s\n' "$MCPB_SIGNING_KEY" > "$secrets/key.pem"
)
chain=("$secrets"/cert-*.pem)
if [[ ! -f "${chain[0]}" ]]; then
  echo "error: MCPB_SIGNING_CERT holds no PEM certificate" >&2
  exit 1
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
sign_args=(--cert "$secrets/cert-1.pem" --key "$secrets/key.pem")
if (( ${#chain[@]} > 1 )); then
  sign_args+=(--intermediate "${chain[@]:1}")
fi
npx -y "$MCPB_CLI" sign "$bundle" "${sign_args[@]}"
# `mcpb verify` passes only when the certificate chains to the OS trust store.
# A self-signed certificate signs correctly but fails that check, so it is
# accepted only when MCPB_ALLOW_UNTRUSTED_CERT=1 says the owner chose one, and
# then the signature block must still be on the end of the file.
if ! npx -y "$MCPB_CLI" verify "$bundle"; then
  if [[ "${MCPB_ALLOW_UNTRUSTED_CERT:-}" != "1" ]]; then
    echo "error: the bundle's signature does not verify against a trusted certificate" >&2
    exit 1
  fi
  if [[ "$(tail -c 12 "$bundle")" != "MCPB_SIG_END" ]]; then
    echo "error: the bundle carries no signature block" >&2
    exit 1
  fi
  echo "warning: signed with a certificate the OS does not trust (MCPB_ALLOW_UNTRUSTED_CERT=1)" >&2
fi
cp "$bundle" "$out/lastping.mcpb"

echo "built $bundle and $out/lastping.mcpb"
