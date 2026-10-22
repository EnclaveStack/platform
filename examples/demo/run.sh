#!/usr/bin/env bash
# One command to a running EnclaveStack with sample data (L4-001 §2): the server, an admin account, an API
# token and a few resources of each kind. Run through `make demo`.
#   DRIVER=fake|docker  fake (default) simulates every resource; docker runs real containers
#   PORT=7070           where the console and API listen
#   DATA=<dir>          keep the data there. Default: a temporary folder removed on exit with the fake engine;
#                       ~/.enclave-demo with Docker, whose containers outlive the server
#   DRIVER=docker again picks up where the last run stopped.
# Ctrl-C stops the server.
set -euo pipefail
cd "$(dirname "$0")/../.."

driver=${DRIVER:-fake}
port=${PORT:-7070}
base="http://127.0.0.1:$port"
email=admin@enclave.local password=enclave-demo # the console's demo mode signs in with the same
bin=./build/enclave
[ -x "$bin" ] || { echo "build/enclave is missing: run make demo, which builds it" >&2; exit 1; }
if curl -fs "$base/healthz" >/dev/null 2>&1; then
  echo "Something already answers on $base. Stop it, or pass another port: make demo PORT=7171" >&2
  exit 1
fi

work=$(mktemp -d)
keep=${DATA:-}
[ "$driver" = fake ] || keep=${keep:-$HOME/.enclave-demo}
data=${keep:-$work/data}
"$bin" server --driver "$driver" --data-dir "$data" --listen "127.0.0.1:$port" \
  --setup-code-file "$work/setup-code" --log-format json >"$work/server.log" 2>&1 &
pid=$!
cleanup() {
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 0' INT TERM

for _ in $(seq 1 150); do
  curl -fs "$base/healthz" >/dev/null 2>&1 && break
  kill -0 "$pid" 2>/dev/null || { echo "The server stopped:" >&2; cat "$work/server.log" >&2; exit 1; }
  sleep 0.2
done
curl -fs "$base/healthz" >/dev/null || { echo "The server didn't start in 30 s:" >&2; cat "$work/server.log" >&2; exit 1; }

if curl -fs "$base/api/v1/setup" | grep -q '"required":true'; then
  # The server writes the code just after it starts answering.
  for _ in $(seq 1 50); do [ -s "$work/setup-code" ] && break; sleep 0.1; done
  [ -s "$work/setup-code" ] || { echo "The server didn't write its setup code" >&2; exit 1; }
else
  echo "EnclaveStack is running at $base with the data in $data, set up before."
  echo "Sign in as $email / $password if the demo made it, else with your own account."
  echo "Ctrl-C stops it."
  wait "$pid"
  exit 0
fi

# The admin account and a token for the CLI, as the console's setup page and `enclave login` would.
jar="$work/cookies"
curl -fsS -c "$jar" -o /dev/null -H 'Content-Type: application/json' \
  -d "{\"setupCode\":\"$(cat "$work/setup-code")\",\"machineName\":\"demo\",\"email\":\"$email\",\"password\":\"$password\"}" "$base/api/v1/setup"
token=$(curl -fsS -b "$jar" -H 'X-Enclave-CSRF: 1' -H 'Content-Type: application/json' \
  -d '{"name":"demo","scope":"write"}' "$base/api/v1/tokens" | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo "Couldn't create an API token" >&2; exit 1; }
export ENCLAVE_SERVER=$base ENCLAVE_TOKEN=$token

# Sample data. The fake engine is ready at once; real containers are left to start in the background.
wait=--wait
[ "$driver" = fake ] || wait=
step() { printf '  %-44s' "$1"; shift; if out=$("$@" 2>&1); then echo ok; else echo "failed"; echo "$out" | tail -3 | sed 's/^/    /'; fi; }
echo "Adding sample data:"
step "secret stripe-key" "$bin" secret create stripe-key --value sk_test_demo
step "database orders (PostgreSQL)" "$bin" db create orders $wait
step "cache sessions (Redis)" "$bin" cache create sessions $wait
step "bucket uploads" "$bin" bucket create uploads $wait
step "app api (nginx, using stripe-key)" "$bin" app create api --image nginx:1.27-alpine --port 80 --publish --secret-env STRIPE_KEY=stripe-key $wait
step "job nightly-report (02:00 every day)" "$bin" job create nightly-report --image alpine:3 --schedule "0 2 * * *" $wait -- echo hello
step "stack blog (WordPress from the catalogue)" "$bin" stack create blog --template wordpress --param domain=blog.localhost $wait
step "project shop" "$bin" project create shop
step "app storefront in project shop" "$bin" app create storefront --project shop --image nginx:1.27-alpine --port 80 $wait

cat <<EOF

EnclaveStack is running ($driver engine).
  Console   $base
  Sign in   $email / $password
  CLI       export ENCLAVE_SERVER=$base ENCLAVE_TOKEN=$token
            ./build/enclave ls
Ctrl-C stops it$([ -z "$keep" ] && echo " and removes the data" || echo "; the data stays in $data").
EOF
wait "$pid"
