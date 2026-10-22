#!/usr/bin/env bash
# Runs the k6 load test against a freshly started `enclave server --driver fake` (L5-003 §2).
# Usage: tests/load/run.sh [extra k6 args]   (DURATION=60s, READ_VUS=50, WRITE_VUS=10 by default; CI uses fewer, L5-003 §2)
set -euo pipefail
cd "$(dirname "$0")/../.."
command -v k6 >/dev/null || { echo "k6 is not installed (brew install k6)" >&2; exit 1; }
work=$(mktemp -d)
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; rm -rf "$work"' EXIT
go build -o "$work/enclave" ./cmd/enclave
port=${PORT:-7171}
# The load generator is one client IP, so the per-client limit is raised for the run.
"$work/enclave" server --driver fake --fake-step-delay 0 --data-dir "$work/data" --listen "127.0.0.1:$port" \
  --rate-limit 100000 --log-level warn --log-format json >"$work/out" 2>"$work/server.log" &
pid=$!
for _ in $(seq 1 100); do grep -q '^Console:' "$work/out" 2>/dev/null && break; sleep 0.1; done
code=$(grep -Eo '[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}' "$work/out" | head -1)
[ -n "$code" ] || { echo "server did not print a setup code" >&2; cat "$work/server.log" >&2; exit 1; }
k6 run -e BASE_URL="http://127.0.0.1:$port" -e SETUP_CODE="$code" -e DURATION="${DURATION:-60s}" \
  -e READ_VUS="${READ_VUS:-50}" -e WRITE_VUS="${WRITE_VUS:-10}" "$@" tests/load/api.js
status=$?
# macOS RSS counts pages Go already returned to the OS; the physical footprint is the real charge.
if [ "$(uname)" = "Darwin" ]; then
  echo "server physical footprint after load: $(vmmap --summary "$pid" 2>/dev/null | awk -F': *' '/Physical footprint:/ {print $2; exit}')"
else
  echo "server RSS after load: $(( $(ps -o rss= -p "$pid" | tr -d ' ') / 1024 )) MiB"
fi
if grep -q '"level":"ERROR"' "$work/server.log"; then echo "server logged errors:" >&2; grep '"level":"ERROR"' "$work/server.log" | head >&2; exit 1; fi
exit $status
