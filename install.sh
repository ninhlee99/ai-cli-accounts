#!/usr/bin/env bash
# am installer — builds from source, no manual git clone needed.
#
#   curl -fsSL https://raw.githubusercontent.com/ninhlee99/amux/main/install.sh | sh
#
# Needs: macOS (am uses the `security` keychain CLI), Go 1.22+, git.
set -euo pipefail

REPO_URL="${AM_REPO_URL:-https://github.com/ninhlee99/amux.git}"

if [ -z "${AM_INSTALL_DIR:-}" ]; then
  if [ -d "$HOME/.local/bin" ] && [ -w "$HOME/.local/bin" ] && [[ ":$PATH:" == *":$HOME/.local/bin:"* ]]; then
    INSTALL_DIR="$HOME/.local/bin"
  elif [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
else
  INSTALL_DIR="$AM_INSTALL_DIR"
fi

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "am is macOS-only (uses the 'security' keychain CLI)."
command -v git >/dev/null 2>&1 || die "git is required. Install it, then re-run."
command -v go  >/dev/null 2>&1 || die "Go 1.22+ is required. Install it (https://go.dev/dl/), then re-run."

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT
mkdir -p "$tmp/amux-accounts"

if [ -f "./main.go" ] && [ -f "./go.mod" ]; then
  say "building am from local source..."
  go build -o "$tmp/amux-accounts/am" . || die "build failed."
else
  say "cloning $REPO_URL..."
  git clone --depth 1 "$REPO_URL" "$tmp/amux-accounts/src" >/dev/null 2>&1 \
    || die "clone failed — check the URL and your network."

  say "building am..."
  ( cd "$tmp/amux-accounts/src" && go build -o "$tmp/amux-accounts/am" . ) \
    || die "build failed."
fi

if [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/amux-accounts/am" "$INSTALL_DIR/am"
  ln -sf "$INSTALL_DIR/am" "$INSTALL_DIR/amux"
else
  say "need sudo to write to $INSTALL_DIR..."
  sudo mv "$tmp/amux-accounts/am" "$INSTALL_DIR/am"
  sudo ln -sf "$INSTALL_DIR/am" "$INSTALL_DIR/amux"
fi

if [ "$INSTALL_DIR" != "/usr/local/bin" ] && [ -w "/usr/local/bin/am" ]; then
  cp "$INSTALL_DIR/am" "/usr/local/bin/am" 2>/dev/null || true
fi

say "installed am & amux -> $INSTALL_DIR"

AUTO_UPDATE_FLAG=""
for arg in "$@"; do
  if [ "$arg" = "--auto-update" ] || [ "$arg" = "-u" ]; then
    AUTO_UPDATE_FLAG="--auto-update"
  fi
done
if [ "${AM_AUTO_UPDATE:-}" = "1" ]; then
  AUTO_UPDATE_FLAG="--auto-update"
fi

if command -v am >/dev/null 2>&1; then
  say "running 'am setup $AUTO_UPDATE_FLAG' (hook + /am:feedback slash command)..."
  am setup $AUTO_UPDATE_FLAG || say "am setup reported an issue — you can re-run it any time: am setup $AUTO_UPDATE_FLAG"
else
  say "warning: $INSTALL_DIR isn't on PATH — add it, then run: am setup $AUTO_UPDATE_FLAG"
fi

say ""
say "done. Next: am add   (save your first account)"
