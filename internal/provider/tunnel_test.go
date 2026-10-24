package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-003 TF-51 – TF-53) with real OpenTofu: app links, a Cloudflare tunnel with a public
// domain and Cloudflare Access, and an edge machine.

const accountID = "0123456789abcdef0123456789abcdef"

func publicConfig(gate, access string) string {
	return fmt.Sprintf(`
resource "enclave_app" "web" {
  name  = "web"
  image = "nginx:1.27-alpine"
  port  = 80
}
resource "enclave_secret" "stage_pass" {
  name  = "stage-pass"
  value = jsonencode({ username = "team", password = "correct horse" })
}
resource "enclave_secret" "cf" {
  name  = "cf-token"
  value = jsonencode({ apiToken = "t" }) # one secret serves the zone and the tunnel
}
resource "enclave_tunnel" "demo" {
  name            = "demo"
  tunnel_provider = "quick"
  app             = enclave_app.web.name
  %s
}
resource "enclave_zone" "example" {
  name               = "example"
  dns_provider       = "cloudflare"
  zone               = "example.com"
  credentials_secret = enclave_secret.cf.name
}
resource "enclave_tunnel" "home" {
  name               = "home"
  tunnel_provider    = "cloudflare"
  credentials_secret = enclave_secret.cf.name
  account_id         = %q
}
resource "enclave_domain" "stage" {
  name     = "stage"
  hostname = "stage.example.com"
  app      = enclave_app.web.name
  zone     = enclave_zone.example.name
  tunnel   = enclave_tunnel.home.name
  %s
}
`, gate, accountID, access)
}

func TestAccTunnelsAndPublicDomains(t *testing.T) {
	env, c := boot(t)
	env.DNS.AddZone("example.com")
	env.Cloudflare.AddZone("example.com", accountID)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: publicConfig("", `cloudflare_access = { emails = ["sunny@example.com"] }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("enclave_tunnel.demo", "url", regexp.MustCompile(`^https://fake-.+\.trycloudflare\.com$`)),
					resource.TestCheckResourceAttr("enclave_tunnel.demo", "gate_mode", api.GateSignin),
					resource.TestCheckNoResourceAttr("enclave_tunnel.demo", "gate"),
					resource.TestMatchResourceAttr("enclave_tunnel.home", "tunnel_id", regexp.MustCompile(`.+`)),
					resource.TestCheckResourceAttr("enclave_domain.stage", "public", "true"),
					resource.TestCheckResourceAttr("enclave_domain.stage", "url", "https://stage.example.com"),
					resource.TestCheckResourceAttr("enclave_domain.stage", "dns_record.type", "CNAME"),
					resource.TestCheckResourceAttr("enclave_domain.stage", "dns_record.state", "created"),
					resource.TestCheckResourceAttr("enclave_domain.stage", "cloudflare_access.emails.0", "sunny@example.com"),
					func(*terraform.State) error {
						if _, ok := env.Cloudflare.AccessApps()["stage.example.com"]; !ok {
							return fmt.Errorf("no Access application: %+v", env.Cloudflare.AccessApps())
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_tunnel.demo",
				ImportState:             true,
				ImportStateId:           "demo",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "url"}, // a quick link's address is whatever it is now
			},
			{
				// The gate changes in place; dropping cloudflare_access removes the Access application.
				Config: publicConfig(`gate = { mode = "password", password_secret = enclave_secret.stage_pass.name, open_paths = ["/webhooks/"] }`,
					`gate = { mode = "signin" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_tunnel.demo", "gate_mode", api.GatePassword),
					resource.TestCheckResourceAttr("enclave_tunnel.demo", "gate.open_paths.0", "/webhooks/"),
					resource.TestCheckResourceAttr("enclave_domain.stage", "gate.mode", api.GateSignin),
					resource.TestCheckNoResourceAttr("enclave_domain.stage", "cloudflare_access"),
					func(*terraform.State) error {
						if n := len(env.Cloudflare.AccessApps()); n != 0 {
							return fmt.Errorf("%d Access applications left", n)
						}
						return nil
					},
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			items, err := c.ListAllResources(context.Background(), api.KindTunnel)
			if err != nil {
				return err
			}
			for _, r := range items {
				if r.Status.Phase != api.PhaseDeleting {
					return fmt.Errorf("tunnel %s survived destroy", r.Name)
				}
			}
			if rec := env.DNS.Lookup("stage.example.com", "CNAME"); rec != nil {
				return fmt.Errorf("the domain's record survived destroy: %+v", rec)
			}
			return nil
		},
	})
}

func TestAccEdgeMachine(t *testing.T) {
	boot(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `resource "enclave_machine" "vps" {
  name    = "vps1"
  role    = "edge"
  address = "203.0.113.9"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_machine.vps", "role", api.MachineEdge),
					resource.TestCheckResourceAttr("enclave_machine.vps", "schedulable", "false"),
					resource.TestCheckResourceAttr("enclave_machine.vps", "address", "203.0.113.9"),
					resource.TestMatchResourceAttr("enclave_machine.vps", "join_command", regexp.MustCompile(`^enclave agent join --edge --code \S+$`)),
				),
			},
			{
				// A label changes in place, and the plan stays empty afterwards.
				Config: `resource "enclave_machine" "vps" {
  name    = "vps1"
  role    = "edge"
  address = "203.0.113.9"
  labels  = { region = "fra" }
}`,
				Check: resource.TestCheckResourceAttr("enclave_machine.vps", "labels.region", "fra"),
			},
			{
				Config: `resource "enclave_machine" "box" {
  name    = "box2"
  address = "203.0.113.10"
}`,
				ExpectError: regexp.MustCompile(`(?i)edge machine`),
			},
		},
	})
}
