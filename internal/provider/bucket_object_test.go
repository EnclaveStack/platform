package provider_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/pkg/api"
)

// enclave_bucket_object with real OpenTofu (L2-003 TF-50, L2-013).
func TestAccBucketObject(t *testing.T) {
	_, c := boot(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "intro.mp4")
	if err := os.WriteFile(file, []byte("first version"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := func(body string) string {
		return fmt.Sprintf(`
resource "enclave_bucket" "media" { name = "media" }
resource "enclave_bucket_object" "intro" {
  bucket = enclave_bucket.media.name
  key    = "videos/intro.mp4"
%s
}
resource "enclave_bucket_object" "note" {
  bucket  = enclave_bucket.media.name
  key     = "notes/hello.txt"
  content = "hello"
}
`, body)
	}
	stored := func(key, want string, tags map[string]string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			ctx := context.Background()
			b, err := c.FindResource(ctx, api.KindBucket, "media")
			if err != nil {
				return err
			}
			info, body, err := c.GetObject(ctx, b.ID, key)
			if err != nil {
				return err
			}
			defer body.Close()
			got, _ := io.ReadAll(body)
			if string(got) != want || fmt.Sprint(info.Tags) != fmt.Sprint(tags) {
				return fmt.Errorf("%s holds %q tags %v", key, got, info.Tags)
			}
			return nil
		}
	}
	src := fmt.Sprintf("  source      = %q\n  source_hash = filesha256(%q)\n  tags        = { video-id = \"v1\" }", file, file)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(src),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_bucket_object.intro", "id", "media/videos/intro.mp4"),
					resource.TestCheckResourceAttr("enclave_bucket_object.intro", "size", "13"),
					resource.TestCheckResourceAttr("enclave_bucket_object.intro", "content_type", "video/mp4"),
					resource.TestCheckResourceAttr("enclave_bucket_object.note", "content_type", "text/plain; charset=utf-8"),
					stored("videos/intro.mp4", "first version", map[string]string{"video-id": "v1"}),
					stored("notes/hello.txt", "hello", map[string]string{}),
				),
			},
			{
				// The file changes: its hash changes, so it is uploaded again in place.
				PreConfig: func() { _ = os.WriteFile(file, []byte("second version!"), 0o644) },
				Config:    config(src),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_bucket_object.intro", "size", "15"),
					stored("videos/intro.mp4", "second version!", map[string]string{"video-id": "v1"}),
				),
			},
			{ResourceName: "enclave_bucket_object.note", ImportState: true, ImportStateId: "media/notes/hello.txt", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"content", "tags"}},
			{
				Config:      config(fmt.Sprintf("  source = %q\n  content = \"both\"", file)),
				ExpectError: regexp.MustCompile(`only one\) of \[source,content\]`),
			},
			{
				Config:      config(`  content = "x"` + "\n" + `  tags = { "Video_ID" = "v1" }`),
				ExpectError: regexp.MustCompile(`lowercase letters`),
			},
			{Config: config(src)},
		},
	})
}
