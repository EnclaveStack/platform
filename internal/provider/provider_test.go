package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/provider"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Acceptance tests run real OpenTofu (TF_ACC=1, TF_ACC_TERRAFORM_PATH=$(which tofu)) against a
// control plane booted in-process with the fake engine (L5-002 §1).

var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"enclave": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func boot(t *testing.T) (*testenv.Env, *client.Client) {
	t.Helper()
	env := testenv.Start(t)
	c, tok := env.Token(t, api.ScopeWrite)
	t.Setenv("ENCLAVE_SERVER", env.URL)
	t.Setenv("ENCLAVE_TOKEN", tok)
	return env, c
}

func TestAccDatabaseCreateImportReplace(t *testing.T) {
	_, c := boot(t)
	var firstID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "enclave_database" "orders" { name = "orders" }
data "enclave_secret_value" "pw" { name = enclave_database.orders.password_secret }
data "enclave_machine" "this" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_database.orders", "version", "16"),
					resource.TestCheckResourceAttr("enclave_database.orders", "database_name", "orders"),
					resource.TestCheckResourceAttr("enclave_database.orders", "password_secret", "orders-password"),
					resource.TestCheckResourceAttrSet("enclave_database.orders", "port"),
					resource.TestCheckResourceAttrSet("data.enclave_secret_value.pw", "value"),
					resource.TestCheckResourceAttr("data.enclave_machine.this", "engine_driver", "fake"),
					resource.TestCheckResourceAttr("data.enclave_machine.this", "engine_available", "true"),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources["enclave_database.orders"].Primary.ID
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_database.orders",
				ImportState:             true,
				ImportStateId:           "orders",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// Changing the version needs a new database (TF-03).
				Config: `resource "enclave_database" "orders" {
  name    = "orders"
  version = "17"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_database.orders", "version", "17"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["enclave_database.orders"].Primary.ID; id == firstID {
							return fmt.Errorf("version change did not replace the database (id %s unchanged)", id)
						}
						return nil
					},
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			items, err := c.ListAllResources(context.Background(), api.KindDatabase)
			if err != nil {
				return err
			}
			for _, r := range items {
				if r.Status.Phase != api.PhaseDeleting {
					return fmt.Errorf("database %s survived destroy", r.Name)
				}
			}
			return nil
		},
	})
}

func TestAccAppAndSecretUpdateInPlace(t *testing.T) {
	_, c := boot(t)
	var appID string
	config := func(image, value string, env string) string {
		return fmt.Sprintf(`
resource "enclave_secret" "key" {
  name  = "stripe-key"
  value = %q
}
resource "enclave_app" "api" {
  name       = "api"
  image      = %q
  port       = 8080
  publish    = true
  %s
  secret_env = { STRIPE_KEY = enclave_secret.key.name }
}`, value, image, env)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("ghcr.io/acme/api:1.4", "v1", `env = { LOG_LEVEL = "info" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.api", "memory_mib", "256"),
					resource.TestCheckResourceAttr("enclave_app.api", "host", "127.0.0.1"),
					resource.TestCheckResourceAttrSet("enclave_app.api", "host_port"),
					resource.TestCheckResourceAttr("enclave_app.api", "env.LOG_LEVEL", "info"),
					resource.TestCheckResourceAttr("enclave_secret.key", "version", "1"),
					func(s *terraform.State) error {
						appID = s.RootModule().Resources["enclave_app.api"].Primary.ID
						return nil
					},
				),
			},
			{
				// Image, env and secret value change in place: same IDs, new versions.
				Config: config("ghcr.io/acme/api:1.5", "v2", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.api", "image", "ghcr.io/acme/api:1.5"),
					resource.TestCheckNoResourceAttr("enclave_app.api", "env.LOG_LEVEL"),
					resource.TestCheckResourceAttr("enclave_secret.key", "version", "2"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["enclave_app.api"].Primary.ID; id != appID {
							return fmt.Errorf("app was replaced (%s → %s); want an in-place update", appID, id)
						}
						v, err := c.RevealSecret(context.Background(), s.RootModule().Resources["enclave_secret.key"].Primary.ID)
						if err != nil || v != "v2" {
							return fmt.Errorf("secret value on server %q (%v)", v, err)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccCacheAndBucketUpdateAndDrift(t *testing.T) {
	_, c := boot(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "enclave_cache" "sessions" { name = "sessions" }
resource "enclave_bucket" "media" { name = "media" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_cache.sessions", "max_memory_mib", "256"),
					resource.TestCheckResourceAttr("enclave_bucket.media", "versioning", "false"),
					resource.TestCheckResourceAttr("enclave_bucket.media", "credentials_secret", "objectstore-credentials"),
				),
			},
			{
				Config: `
resource "enclave_cache" "sessions" {
  name           = "sessions"
  max_memory_mib = 512
}
resource "enclave_bucket" "media" {
  name       = "media"
  versioning = true
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_cache.sessions", "max_memory_mib", "512"),
					resource.TestCheckResourceAttr("enclave_bucket.media", "versioning", "true"),
				),
			},
			{
				// Someone deletes the cache in the console: the next plan recreates it (TF-04).
				PreConfig: func() {
					r, err := c.FindResource(context.Background(), api.KindCache, "sessions")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := c.DeleteResource(context.Background(), r.ID, nil); err != nil {
						t.Fatal(err)
					}
					_ = c.WaitGone(context.Background(), r.ID, 20e6)
				},
				Config: `
resource "enclave_cache" "sessions" {
  name           = "sessions"
  max_memory_mib = 512
}
resource "enclave_bucket" "media" {
  name       = "media"
  versioning = true
}`,
				Check: resource.TestCheckResourceAttrSet("enclave_cache.sessions", "id"),
			},
		},
	})
}

func TestAccFailureIsReportedWithTheServerMessage(t *testing.T) {
	env, _ := boot(t)
	env.Fake.SetFault("broken", fake.Fault{Permanent: true})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: `resource "enclave_app" "broken" {
  name  = "broken"
  image = "acme/missing:1"
}`,
			ExpectError: regexp.MustCompile(`(?s)Create app failed.*image not found`),
		}},
	})
}

func TestAccPlanTimeValidation(t *testing.T) {
	boot(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      `resource "enclave_cache" "c" { name = "Bad_Name" }`,
			ExpectError: regexp.MustCompile(`Invalid name`),
			PlanOnly:    true,
		}},
	})
}

func TestAccAppHealthCheckStrategyAndFailedRevision(t *testing.T) {
	env, _ := boot(t)
	config := func(image, health string) string {
		return fmt.Sprintf(`
resource "enclave_app" "web" {
  name     = "web"
  image    = %q
  port     = 80
  strategy = "rolling"
  %s
}`, image, health)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("nginx:1.27-alpine", `health_check = {
    type    = "http"
    path    = "/healthz"
    retries = 5
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.web", "serving_revision", "1"),
					resource.TestCheckResourceAttr("enclave_app.web", "health_check.path", "/healthz"),
					resource.TestCheckResourceAttr("enclave_app.web", "health_check.retries", "5"),
					resource.TestCheckNoResourceAttr("enclave_app.web", "health_check.interval_seconds"),
				),
			},
			{
				// A new image is a new revision; the unchanged check plans no diff (server defaults stay hidden).
				Config: config("nginx:1.28-alpine", `health_check = {
    type    = "http"
    path    = "/healthz"
    retries = 5
  }`),
				Check: resource.TestCheckResourceAttr("enclave_app.web", "serving_revision", "2"),
			},
			{
				// Removing the check clears it on the server.
				Config: config("nginx:1.28-alpine", ""),
				Check:  resource.TestCheckNoResourceAttr("enclave_app.web", "health_check.type"),
			},
			{
				// A revision that fails its health check fails the apply with the probe result (TF-12).
				PreConfig:   func() { env.Fake.SetFault("web", fake.Fault{Unhealthy: true}) },
				Config:      config("nginx:broken", ""),
				ExpectError: regexp.MustCompile(`(?s)Revision\s+4\s+failed\s+its\s+health\s+check.*Revision\s+3\s+is\s+still\s+serving`),
			},
		},
	})
}

func TestAccDomainCertificateImportReplaceAndMachineSettings(t *testing.T) {
	boot(t)
	var firstID string
	config := func(hostname, tls string) string {
		return fmt.Sprintf(`
resource "enclave_machine_settings" "this" {
  public_address = "203.0.113.7"
}
resource "enclave_app" "web" {
  name  = "web"
  image = "nginx:1.27-alpine"
  port  = 80
}
resource "enclave_domain" "web" {
  name     = "web"
  hostname = %q
  app      = enclave_app.web.name
  %s
}
data "enclave_ca" "this" { depends_on = [enclave_domain.web] }
`, hostname, tls)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("web.localhost", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_domain.web", "tls", "internal"),
					resource.TestCheckResourceAttr("enclave_domain.web", "redirect_http", "true"),
					resource.TestCheckResourceAttr("enclave_domain.web", "url", "https://web.localhost"),
					resource.TestCheckResourceAttr("enclave_domain.web", "certificate_state", "issued"),
					resource.TestCheckResourceAttrSet("enclave_domain.web", "certificate_expires_at"),
					resource.TestMatchResourceAttr("data.enclave_ca.this", "pem", regexp.MustCompile("BEGIN CERTIFICATE")),
					resource.TestCheckResourceAttr("enclave_machine_settings.this", "public_address", "203.0.113.7"),
					resource.TestCheckNoResourceAttr("enclave_machine_settings.this", "acme_email"),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources["enclave_domain.web"].Primary.ID
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_domain.web",
				ImportState:             true,
				ImportStateId:           "web",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// TLS changes in place.
				Config: config("web.localhost", `tls = "off"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_domain.web", "url", "http://web.localhost"),
					resource.TestCheckResourceAttr("enclave_domain.web", "certificate_state", "none"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["enclave_domain.web"].Primary.ID; id != firstID {
							return fmt.Errorf("tls change replaced the domain")
						}
						return nil
					},
				),
			},
			{
				// A new hostname is a new domain (TF-11).
				Config: config("site.localhost", `tls = "off"`),
				Check: func(s *terraform.State) error {
					if id := s.RootModule().Resources["enclave_domain.web"].Primary.ID; id == firstID {
						return fmt.Errorf("hostname change did not replace the domain")
					}
					return nil
				},
			},
		},
	})
}

func TestAccJobScheduleImportAndValidation(t *testing.T) {
	boot(t)
	config := func(schedule string) string {
		return fmt.Sprintf(`
resource "enclave_app" "api" {
  name  = "api"
  image = "ghcr.io/acme/api:1.4"
  port  = 8080
  env   = { MODE = "web" }
}
resource "enclave_job" "cleanup" {
  name      = "cleanup"
  schedule  = %q
  time_zone = "Asia/Kolkata"
  app       = enclave_app.api.name
  command   = ["./bin/cleanup", "--older-than", "7d"]
  env       = { MODE = "job" }
}`, schedule)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("0 3 * * *"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_job.cleanup", "timeout_seconds", "3600"),
					resource.TestCheckResourceAttr("enclave_job.cleanup", "overlap", "forbid"),
					resource.TestMatchResourceAttr("enclave_job.cleanup", "next_run_at", regexp.MustCompile(`T21:30:00Z$`)), // 03:00 IST
					resource.TestCheckNoResourceAttr("enclave_job.cleanup", "image"),
				),
			},
			{
				ResourceName:            "enclave_job.cleanup",
				ImportState:             true,
				ImportStateId:           "cleanup",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "next_run_at", "last_run_status"},
			},
			{Config: config("@hourly"), Check: resource.TestCheckResourceAttr("enclave_job.cleanup", "schedule", "@hourly")},
			{
				Config: `
resource "enclave_job" "bad" {
  name     = "bad"
  schedule = "@daily"
  app      = "api"
  image    = "busybox"
}`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`), // caught at plan time
			},
			{Config: config("@hourly")}, // the framework destroys with the last config
		},
	})
}

