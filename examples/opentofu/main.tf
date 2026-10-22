# A small stack on one EnclaveStack machine: a database, a cache, a secret and an app that uses all three.
#   make provider-dev && export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave
#   export ENCLAVE_SERVER=http://127.0.0.1:7070 ENCLAVE_TOKEN=hc_pat_...   # enclave token create --name opentofu
#   tofu init && tofu apply

terraform {
  required_providers {
    enclave = {
      source = "enclavestack/enclave"
    }
  }
}

provider "enclave" {}

variable "greeting" {
  type      = string
  sensitive = true
  default   = "hello from enclave"
}

data "enclave_machine" "this" {}

resource "enclave_database" "orders" {
  name    = "orders"
  version = "16"
}

resource "enclave_cache" "sessions" {
  name           = "sessions"
  max_memory_mib = 128
}

resource "enclave_secret" "greeting" {
  name  = "greeting"
  value = var.greeting
}

resource "enclave_app" "web" {
  name       = "web"
  image      = "busybox:1.36"
  port       = 8080
  publish    = true
  memory_mib = 64
  command    = ["sh", "-c", "mkdir -p /www && echo \"$GREETING (db at $DB_HOST)\" > /www/index.html && exec httpd -f -p 8080 -h /www"]
  env = {
    DB_HOST    = enclave_database.orders.internal_host
    CACHE_HOST = enclave_cache.sessions.internal_host
  }
  secret_env = {
    GREETING          = enclave_secret.greeting.name
    DATABASE_PASSWORD = enclave_database.orders.password_secret
  }
}

output "machine" {
  value = "${data.enclave_machine.this.name} (engine: ${data.enclave_machine.this.engine_driver})"
}

output "app_url" {
  value = "http://${enclave_app.web.host}:${enclave_app.web.host_port}/"
}

output "database" {
  value = "postgres://${enclave_database.orders.username}@${enclave_database.orders.host}:${enclave_database.orders.port}/${enclave_database.orders.database_name}"
}
