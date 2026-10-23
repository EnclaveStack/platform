#!/usr/bin/env bash
# One command to run OpenTofu against your EnclaveStack (L4-001 §2): builds the provider, points OpenTofu at it,
# signs in once, keeps an API token for OpenTofu, then runs `tofu` in a folder. Run through `make tofu`.
#   CMD=apply           the tofu command: plan, apply, destroy, output …
#   DIR=examples/opentofu   the folder with the .tf files
#   ARGS=               extra flags for tofu, e.g. ARGS=-auto-approve
#   ENCLAVE_SERVER=http://127.0.0.1:7070   where EnclaveStack runs (start it with make start)
set -euo pipefail
cd "$(dirname "$0")/../.."

server=${ENCLAVE_SERVER:-http://127.0.0.1:7070}
cmd=${CMD:-apply}
dir=${DIR:-examples/opentofu}
tokenfile=$HOME/.enclave-opentofu-token
command -v tofu >/dev/null || { echo "OpenTofu isn't installed: brew install opentofu" >&2; exit 1; }
curl -fs "$server/healthz" >/dev/null 2>&1 || { echo "Nothing answers at $server. Start EnclaveStack first, in another terminal: make start" >&2; exit 1; }

echo "Building the OpenTofu provider…"
mkdir -p build
go build -ldflags '-s -w' -o build/terraform-provider-enclave ./cmd/terraform-provider-enclave
[ -x build/enclave ] || go build -tags embedconsole -o build/enclave ./cmd/enclave 2>/dev/null || go build -o build/enclave ./cmd/enclave
printf 'provider_installation {\n  dev_overrides {\n    "enclavestack/enclave" = "%s"\n  }\n  direct {}\n}\n' "$PWD/build" > "$HOME/.tofurc.enclave"
export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave ENCLAVE_SERVER=$server

# A saved token that still works is reused; otherwise sign in (asked once) and make a new one.
token=$(cat "$tokenfile" 2>/dev/null || true)
if [ -z "$token" ] || ! ENCLAVE_TOKEN=$token ./build/enclave whoami >/dev/null 2>&1; then
  echo "Signing in to $server to make a token for OpenTofu (you're asked for your email and password once)."
  ./build/enclave login
  token=$(./build/enclave token create --name opentofu)
  [ -n "$token" ] || { echo "Couldn't create an API token" >&2; exit 1; }
  (umask 077; printf '%s\n' "$token" > "$tokenfile")
fi
export ENCLAVE_TOKEN=$token

echo "Running: tofu $cmd in $dir"
# shellcheck disable=SC2086
status=0
tofu -chdir="$dir" "$cmd" ${ARGS:-} || status=$?

# After a destroy that finished and left nothing managed, the local state is an empty notebook and its backup
# holds the old values (passwords): remove both. A failed or partial destroy keeps them: they are the only
# record of what is left.
if [ "$cmd" = destroy ] && [ "$status" -eq 0 ] && [ -f "$dir/terraform.tfstate" ] \
  && [ -z "$(tofu -chdir="$dir" state list 2>/dev/null)" ]; then
  rm -f "$dir/terraform.tfstate" "$dir/terraform.tfstate.backup"
  echo "Removed the empty OpenTofu state files in $dir."
fi
exit "$status"