func TestAccBackupDestinationsImportAndReplace(t *testing.T) {
	boot(t)
	config := func(source, destination, extra string) string {
		return fmt.Sprintf(`
resource "enclave_database" "shop" { name = "shop" }
resource "enclave_cache" "sessions" { name = "sessions" }
resource "enclave_bucket" "vault" { name = "vault" }
resource "enclave_secret" "s3" {
  name  = "offsite-s3"
  value = jsonencode({ accessKey = "AKIAEXAMPLE", secretKey = "example-secret" })
}
resource "enclave_backup" "nightly" {
  name      = "nightly"
  %s
  %s
  time_zone = "Asia/Kolkata"
  %s
}`, source, destination, extra)
	}
	db, cache := `source = { database = enclave_database.shop.name }`, `source = { cache = enclave_cache.sessions.name }`
	bucket := `bucket = enclave_bucket.vault.name`
	s3 := `s3 = {
    endpoint           = "https://s3.eu-central-1.amazonaws.com"
    region             = "eu-central-1"
    bucket             = "acme-offsite"
    credentials_secret = enclave_secret.s3.name
  }`
	var firstID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(db, bucket, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_backup.nightly", "schedule", "0 3 * * *"),
					resource.TestCheckResourceAttr("enclave_backup.nightly", "keep", "7"),
					resource.TestCheckResourceAttr("enclave_backup.nightly", "keep_days", "0"),
					resource.TestCheckResourceAttr("enclave_backup.nightly", "prefix", "enclave-backups"),
					resource.TestCheckResourceAttr("enclave_backup.nightly", "timeout_seconds", "7200"),
					resource.TestMatchResourceAttr("enclave_backup.nightly", "next_run_at", regexp.MustCompile(`T21:30:00Z$`)), // 03:00 IST
					resource.TestCheckNoResourceAttr("enclave_backup.nightly", "source.cache"),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources["enclave_backup.nightly"].Primary.ID
						return nil
					},
				),
			},
			{
				ResourceName:            "enclave_backup.nightly",
				ImportState:             true,
				ImportStateId:           "nightly",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts", "next_run_at", "last_run_status", "last_success_at"},
			},
			{
				// Destination and retention change in place.
				Config: config(db, s3, "keep = 30\n  keep_days = 90"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_backup.nightly", "s3.bucket", "acme-offsite"),
					resource.TestCheckNoResourceAttr("enclave_backup.nightly", "bucket"),
					resource.TestCheckResourceAttr("enclave_backup.nightly", "keep", "30"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["enclave_backup.nightly"].Primary.ID; id != firstID {
							return fmt.Errorf("a destination change replaced the backup")
						}
						return nil
					},
				),
			},
			{
				// A new source is a new backup.
				Config: config(cache, s3, ""),
				Check: func(s *terraform.State) error {
					r := s.RootModule().Resources["enclave_backup.nightly"].Primary
					if r.ID == firstID || r.Attributes["source.cache"] != "sessions" {
						return fmt.Errorf("source change: id %s cache %q", r.ID, r.Attributes["source.cache"])
					}
					return nil
				},
			},
			{Config: config(`source = {
    database = "shop"
    cache    = "sessions"
  }`, bucket, ""), ExpectError: regexp.MustCompile(`Invalid Attribute Combination`)},
			{Config: config(`source = {}`, bucket, ""), ExpectError: regexp.MustCompile(`Missing Attribute Configuration`)},
			{Config: config(db, bucket+"\n  "+s3, ""), ExpectError: regexp.MustCompile(`Invalid Attribute Combination`)},
			{Config: config(db, bucket, "keep = 0"), ExpectError: regexp.MustCompile(`keep`)},
			{
				Config:      config(db, strings.Replace(s3, "https://", "http://", 1), ""),
				ExpectError: regexp.MustCompile(`(?s)spec\.destination\.s3\.endpoint`), // server-side: https only
			},
			{Config: config(cache, s3, "")},
		},
	})
}

