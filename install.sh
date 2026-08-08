#!/bin/sh
# Install the `lastping` CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/tp322d/lastping-app/main/install.sh | sh
#
# Deliberately POSIX sh with no dependencies beyond curl (or wget) and tar:
# this runs on machines that do not have Go, and often inside a CI image that
# has almost nothing. Anything clever here is a way to fail on somebody's
# box at the exact moment they are deciding whether the product is worth it.
set -eu

REPO="tp322d/lastping-app"
BIN="lastping"

# Install somewhere on PATH without needing root where possible. ~/.local/bin
# is on PATH by default on most modern distros and is the least surprising
# place to put a user-scoped binary.
INSTALL_DIR="${LASTPING_INSTALL_DIR:-$HOME/.local/bin}"

say()  { printf '%s\n' "$*"; }
die()  { printf 'install: %s\n' "$*" >&2; exit 1; }

fetch() {
  # $1 url, $2 output path
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "needs curl or wget"
  fi
}

os=$(uname -s)
case "$os" in
  Linux)   os=linux  ;;
  Darwin)  os=darwin ;;
  *) die "unsupported OS: $os. Windows users: download a .zip from https://github.com/$REPO/releases" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

# Resolve the newest tag from the redirect on /releases/latest rather than
# parsing the API: no token, no rate limit, no jq.
say "Finding the latest release..."
latest_url=$(
  if command -v curl >/dev/null 2>&1; then
    curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest"
  else
    wget -q -S -O /dev/null "https://github.com/$REPO/releases/latest" 2>&1 |
      sed -n 's/.*Location: *//p' | tail -1
  fi
) || die "could not reach GitHub"

version=${latest_url##*/}
[ -n "$version" ] || die "could not determine the latest version"
# Tags are v-prefixed; archive names are not.
num=${version#v}

tmp=$(mktemp -d)
# shellcheck disable=SC2064
trap "rm -rf '$tmp'" EXIT INT TERM

archive="${BIN}_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

say "Downloading $BIN $version ($os/$arch)..."
fetch "$base/$archive" "$tmp/$archive" || die "no build for $os/$arch in $version"

# Verify against the published checksums when they are available. Not fatal if
# absent -- a missing sums file should not block an install -- but a MISMATCH
# always is.
if fetch "$base/${BIN}_${num}_SHA256SUMS" "$tmp/sums" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    have=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
  elif command -v shasum >/dev/null 2>&1; then
    have=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
  else
    have=""
  fi
  if [ -n "$have" ]; then
    want=$(grep " $archive\$" "$tmp/sums" | cut -d' ' -f1 || true)
    [ -n "$want" ] || want=$(grep "$archive" "$tmp/sums" | cut -d' ' -f1 || true)
    if [ -n "$want" ] && [ "$have" != "$want" ]; then
      die "checksum mismatch for $archive -- refusing to install"
    fi
    [ -n "$want" ] && say "Checksum verified."
  fi
fi

tar -xzf "$tmp/$archive" -C "$tmp" || die "could not unpack $archive"
[ -f "$tmp/$BIN" ] || die "archive did not contain $BIN"

mkdir -p "$INSTALL_DIR"
mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
chmod +x "$INSTALL_DIR/$BIN"

say ""
say "Installed $BIN to $INSTALL_DIR/$BIN"

# Say so plainly if it will not be found, rather than letting the next command
# fail with 'command not found' and no explanation.
case ":${PATH}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    say ""
    say "$INSTALL_DIR is not on your PATH. Add it:"
    say "    export PATH=\"\$PATH:$INSTALL_DIR\""
    ;;
esac

say ""
say "Wrap anything that should report when it runs:"
say "    lastping run --monitor <monitor-id> -- your-command"
