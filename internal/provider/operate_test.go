package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// v0.7 resources (L2-003 TF-34 – TF-41) with real OpenTofu against the fake engine and a fake Vault.

func TestAccOperateStack(t *testing.T) {
	env, _ := boot(t)
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("X-Vault-Token") != "s.tofu":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/v1/auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case r.URL.Path == "/v1/secret/data/shop/stripe":
			_, _ = w.Write([]byte(`{"data":{"data":{"key":"sk_tofu"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(vault.Close)
	if err := env.Monitor.SampleOnce(context.Background()); err != nil { // the machine's series exist for the data source
		t.Fatal(err)
	}
	var alertID string
	config := func(threshold, role string) string {
		return fmt.Sprintf(`
resource "enclave_project" "shop" {
  name        = "shop"
  description = "the shop"
}
resource "enclave_email" "mail" {
  name = "mail"
}
resource "enclave_machine_settings" "this" {
  invite_email = { service = enclave_email.mail.name, from = "hc@example.test" }
}
resource "enclave_user" "dev" {
  depends_on    = [enclave_machine_settings.this]
  email         = "dev@example.test"
  project_roles = { (enclave_project.shop.name) = %q }
}
resource "enclave_secret" "key_default" {
  name  = "key"
  value = "in default"
}
resource "enclave_secret" "key_shop" {
  name    = "key"
  project = enclave_project.shop.name
  value   = "in shop"
}
resource "enclave_secret" "vault_token" {
  name    = "vault-token"
  project = enclave_project.shop.name
  value   = "s.tofu"
}
resource "enclave_secret_store" "vault" {
  name            = "vault"
  project         = enclave_project.shop.name
  secret_provider = "vault"
  settings        = { address = %q, tokenSecret = enclave_secret.vault_token.name }
}
resource "enclave_secret" "stripe" {
  name    = "stripe"
  project = enclave_project.shop.name
  source  = { store = enclave_secret_store.vault.name, key = "shop/stripe", field = "key" }
}
data "enclave_secret_value" "stripe" {
  name    = enclave_secret.stripe.name
  project = enclave_project.shop.name
}
resource "enclave_app" "api" {
  name    = "api"
  project = enclave_project.shop.name
  image   = "nginx:1.27-alpine"
}
resource "enclave_channel" "hook" {
  name    = "hook"
  project = enclave_project.shop.name
  type    = "webhook"
  webhook = { url = "http://127.0.0.1:9/alerts" }
}
resource "enclave_alert" "cpu" {
  name      = "api-cpu"
  project   = enclave_project.shop.name
  condition = { type = "metric", resources = ["app/${enclave_app.api.name}"], metric = "cpu", operator = "above", threshold = %s }
  channels  = [enclave_channel.hook.name]
}
resource "enclave_dashboard" "health" {
  name    = "health"
  project = enclave_project.shop.name
  widgets = [{ title = "API CPU", metric = "cpu", resources = ["app/${enclave_app.api.name}"] }]
}
resource "enclave_log_export" "out" {
  name    = "out"
  project = enclave_project.shop.name
  http    = { url = "http://127.0.0.1:9/logs" }
}
data "enclave_metrics" "disk" {
  resource = "machine"
  metric   = "diskFree"
}
`, role, vault.URL, threshold)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("0.9", "viewer"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_secret.key_default", "project", "default"),
					resource.TestCheckResourceAttr("enclave_secret.key_shop", "project", "shop"),
					resource.TestCheckResourceAttr("enclave_user.dev", "status", "invited"),
					resource.TestCheckResourceAttr("enclave_machine_settings.this", "invite_email.project", "default"),
					resource.TestMatchResourceAttr("enclave_user.dev", "invite_url", regexp.MustCompile(`/invite\?code=`)),
					resource.TestCheckResourceAttr("data.enclave_secret_value.stripe", "value", "sk_tofu"),
					resource.TestCheckResourceAttrSet("enclave_secret.stripe", "synced_at"),
					resource.TestCheckResourceAttr("enclave_alert.cpu", "condition.threshold", "0.9"),
					resource.TestCheckNoResourceAttr("enclave_alert.cpu", "condition.for_minutes"),
					resource.TestCheckResourceAttr("enclave_log_export.out", "lines_sent", "0"),
					resource.TestCheckResourceAttr("data.enclave_metrics.disk", "unit", "bytes"),
					resource.TestCheckResourceAttrSet("data.enclave_metrics.disk", "latest"),
					sameID("enclave_alert.cpu", &alertID),
				),
			},
			{
				// In place: the alert's threshold and the person's role.
				Config: config("0.5", "editor"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_alert.cpu", "condition.threshold", "0.5"),
					resource.TestCheckResourceAttr("enclave_user.dev", "project_roles.shop", "editor"),
					sameID("enclave_alert.cpu", &alertID),
				),
			},
			{ResourceName: "enclave_alert.cpu", ImportState: true, ImportStateId: "shop/api-cpu", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_channel.hook", ImportState: true, ImportStateId: "shop/hook", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_dashboard.health", ImportState: true, ImportStateId: "shop/health", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_log_export.out", ImportState: true, ImportStateId: "shop/out", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "last_sent_at"}},
			{ResourceName: "enclave_secret_store.vault", ImportState: true, ImportStateId: "shop/vault", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_project.shop", ImportState: true, ImportStateId: "shop", ImportStateVerify: true},
			{ResourceName: "enclave_secret.key_default", ImportState: true, ImportStateId: "key", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "value"}},
		},
	})
}