func TestAccAppFromGitBuildsImportsAndSwitches(t *testing.T) {
	env, _ := boot(t)
	const repo = "https://git.example.com/acme/site.git"
	first := env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo $MODE\n"})
	config := func(source string) string {
		return fmt.Sprintf(`
resource "enclave_secret" "git" {
  name  = "git-token"
  value = jsonencode({ token = "ghp_example" })
}
resource "enclave_app" "site" {
  name = "site"
  port = 8080
  %s
}`, source)
	}
	src := func(args string) string {
		return fmt.Sprintf(`source = {
    git                = %q
    ref                = "main"
    credentials_secret = enclave_secret.git.name
    build_args         = { %s }
  }`, repo, args)
	}
	var second string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(src(`MODE = "one"`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.site", "built_commit", first),
					resource.TestCheckNoResourceAttr("enclave_app.site", "image"),
					resource.TestCheckNoResourceAttr("enclave_app.site", "source.dockerfile"),
				),
			},
			{
				ResourceName:            "enclave_app.site",
				ImportState:             true,
				ImportStateId:           "site",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// A changed build argument is a new revision, built from the ref's newest commit.
				PreConfig: func() {
					second = env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo $MODE again\n"})
				},
				Config: config(src(`MODE = "two"`)),
				Check: func(s *terraform.State) error {
					if got := s.RootModule().Resources["enclave_app.site"].Primary.Attributes["built_commit"]; got != second {
						return fmt.Errorf("built %s, want %s", got, second)
					}
					return nil
				},
			},
			{
				PreConfig:   func() { env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN exit 4\n"}) },
				Config:      config(src(`MODE = "three"`)),
				ExpectError: regexp.MustCompile(`(?s)exit code: 4`),
			},
			{
				Config: config(`image = "nginx:1.27-alpine"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.site", "image", "nginx:1.27-alpine"),
					resource.TestCheckNoResourceAttr("enclave_app.site", "source.git"),
				),
			},
			{
				Config:      config(`image = "nginx"` + "\n  " + src(`MODE = "x"`)),
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{Config: config(`image = "nginx:1.27-alpine"`)},
		},
	})
}
