package cli_test

import (
	"strings"
	"testing"
)

// --bucket on apps and jobs (L2-002 CLI-53, L2-011).
func TestBucketAccessFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("bucket", "create", "media", "--wait")
	h.ok("bucket", "create", "invoices", "--wait")

	h.ok("app", "create", "storefront", "--image", "nginx:1.27", "--port", "80", "--bucket", "media:read", "--wait")
	r := h.ok("get", "app", "storefront")
	if !strings.Contains(r.out, "media") || !strings.Contains(r.out, "storefront-bucket-credentials") {
		t.Errorf("get after create: %s", r.out)
	}
	h.ok("app", "update", "storefront", "--bucket", "media:readwrite", "--bucket", "invoices:read", "--wait")
	r = h.ok("get", "app", "storefront", "-o", "json")
	if !strings.Contains(r.out, `"access": "readwrite"`) || !strings.Contains(r.out, `"bucket": "invoices"`) {
		t.Errorf("get after update: %s", r.out)
	}
	// The keys are a secret like any other: hidden unless revealed.
	if r := h.ok("secret", "get", "storefront-bucket-credentials"); strings.Contains(r.out, "secretKey") {
		t.Errorf("secret get shows the keys without --reveal: %s", r.out)
	}
	if r := h.ok("secret", "get", "storefront-bucket-credentials", "--reveal"); !strings.Contains(r.out, "secretKey") {
		t.Errorf("secret get --reveal: %s", r.out)
	}
	// A bucket in use can't go; --bucket "" takes all access away, and then it can.
	if r := h.run("bucket", "delete", "invoices", "--yes"); r.code == 0 || !strings.Contains(r.err+r.out, "storefront") {
		t.Errorf("deleting a bucket in use: %d %q %q", r.code, r.out, r.err)
	}
	h.ok("app", "update", "storefront", "--bucket", "", "--wait")
	if r := h.run("secret", "get", "storefront-bucket-credentials"); r.code == 0 {
		t.Error("the keys outlived the access")
	}
	h.ok("bucket", "delete", "invoices", "--yes", "--wait")

	// Jobs: their own access when they run an image.
	h.ok("job", "create", "export", "--schedule", "@daily", "--image", "busybox:1.36", "--bucket", "media:readwrite", "--wait", "--", "true")
	if r := h.ok("get", "job", "export"); !strings.Contains(r.out, "export-bucket-credentials") {
		t.Errorf("get job: %s", r.out)
	}
	h.ok("job", "update", "export", "--bucket", "media:read", "--wait")

	// Mistakes are said plainly.
	for _, bad := range []string{"media", "media:write", ":read"} {
		if r := h.run("app", "create", "bad", "--image", "nginx", "--bucket", bad); r.code == 0 || !strings.Contains(r.err, "NAME:read or NAME:readwrite") {
			t.Errorf("--bucket %q: %d %q", bad, r.code, r.err)
		}
	}
	if r := h.run("app", "create", "nowhere", "--image", "nginx", "--bucket", "missing:read"); r.code == 0 || !strings.Contains(r.err, "no bucket named missing") {
		t.Errorf("an unknown bucket: %d %q", r.code, r.err)
	}
}
