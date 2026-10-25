package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// v0.6 resources (L2-003 TF-27 – TF-33) with real OpenTofu against the fake engine, the in-memory broker
// and in-memory email.

func TestAccDataAndMessagingStack(t *testing.T) {
	boot(t)
	var queueID, topicID, cdnID, emailID string
	config := func(visibility, memory, ttl, presets, relay string) string {
		return fmt.Sprintf(`
resource "enclave_database" "analytics" {
  name   = "analytics"
  engine = "clickhouse"
}
resource "enclave_secret" "hook" {
  name  = "hook-key"
  value = "s3cret"
}
resource "enclave_secret" "smtp" {
  name  = "smtp-login"
  value = jsonencode({ username = "u", password = "p" })
}
resource "enclave_queue" "failed" {
  name = "emails-failed"
}
resource "enclave_queue" "emails" {
  name                       = "emails"
  visibility_timeout_seconds = %s
  max_receives               = 5
  dead_letter_queue          = enclave_queue.failed.name
}
resource "enclave_topic" "orders" {
  name          = "orders"
  subscriptions = [
    { queue = enclave_queue.emails.name },
    { url = "https://hooks.example.com/orders", secret = enclave_secret.hook.name },
  ]
}
resource "enclave_search" "catalogue" {
  name       = "catalogue"
  memory_mib = %s
}
resource "enclave_bucket" "uploads" {
  name = "uploads"
}
resource "enclave_transcoder" "videos" {
  name    = "videos"
  input   = { bucket = enclave_bucket.uploads.name, prefix = "videos/" }
  output  = { bucket = enclave_bucket.uploads.name, prefix = "media/" }
  presets = %s
}
resource "enclave_app" "web" {
  name  = "web"
  image = "nginx:1.27-alpine"
  port  = 80
}
resource "enclave_cdn" "assets" {
  name                = "assets"
  hostname            = "assets.localhost"
  origin              = "app/${enclave_app.web.name}"
  default_ttl_seconds = %s
}
resource "enclave_email" "shop" {
  name   = "shop-mail"
  domain = "example.com"
  %s
}
`, visibility, memory, presets, ttl, relay)
	}
	relay := `relay = { host = "smtp.example.com", credentials_secret = enclave_secret.smtp.name }`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("30", "2048", "3600", `["mp4-720p", "thumbnail"]`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_database.analytics", "version", "26.3"),
					resource.TestCheckResourceAttrSet("enclave_database.analytics", "engine_port_native"),
					resource.TestCheckResourceAttr("enclave_queue.emails", "subject", "es.q.emails"),
					resource.TestCheckResourceAttr("enclave_queue.emails", "password_secret", "emails-queue-password"),
					resource.TestCheckNoResourceAttr("enclave_queue.failed", "retention_hours"),
					resource.TestMatchResourceAttr("enclave_topic.orders", "broker_url", regexp.MustCompile(`^nats://127\.0\.0\.1:\d+$`)),
					resource.TestCheckResourceAttr("enclave_topic.orders", "internal_broker_url", "nats://es-system-broker:4222"),
					resource.TestCheckResourceAttr("enclave_search.catalogue", "version", "3"),
					resource.TestCheckResourceAttr("enclave_search.catalogue", "username", "admin"),
					resource.TestCheckResourceAttr("enclave_cdn.assets", "url", "https://assets.localhost"),
					resource.TestCheckResourceAttr("enclave_cdn.assets", "tls", "internal"),
					resource.TestCheckResourceAttr("enclave_email.shop", "mode", "capture"),
					resource.TestCheckResourceAttrSet("enclave_email.shop", "web_url"),
					sameID("enclave_queue.emails", &queueID), sameID("enclave_topic.orders", &topicID),
					sameID("enclave_cdn.assets", &cdnID), sameID("enclave_email.shop", &emailID),
				),
			},
			{
				// In place: queue and CDN settings, search memory, presets, relay mode.
				Config: config("60", "3072", "600", `["mp3"]`, relay),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_queue.emails", "visibility_timeout_seconds", "60"),
					resource.TestCheckResourceAttr("enclave_search.catalogue", "memory_mib", "3072"),
					resource.TestCheckResourceAttr("enclave_transcoder.videos", "presets.#", "1"),
					resource.TestCheckResourceAttr("enclave_cdn.assets", "default_ttl_seconds", "600"),
					resource.TestCheckResourceAttr("enclave_email.shop", "mode", "relay"),
					resource.TestCheckNoResourceAttr("enclave_email.shop", "relay.port"),
					sameID("enclave_queue.emails", &queueID), sameID("enclave_topic.orders", &topicID),
					sameID("enclave_cdn.assets", &cdnID), sameID("enclave_email.shop", &emailID),
				),
			},
			{ResourceName: "enclave_queue.emails", ImportState: true, ImportStateId: "emails", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_topic.orders", ImportState: true, ImportStateId: "orders", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_cdn.assets", ImportState: true, ImportStateId: "assets", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_transcoder.videos", ImportState: true, ImportStateId: "videos", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_email.shop", ImportState: true, ImportStateId: "shop-mail", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_search.catalogue", ImportState: true, ImportStateId: "catalogue", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
		},
	})
}
