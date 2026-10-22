#!/usr/bin/env bash
# Zero-drop deploys behind a load balancer on real Docker (L2-006 LB-06, L5-002 §7): starts
# `enclave server` on the Docker driver, puts a 3-replica app behind a published load balancer,
# runs tests/load/lb.js through it and redeploys the app DEPLOYS times meanwhile. Cleans up after itself.
# Usage: tests/load/lb.sh   (DURATION=60s, RATE=200, DEPLOYS=3)
set -euo pipefail
cd "$(dirname "$0")/../.."
command -v k6 >/dev/null || { echo "k6 is not installed (brew install k6)" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker is not running" >&2; exit 1; }
work=$(mktemp -d)
port=${PORT:-7172}
suffix=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
app="load-web-$suffix" lb="load-lb-$suffix"
go build -o "$work/enclave" ./cmd/enclave
"$work/enclave" server --data-dir "$work/data" --listen "127.0.0.1:$port" --log-level warn --log-format json \
  --setup-code-file "$work/code" >"$work/out" 2>"$work/server.log" &
pid=$!
export ENCLAVE_CONFIG="$work/cfg.json" ENCLAVE_SERVER="http://127.0.0.1:$port"
hc() { "$work/enclave" "$@"; }
cleanup() {
  hc lb delete "$lb" --yes --wait >/dev/null 2>&1 || true
  hc app delete "$app" --yes --wait >/dev/null 2>&1 || true
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
for _ in $(seq 1 100); do [ -s "$work/code" ] && break; sleep 0.1; done
curl -fsS -X POST -H 'Content-Type: application/json' "$ENCLAVE_SERVER/api/v1/setup" -o /dev/null \
  -d "{\"setupCode\":\"$(cat "$work/code")\",\"machineName\":\"load\",\"email\":\"load@example.com\",\"password\":\"load-password-123\"}"
printf 'load@example.com\nload-password-123\n' | hc login >/dev/null
hc app create "$app" --image traefik/whoami:v1.11 --port 80 --replicas 3 --memory 64 --wait >/dev/null
hc lb create "$lb" --port 80 --target "$app" --health http:/ --publish --wait >/dev/null
lb_port=$(hc lb get "$lb" | awk '$1 == "port" { print $2 }')
echo "load balancer on 127.0.0.1:$lb_port; $DEPLOYS redeploys during ${DURATION:-60s} at ${RATE:-200} req/s"
k6 run -q -e LB_URL="http://127.0.0.1:$lb_port/" -e DURATION="${DURATION:-60s}" -e RATE="${RATE:-200}" tests/load/lb.js >"$work/k6.log" 2>&1 &
k6pid=$!
sleep 5
for i in $(seq 1 "${DEPLOYS:-3}"); do
  hc app redeploy "$app" --wait >/dev/null
  echo "redeploy $i done"
done
status=0
wait "$k6pid" || status=$?
grep -E "http_req_failed|http_reqs|checks" "$work/k6.log" || cat "$work/k6.log"; grep -o "FAIL [^\"]*" "$work/k6.log" | sed "s/ t=.*//" | sort | uniq -c | sort -rn | head
if grep -q '"level":"ERROR"' "$work/server.log"; then echo "server logged errors:" >&2; grep '"level":"ERROR"' "$work/server.log" | head >&2; exit 1; fi
exit $status
