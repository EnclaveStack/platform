package provider_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// bucket_access on enclave_app and enclave_job with real OpenTofu (L2-003 TF-48, L2-011).
func TestAccBucketAccess(t *testing.T) {
	env, c := boot(t)
	config := func(appAccess, jobAccess string) string {
		return fmt.Sprintf(`
resource "enclave_bucket" "media" { name = "media" }
resource "enclave_bucket" "invoices" { name = "invoices" }
resource "enclave_app" "web" {
  name          = "web"
  image         = "nginx:1.27"
  port          = 80
  bucket_access = %s
}
resource "enclave_job" "export" {
  name          = "export"
  schedule      = "@daily"
  image         = "busybox:1.36"
  command       = ["true"]
  bucket_access = %s
}
`, appAccess, jobAccess)
	}
	storeUser := func(name string, want ...api.BucketGrant) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			id := s.RootModule().Resources[name].Primary.ID
			got := env.Fake.StoreUserAccess(domain.StoreUserFor(id))
			if fmt.Sprint(got) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
				return fmt.Errorf("%s's store user reaches %v, want %v", name, got, want)
			}
			return nil
		}
	}
	read := `[{ bucket = enclave_bucket.media.name, access = "read" }]`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(read, `[{ bucket = enclave_bucket.invoices.name, access = "readwrite" }]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.web", "bucket_access.#", "1"),
					resource.TestCheckResourceAttr("enclave_app.web", "bucket_access.0.access", "read"),
					resource.TestCheckResourceAttr("enclave_app.web", "bucket_credentials_secret", "web-bucket-credentials"),
					resource.TestCheckResourceAttr("enclave_job.export", "bucket_credentials_secret", "export-bucket-credentials"),
					storeUser("enclave_app.web", api.BucketGrant{Bucket: "media", Access: api.BucketRead}),
					storeUser("enclave_job.export", api.BucketGrant{Bucket: "invoices", Access: api.BucketReadWrite}),
				),
			},
			{
				// Read to read and write, and a second bucket: updated in place.
				Config: config(`[
    { bucket = enclave_bucket.media.name, access = "readwrite" },
    { bucket = enclave_bucket.invoices.name, access = "read" },
  ]`, `[{ bucket = enclave_bucket.invoices.name, access = "readwrite" }]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_app.web", "bucket_access.#", "2"),
					storeUser("enclave_app.web", api.BucketGrant{Bucket: "media", Access: api.BucketReadWrite}, api.BucketGrant{Bucket: "invoices", Access: api.BucketRead}),
				),
			},
			{
				ResourceName:            "enclave_app.web",
				ImportState:             true,
				ImportStateIdFunc:       func(s *terraform.State) (string, error) { return "web", nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// A bad level is refused at plan time.
				Config:      config(`[{ bucket = "media", access = "write" }]`, "null"),
				ExpectError: regexp.MustCompile(`(?s)read.*readwrite`),
			},
			{
				// Removed from the configuration: the access, the user and the keys go.
				Config: config("null", "null"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("enclave_app.web", "bucket_credentials_secret"),
					resource.TestCheckNoResourceAttr("enclave_job.export", "bucket_credentials_secret"),
					storeUser("enclave_app.web"),
					func(s *terraform.State) error {
						if _, err := c.FindResource(context.Background(), api.KindSecret, "web-bucket-credentials"); err == nil {
							return fmt.Errorf("the keys outlived the access")
						}
						return nil
					},
				),
			},
		},
	})
}
