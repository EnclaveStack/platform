# Public access (L2-014): reach an app on this machine from any device, with nothing opened on the router.
# Run with: make tofu DIR=examples/opentofu/public-access
#
# By default this makes a quick link (a random https://….trycloudflare.com address, no account needed)
# behind EnclaveStack's sign-in. Set use_cloudflare = true, with your Cloudflare details, to serve
# stage.<your domain> through a Cloudflare tunnel instead.

terraform {
  required_providers {
    enclave = { source = "hashicorp/enclave" }
  }
}

variable "use_cloudflare" {
  type    = bool
  default = false
}

variable "domain" {
  type    = string
  default = "example.com"
}

variable "cloudflare_api_token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "cloudflare_account_id" {
  type    = string
  default = ""
}

resource "enclave_app" "web" {
  name  = "web"
  image = "nginx:1.27-alpine"
  port  = 80
}

# A quick link: anyone with the address meets the sign-in page first.
resource "enclave_tunnel" "demo" {
  name            = "demo"
  tunnel_provider = "quick"
  app             = enclave_app.web.name
}

# Your own domain through Cloudflare: the zone writes the record, the tunnel carries the visitors.
resource "enclave_secret" "cloudflare" {
  count = var.use_cloudflare ? 1 : 0
  name  = "cloudflare-token"
  value = jsonencode({ apiToken = var.cloudflare_api_token })
}

resource "enclave_zone" "main" {
  count              = var.use_cloudflare ? 1 : 0
  name               = "main"
  dns_provider       = "cloudflare"
  zone               = var.domain
  credentials_secret = enclave_secret.cloudflare[0].name
}

resource "enclave_tunnel" "home" {
  count              = var.use_cloudflare ? 1 : 0
  name               = "home"
  tunnel_provider    = "cloudflare"
  credentials_secret = enclave_secret.cloudflare[0].name
  account_id         = var.cloudflare_account_id
}

resource "enclave_domain" "stage" {
  count    = var.use_cloudflare ? 1 : 0
  name     = "stage"
  hostname = "stage.${var.domain}"
  app      = enclave_app.web.name
  zone     = enclave_zone.main[0].name
  tunnel   = enclave_tunnel.home[0].name
  gate     = { mode = "signin" }
}

output "quick_link" {
  value = enclave_tunnel.demo.url
}

output "stage_url" {
  value = var.use_cloudflare ? enclave_domain.stage[0].url : null
}
