package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// v0.4 resources (L2-003 TF-16 – TF-21) with real OpenTofu against the fake engine.

func sameID(addr string, id *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		got := s.RootModule().Resources[addr].Primary.ID
		if *id == "" {
			*id = got
			return nil
		}
		if got != *id {
			return fmt.Errorf("%s was replaced (%s → %s)", addr, *id, got)
		}
		return nil
	}
}

func TestAccNetworkingStack(t *testing.T) {
	boot(t)
	var dbID, appID, fwID, lbID string
	config := func(dbNetwork, apiReplicas, fwPort, lbAlgorithm, recordTTL string) string {
		return fmt.Sprintf(`
resource "enclave_network" "backend" {
  name = "backend"
  cidr = "10.40.0.0/16"
  subnet = [
    { name = "apps", cidr = "10.40.2.0/24" },
    { name = "data", cidr = "10.40.1.0/24" },
  ]
}
resource "enclave_database" "orders" {
  name    = "orders"
  %s
}
resource "enclave_app" "api" {
  name     = "api"
  image    = "nginx:1.27-alpine"
  port     = 8080
  network  = enclave_network.backend.name
  replicas = %s
}
resource "enclave_firewall" "db" {
  name    = "db-access"
  targets = ["database/${enclave_database.orders.name}"]
  ingress = [{ from = ["app/${enclave_app.api.name}", "10.9.0.0/16"], ports = [%q] }]
}
resource "enclave_load_balancer" "api" {
  name      = "api-lb"
  port      = 80
  publish   = true
  algorithm = %q
  target    = [{ app = enclave_app.api.name, weight = 3 }]
}
resource "enclave_secret" "cf" {
  name  = "cf-token"
  value = jsonencode({ apiToken = "t" })
}
resource "enclave_machine_settings" "this" { public_address = "203.0.113.7" }
resource "enclave_zone" "example" {
  name               = "example"
  dns_provider       = "cloudflare"
  zone               = "Example.com"
  credentials_secret = enclave_secret.cf.name
}
resource "enclave_record" "api" {
  name        = "api"
  zone        = enclave_zone.example.name
  record_name = "api"
  type        = "a"
  machine     = true
  ttl         = %s
  depends_on  = [enclave_machine_settings.this]
}
resource "enclave_domain" "wild" {
  name     = "wild"
  hostname = "*.example.com"
  app      = enclave_app.api.name
  zone     = enclave_zone.example.name
}
`, dbNetwork, apiReplicas, fwPort, lbAlgorithm, recordTTL)
	}
	placed := `network = enclave_network.backend.name
  subnet  = "data"`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("", "3", "5432", "round_robin", "300"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_network.backend", "docker_network", "es-net-backend"),
					resource.TestCheckResourceAttr("enclave_network.backend", "internet", "true"),
					resource.TestCheckResourceAttr("enclave_app.api", "private_dns", "api.app.internal"),
					resource.TestCheckResourceAttr("enclave_firewall.db", "applied_containers", "1"),
					resource.TestCheckResourceAttr("enclave_firewall.db", "ingress.0.protocol", "tcp"),
					resource.TestCheckResourceAttr("enclave_load_balancer.api", "healthy_targets", "3"),
					resource.TestCheckResourceAttrSet("enclave_load_balancer.api", "host_port"),
					resource.TestCheckResourceAttr("enclave_zone.example", "zone", "Example.com"),
					resource.TestCheckResourceAttr("enclave_zone.example", "name_servers.#", "2"),
					resource.TestCheckResourceAttr("enclave_record.api", "fqdn", "api.example.com"),
					resource.TestCheckResourceAttr("enclave_domain.wild", "certificate_state", "issued"),
					sameID("enclave_database.orders", &dbID), sameID("enclave_app.api", &appID),
					sameID("enclave_firewall.db", &fwID), sameID("enclave_load_balancer.api", &lbID),
				),
			},
			{
				// Everything but the network changes in place: the database moves, the app scales, rules,
				// algorithm and TTL change.
				Config: config(placed, "2", "5433", "least_conn", "120"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_database.orders", "private_dns", "orders.database.internal"),
					resource.TestMatchResourceAttr("enclave_database.orders", "private_ip", regexpIn("10.40.1.")),
					resource.TestCheckResourceAttr("enclave_app.api", "replicas", "2"),
					resource.TestCheckResourceAttr("enclave_firewall.db", "ingress.0.ports.0", "5433"),
					resource.TestCheckResourceAttr("enclave_load_balancer.api", "algorithm", "least_conn"),
					resource.TestCheckResourceAttr("enclave_record.api", "ttl", "120"),
					sameID("enclave_database.orders", &dbID), sameID("enclave_app.api", &appID),
					sameID("enclave_firewall.db", &fwID), sameID("enclave_load_balancer.api", &lbID),
				),
			},
			{ResourceName: "enclave_network.backend", ImportState: true, ImportStateId: "backend", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_firewall.db", ImportState: true, ImportStateId: "db-access", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "applied_containers"}},
			{ResourceName: "enclave_load_balancer.api", ImportState: true, ImportStateId: "api-lb", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "healthy_targets"}},
			{ResourceName: "enclave_record.api", ImportState: true, ImportStateId: "api", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "type"}},
		},
	})
}

func regexpIn(prefix string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(prefix))
}
