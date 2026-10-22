#!/bin/bash
# Installs every tracked hook from .githooks/ into .git/hooks/. Run once per clone.
# Installs ALL tracked hooks rather than naming them, so adding a hook can never ship one nobody installed.
set -euo pipefail

ROOT_DIR=$(git rev-parse --show-toplevel)
HOOK_DIR="$ROOT_DIR/.githooks"
[ -d "$HOOK_DIR" ] || { echo "[hooks] Missing tracked hook directory: $HOOK_DIR"; exit 1; }

INSTALLED=0
for SRC in "$HOOK_DIR"/*; do
  [ -f "$SRC" ] || continue
  NAME=$(basename "$SRC")
  DEST="$ROOT_DIR/.git/hooks/$NAME"
  mkdir -p "$(dirname "$DEST")"
  if [ -f "$DEST" ] && ! cmp -s "$SRC" "$DEST"; then
    cp "$DEST" "$DEST.enclave.backup"
    echo "[hooks] Backed up existing $NAME hook to $DEST.enclave.backup"
  fi
  cp "$SRC" "$DEST"
  chmod +x "$DEST"
  echo "[hooks] Installed $NAME"
  INSTALLED=$((INSTALLED + 1))
done
[ "$INSTALLED" -gt 0 ] || { echo "[hooks] No hooks found in $HOOK_DIR"; exit 1; }
echo "[hooks] Installed $INSTALLED hook(s)"
