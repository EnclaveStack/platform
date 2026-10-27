package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.8 (L2-003 TF-42 – TF-47) with real OpenTofu: machines with fake agents, moves, stacks and
// templates.

func TestAccMachinesPlacementAndMoves(t *testing.T) {
	env, c := boot(t)
	var dbID string
	joined := func(s *terraform.State) error {
		cmd := s.RootModule().Resources["enclave_machine.rack"].Primary.Attributes["join_command"]
		code := regexp.MustCompile(`--code (\S+)`).FindStringSubmatch(cmd)
		if code == nil {
			return fmt.Errorf("join_command %q", cmd)
		}
		env.JoinAgent(t, "rack-1", code[1])
		env.WaitMachine(t, c, "rack-1", api.MachineOnline)
		return nil
	}
	db := func(machine string) string {
		return fmt.Sprintf(`
resource "enclave_machine" "rack" {
  name   = "rack-1"
  labels = { disk = "ssd" }
}
resource "enclave_database" "orders" {
  name    = "orders"
  machine = %s
}
resource "enclave_cache" "sessions" {
  depends_on     = [enclave_machine.rack]
  name           = "sessions"
  machine_labels = { disk = "ssd" }
}
data "enclave_machines" "all" { depends_on = [enclave_database.orders] }
`, machine)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `resource "enclave_machine" "rack" {
  name   = "rack-1"
  labels = { disk = "ssd" }
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_machine.rack", "state", api.MachineWaiting),
					resource.TestMatchResourceAttr("enclave_machine.rack", "join_command", regexp.MustCompile(`^enclave agent join `)),
					joined,
				),
			},
			{
				Config: db(`enclave_machine.rack.name`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_machine.rack", "state", api.MachineOnline),
					resource.TestCheckResourceAttr("enclave_machine.rack", "join_command", ""),
					resource.TestCheckResourceAttr("enclave_database.orders", "machine", "rack-1"),
					resource.TestCheckResourceAttr("enclave_cache.sessions", "machine", "rack-1"),
					resource.TestCheckResourceAttr("data.enclave_machines.all", "machines.#", "2"),
					resource.TestCheckResourceAttr("data.enclave_machines.all", "machines.0.name", testenv.MainName()),
					resource.TestCheckResourceAttr("data.enclave_machines.all", "machines.1.resources", "2"),
					func(s *terraform.State) error {
						dbID = s.RootModule().Resources["enclave_database.orders"].Primary.ID
						return nil
					},
				),
			},
			{
				// Changing machine moves the database in place: the same ID, now on the main machine (TF-43).
				Config: db(`"` + testenv.MainName() + `"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_database.orders", "machine", testenv.MainName()),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["enclave_database.orders"].Primary.ID; id != dbID {
							return fmt.Errorf("replaced: %s → %s", dbID, id)
						}
						r, err := c.GetResource(context.Background(), dbID)
						if err != nil || r.Outputs["machine"] != testenv.MainName() {
							return fmt.Errorf("on %v: %v", r.Outputs["machine"], err)
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_machine.rack",
				ImportState:             true,
				ImportStateId:           "rack-1",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"join_command"},
			},
			{
				// Destroying a machine something runs on is refused (TF-42).
				Config: `
resource "enclave_cache" "sessions" {
  name           = "sessions"
  machine_labels = { disk = "ssd" }
}
resource "enclave_database" "orders" {
  name    = "orders"
  machine = "` + testenv.MainName() + `"
}`,
				ExpectError: regexp.MustCompile(`(?s)still runs.*cache/sessions`),
			},
			{
				// Back to the whole configuration, so the cache is destroyed before its machine.
				Config: db(`"` + testenv.MainName() + `"`),
				Check:  resource.TestCheckResourceAttr("enclave_machine.rack", "state", api.MachineOnline),
			},
		},
	})
}

func TestAccStacksAndTemplates(t *testing.T) {
	_, c := boot(t)
	const tpl = `name: tiny
title: Tiny
version: %s
parameters:
  - { name: memoryMiB, type: number, default: 256 }
  - { name: key, type: secret, required: true }
resources:
  - kind: app
    name: ""
    spec: { image: nginx, memoryMiB: "${param:memoryMiB}", secretEnv: { KEY: "${param:key}" } }
outputs:
  app: "${name:}"
`
	config := func(version, memory string) string {
		return fmt.Sprintf(`
data "enclave_catalogue" "all" {}
resource "enclave_stack" "blog" {
  name       = "blog"
  template   = "wordpress"
  parameters = { domain = "blog.localhost" }
}
resource "enclave_catalogue_template" "tiny" {
  body = <<-EOT
%s
EOT
}
resource "enclave_stack" "small" {
  name              = "small"
  template          = enclave_catalogue_template.tiny.name
  template_version  = %q
  parameters        = { memoryMiB = %q }
  secret_parameters = { key = "s3cret" }
}
`, strings.ReplaceAll(fmt.Sprintf(tpl, version), "${", "$${"), version, memory)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("1.0.0", "256"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.enclave_catalogue.all", "entries.#", "18"), // 6 stacks, 6 ways in (L2-014), 6 connections (L2-015, L2-016)
					resource.TestCheckTypeSetElemNestedAttrs("data.enclave_catalogue.all", "entries.*", map[string]string{"name": "quick-link", "category": api.CategoryPublicAccess}),
					resource.TestCheckTypeSetElemNestedAttrs("data.enclave_catalogue.all", "entries.*", map[string]string{"name": "supabase", "category": api.CategoryConnections}),
					resource.TestCheckResourceAttr("enclave_stack.blog", "members.#", "4"),
					resource.TestCheckResourceAttr("enclave_stack.blog", "outputs.url", "https://blog.localhost"),
					resource.TestCheckResourceAttr("enclave_stack.blog", "template_version", "1.0.0"),
					resource.TestCheckResourceAttr("enclave_catalogue_template.tiny", "name", "tiny"),
					resource.TestCheckResourceAttr("enclave_stack.small", "outputs.app", "small"),
					resource.TestCheckResourceAttr("enclave_stack.small", "parameters.memoryMiB", "256"),
				),
			},
			{
				// A new version of the template, and the stack follows it with more memory (TF-44, TF-45).
				Config: config("1.1.0", "512"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_catalogue_template.tiny", "versions.#", "2"),
					resource.TestCheckResourceAttr("enclave_stack.small", "template_version", "1.1.0"),
					func(*terraform.State) error {
						app, err := c.FindResource(context.Background(), api.KindApp, "small")
						if err != nil || !strings.Contains(string(app.Spec), `"memoryMiB":512`) {
							return fmt.Errorf("the member: %s %v", app.Spec, err)
						}
						if strings.Contains(string(app.Spec), "s3cret") {
							return fmt.Errorf("the secret parameter is in the member's spec")
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_stack.blog",
				ImportState:             true,
				ImportStateId:           "blog",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				ResourceName:      "enclave_catalogue_template.tiny",
				ImportState:       true,
				ImportStateId:     "tiny",
				ImportStateVerify: true,
			},
			{
				// A member is the stack's: importing it on its own is refused with the stack's name (TF-46).
				Config: config("1.1.0", "512") + `resource "enclave_app" "blog" {
  name  = "blog"
  image = "wordpress"
}`,
				ResourceName:  "enclave_app.blog",
				ImportState:   true,
				ImportStateId: "blog",
				ExpectError:   regexp.MustCompile(`part of the stack blog`),
			},
		},
	})
	if _, err := c.FindResource(context.Background(), api.KindApp, "blog"); err == nil {
		t.Error("the stack's members outlived it")
	}
}
