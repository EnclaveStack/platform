#!/bin/bash
# Secret scan. `staged` (default) scans the index; `all` scans the working tree.
set -euo pipefail
MODE="${1:-staged}"
if ! command -v gitleaks >/dev/null 2>&1; then
  echo "[gitleaks] gitleaks is required but not installed. Install it, e.g.: brew install gitleaks"
  exit 1
fi
if gitleaks help 2>&1 | grep -q "protect"; then
  [ "$MODE" = "staged" ] && exec gitleaks protect --staged --redact
  exec gitleaks detect --source . --no-git --redact
fi
if gitleaks help 2>&1 | grep -q "git"; then
  [ "$MODE" = "staged" ] && exec gitleaks git --staged --redact
  exec gitleaks dir . --redact
fi
echo "[gitleaks] Unsupported gitleaks CLI version (expected 'protect' or 'git' commands)."
exit 1
