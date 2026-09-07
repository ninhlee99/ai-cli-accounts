#!/usr/bin/env bash
# am installer — builds from source, no manual git clone needed.
#
#   curl -fsSL https://raw.githubusercontent.com/ninhlee99/ai-cli-accounts/main/install.sh | sh
#
# Needs: macOS (am uses the `security` keychain CLI), Go 1.22+, git.
set -euo pipefail

REPO_URL="https://github.com/ninhlee99/ai-cli-accounts.git"
INSTALL_DIR="${AM_INSTALL_DIR:-/usr/local/bin}"

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "am is macOS-only (uses the 'security' keychain CLI)."
command -v git >/dev/null 2>&1 || die "git is required. Install it, then re-run."
command -v go  >/dev/null 2>&1 || die "Go 1.22+ is required. Install it (https://go.dev/dl/), then re-run."

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

say "cloning $REPO_URL..."
git clone --depth 1 "$REPO_URL" "$tmp/ai-cli-accounts" >/dev/null 2>&1 \
  || die "clone failed — check the URL and your network."

say "building am..."
( cd "$tmp/ai-cli-accounts" && go build -o am . ) \
  || die "build failed."

if [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/ai-cli-accounts/am" "$INSTALL_DIR/am"
else
  say "need sudo to write to $INSTALL_DIR..."
  sudo mv "$tmp/ai-cli-accounts/am" "$INSTALL_DIR/am"
fi

say "installed am -> $INSTALL_DIR/am"

if command -v am >/dev/null 2>&1; then
  say "running 'am setup' (hook + /am:feedback slash command)..."
  am setup || say "am setup reported an issue — you can re-run it any time: am setup"
else
  say "warning: $INSTALL_DIR isn't on PATH — add it, then run: am setup"
fi

say ""
say "done. Next: am add   (save your first account)"
