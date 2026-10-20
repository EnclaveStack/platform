#!/usr/bin/env bash
# Stable entry point for the Statutory Integrity gate (CI and humans call this; the logic is in the .mjs).
set -euo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || dirname "$0"/..)"
exec node bin/statutory-integrity.mjs "$@"
