#!/bin/sh
# codexfleet installer: builds the CLI, installs the Claude skill and the
# Claude Code plugin.
#
#   curl -fsSL https://raw.githubusercontent.com/Nano112/codexfleet/main/install.sh | sh
#   git clone https://github.com/Nano112/codexfleet && ./codexfleet/install.sh
#
# Options (env): CODEXFLEET_SRC (checkout dir), CODEXFLEET_BIN (install dir),
# CODEXFLEET_REF (git ref, default main), NO_PLUGIN=1, NO_SKILL=1.
set -eu

REPO="Nano112/codexfleet"
SRC="${CODEXFLEET_SRC:-$HOME/.local/share/codexfleet/src}"
REF="${CODEXFLEET_REF:-main}"
SKILL_DIR="$HOME/.claude/skills/codexfleet"

say() { printf '==> %s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

have git || die "git is required"
have go || die "Go is required (https://go.dev/dl/), version 1.27 or newer, or GOTOOLCHAIN=auto"

# 1. Get the source. Prefer the checkout this script lives in.
here=""
case "$0" in */install.sh) here=$(cd "$(dirname "$0")" && pwd) ;; esac
if [ -n "$here" ] && [ -f "$here/go.mod" ] && grep -q "module github.com/$REPO" "$here/go.mod"; then
  SRC="$here"
  say "using checkout $SRC"
elif [ -d "$SRC/.git" ]; then
  say "updating $SRC"
  # Managed checkout: follow the remote exactly (also survives history rewrites).
  if git -C "$SRC" fetch -q --force --tags origin; then
    git -C "$SRC" checkout -q --detach "origin/$REF" 2>/dev/null || git -C "$SRC" checkout -q --detach "$REF" ||
      say "could not check out $REF; using the existing checkout"
  else
    say "update failed; using the existing checkout"
  fi
else
  mkdir -p "$(dirname "$SRC")"
  say "cloning https://github.com/$REPO"
  if ! GIT_TERMINAL_PROMPT=0 git clone -q "https://github.com/$REPO.git" "$SRC"; then
    rm -rf "$SRC"
    if have gh && gh auth status >/dev/null 2>&1; then
      say "https clone failed; retrying with gh"
      gh repo clone "$REPO" "$SRC" -- -q
    else
      die "clone failed (network?); try: gh repo clone $REPO $SRC && $SRC/install.sh"
    fi
  fi
  [ "$REF" = main ] || git -C "$SRC" checkout -q --detach "$REF"
fi

# 2. Build and install the binary.
BIN="${CODEXFLEET_BIN:-$HOME/.local/bin}"
mkdir -p "$BIN"
VERSION=$(git -C "$SRC" describe --tags --always 2>/dev/null || echo dev)
say "building codexfleet $VERSION -> $BIN/codexfleet"
# Keep Go's scratch files off /tmp (often a small tmpfs).
GOTMPDIR="${GOTMPDIR:-$HOME/.cache/codexfleet/go-tmp}"
mkdir -p "$GOTMPDIR"
export GOTMPDIR
(cd "$SRC" && GOFLAGS=-mod=mod go build -ldflags "-X main.version=$VERSION" -o "$BIN/codexfleet" ./cmd/codexfleet)
case ":$PATH:" in *":$BIN:"*) ;; *) say "NOTE: add $BIN to your PATH" ;; esac

# 3. Install the skill for any Claude session.
if [ "${NO_SKILL:-}" != 1 ]; then
  mkdir -p "$SKILL_DIR"
  cp "$SRC/skills/codexfleet/SKILL.md" "$SKILL_DIR/SKILL.md"
  say "skill installed: $SKILL_DIR/SKILL.md"
fi

# 4. Install the Claude Code plugin (pane, status line, /fleet), read from the
#    local checkout: no extra credentials, and `git pull` + /reload-plugins updates it.
if [ "${NO_PLUGIN:-}" != 1 ]; then
  if have claude; then
    claude plugin marketplace add "$SRC" >/dev/null 2>&1 || claude plugin marketplace update codexfleet >/dev/null 2>&1 || true
    if claude plugin install codexfleet@codexfleet >/dev/null 2>&1 || claude plugin list 2>/dev/null | grep -q codexfleet; then
      say "Claude Code plugin installed (codexfleet@codexfleet; run /reload-plugins in open sessions)"
    else
      say "could not install the Claude Code plugin; in Claude Code run: /plugin install codexfleet --marketplace $SRC"
    fi
    if [ "$BIN/codexfleet" != "$(command -v codexfleet 2>/dev/null)" ]; then
      say "NOTE: the plugin runs 'codexfleet' from PATH; put $BIN on PATH or set the plugin's binary option to $BIN/codexfleet"
    fi
  else
    say "claude CLI not found; skipping the plugin"
  fi
fi

"$BIN/codexfleet" --version
say "done. Next: cd <your repo> && codexfleet init && codexfleet doctor"
