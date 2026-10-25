package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// Bucket access on apps and jobs (L2-011 BKA-01, BKA-04, BKA-05; L3-002 §12.4).
func TestBucketAccessValidation(t *testing.T) {
	app := func(extra string) (any, []FieldError) {
		s, errs, err := NormalizeSpec(KindApp, json.RawMessage(`{"image":"nginx:1.27"`+extra+`}`))
		if err != nil {
			t.Fatalf("%s: %v", extra, err)
		}
		return s, errs
	}
	s, errs := app(`,"bucketAccess":[{"bucket":"media","access":"read"},{"bucket":"invoices","access":"readwrite"}]`)
	if len(errs) != 0 {
		t.Fatalf("valid access rejected: %v", errs)
	}
	if g := s.(AppSpec).BucketAccess; len(g) != 2 || g[1] != (BucketGrant{Bucket: "invoices", Access: BucketReadWrite}) {
		t.Errorf("grants: %+v", g)
	}
	for extra, field := range map[string]string{
		`,"bucketAccess":[{"bucket":"media","access":"write"}]`:                                        "spec.bucketAccess[0].access",
		`,"bucketAccess":[{"bucket":"media"}]`:                                                         "spec.bucketAccess[0].access",
		`,"bucketAccess":[{"bucket":"Media","access":"read"}]`:                                         "spec.bucketAccess[0].bucket",
		`,"bucketAccess":[{"bucket":"media","access":"read"},{"bucket":"media","access":"readwrite"}]`: "spec.bucketAccess[1].bucket",
		`,"bucketAccess":[{"bucket":"media","access":"read"}],"env":{"AWS_ACCESS_KEY_ID":"x"}`:         "spec.env.AWS_ACCESS_KEY_ID",
		`,"bucketAccess":[{"bucket":"media","access":"read"}],"secretEnv":{"AWS_ENDPOINT_URL":"s"}`:    "spec.secretEnv.AWS_ENDPOINT_URL",
	} {
		if _, errs := app(extra); codes(errs)[field] == "" {
			t.Errorf("%s: want an error on %s, got %v", extra, field, errs)
		}
	}
	many := make([]string, BucketAccessMax+1)
	for i := range many {
		many[i] = `{"bucket":"b` + strings.Repeat("x", i+2) + `","access":"read"}`
	}
	if _, errs := app(`,"bucketAccess":[` + strings.Join(many, ",") + `]`); codes(errs)["spec.bucketAccess"] != FieldTooLong {
		t.Errorf("11 buckets: %v", errs)
	}
	// Without access the AWS names are ordinary variables.
	if _, errs := app(`,"env":{"AWS_REGION":"eu-west-1"}`); len(errs) != 0 {
		t.Errorf("AWS_REGION without bucket access: %v", errs)
	}

	job := func(spec string) []FieldError {
		_, errs, err := NormalizeSpec(KindJob, json.RawMessage(spec))
		if err != nil {
			t.Fatal(err)
		}
		return errs
	}
	if errs := job(`{"schedule":"@daily","image":"busybox","bucketAccess":[{"bucket":"media","access":"read"}]}`); len(errs) != 0 {
		t.Errorf("an image job with access: %v", errs)
	}
	if errs := job(`{"schedule":"@daily","app":"web","bucketAccess":[{"bucket":"media","access":"read"}]}`); codes(errs)["spec.bucketAccess"] != FieldNotAllowed {
		t.Errorf("an app job with access of its own: %v", errs)
	}
}

func TestBucketCredentialsSecretName(t *testing.T) {
	if got := BucketCredentialsSecretFor("uploader"); got != "uploader-bucket-credentials" {
		t.Errorf("got %q", got)
	}
}
