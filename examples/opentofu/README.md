# OpenTofu example

`main.tf` declares a database, a cache, a secret and an app that uses all three, on one EnclaveStack machine.

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
