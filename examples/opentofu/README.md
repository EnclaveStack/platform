# OpenTofu example

`main.tf` declares a database, a cache, a secret and an app that uses all three, on one EnclaveStack machine.

One command does all of the steps below (build the provider, point OpenTofu at it, sign in once, keep a token) and then runs `tofu`. Start EnclaveStack first, in another terminal, with `make start`:

```bash
make tofu CMD=plan          # shows what would be created
make tofu                   # applies it (CMD=apply is the default); then: curl "$(tofu -chdir=examples/opentofu output -raw app_url)"
make tofu CMD=destroy       # removes it
make tofu DIR=path/to/your/tf CMD=apply ARGS=-auto-approve   # your own files
```

The first run asks for your email and password once and saves a token in `~/.enclave-opentofu-token`. The same thing by hand:

```bash
make provider-dev                                    # from the repo root: builds ./build and writes a dev override
export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave
export ENCLAVE_SERVER=http://127.0.0.1:7070
export ENCLAVE_TOKEN=$(enclave token create --name opentofu)
cd examples/opentofu
tofu plan      # 4 to add
tofu apply     # waits until every resource is ready
curl "$(tofu output -raw app_url)"
tofu destroy
```

With a dev override `tofu init` is not needed (OpenTofu prints a warning that the override is in use — expected).
