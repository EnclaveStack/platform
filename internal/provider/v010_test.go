package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// v0.10 resources and attributes (L2-016 TF-54 – TF-57) with real OpenTofu against the fake engine.

func TestAccV010Stack(t *testing.T) {
	env, _ := boot(t)
	const repo = "https://git.example.com/acme/web.git"
	env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	var fnID, dbID, appID string
	config := func(concurrency, ttl, exposed string) string {
		return fmt.Sprintf(`
resource "enclave_queue" "jobs" {
  name                       = "jobs"
  visibility_timeout_seconds = 120
}
resource "enclave_bucket" "uploads" {
  name = "uploads"
}
resource "enclave_function" "echo" {
  name        = "echo"
  image       = "alpine:3.22"
  command     = ["exit", "0"]
  concurrency = %s
  triggers = [
    { queue = enclave_queue.jobs.name },
    { bucket = enclave_bucket.uploads.name, prefix = "in/" },
    { schedule = "@hourly" },
  ]
}
resource "enclave_topic" "news" {
  name          = "news"
  subscriptions = [{ function = enclave_function.echo.name }]
}
resource "enclave_database" "orders" {
  name        = "orders"
  tls         = true
  exposed     = %s
  init_script = "CREATE TABLE t (id int);"
}
resource "enclave_volume" "files" {
  name = "files"
  type = "filesystem"
}
resource "enclave_backup" "files" {
  name   = "files-bak"
  source = { volume = enclave_volume.files.name }
  bucket = enclave_bucket.uploads.name
}
resource "enclave_channel" "hook" {
  name    = "hook"
  type    = "webhook"
  webhook = { url = "http://127.0.0.1:1/hook" }
}
resource "enclave_alert" "deploys" {
  name      = "deploys"
  condition = { type = "event", resources = ["database/${enclave_database.orders.name}"], events = ["ready", "failed"] }
  channels  = [enclave_channel.hook.name]
}
resource "enclave_secret" "hook" {
  name  = "web-hook"
  value = "s3cret"
}
resource "enclave_app" "web" {
  name = "web"
  port = 8080
  source = {
    git            = %q
    ref            = "main"
    webhook_secret = enclave_secret.hook.name
    deploy_on_push = true
  }
  previews = {
    enabled   = true
    ttl_hours = %s
    domain    = { suffix = "preview.example.com" }
  }
}
`, concurrency, exposed, repo, ttl)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("2", "72", "false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_function.echo", "triggers.#", "3"),
					resource.TestCheckResourceAttr("enclave_function.echo", "timeout_seconds", "60"),
					resource.TestCheckResourceAttrSet("enclave_function.echo", "next_run_at"),
					resource.TestCheckResourceAttr("enclave_topic.news", "subscriptions.0.function", "echo"),
					resource.TestCheckResourceAttr("enclave_database.orders", "tls", "true"),
					resource.TestMatchResourceAttr("enclave_database.orders", "ca_certificate", regexp.MustCompile(`BEGIN CERTIFICATE`)),
					resource.TestCheckResourceAttrSet("enclave_database.orders", "certificate_expires_at"),
					resource.TestCheckResourceAttr("enclave_backup.files", "source.volume", "files"),
					resource.TestCheckResourceAttr("enclave_alert.deploys", "condition.events.#", "2"),
					resource.TestCheckResourceAttr("enclave_app.web", "previews.max_active", "5"),
					resource.TestMatchResourceAttr("enclave_app.web", "webhook_url", regexp.MustCompile(`/api/v1/hooks/git/app_`)),
					sameID("enclave_function.echo", &fnID), sameID("enclave_database.orders", &dbID), sameID("enclave_app.web", &appID),
				),
			},
			{
				// In place: concurrency, a preview setting and outside access.
				Config: config("4", "24", "true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_function.echo", "concurrency", "4"),
					resource.TestCheckResourceAttr("enclave_app.web", "previews.ttl_hours", "24"),
					resource.TestCheckResourceAttr("enclave_database.orders", "exposed", "true"),
					sameID("enclave_function.echo", &fnID), sameID("enclave_database.orders", &dbID), sameID("enclave_app.web", &appID),
				),
			},
			{ResourceName: "enclave_function.echo", ImportState: true, ImportStateId: "echo", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_database.orders", ImportState: true, ImportStateId: "orders", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "certificate_expires_at"}},
		},
	})
}
