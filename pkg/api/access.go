package api

import (
	"fmt"
	"slices"
)

// Per-app bucket access (L2-011, L3-002 §12, ADR-049): an app or a job names the buckets it may reach and
// how, and gets an object store user of its own that reaches exactly those.

// BucketGrant is one bucket an app or job may reach, and how.
type BucketGrant struct {
	Bucket string `json:"bucket"`
	Access string `json:"access"`
}

// Bucket access levels (BKA-01).
const (
	BucketRead      = "read"
	BucketReadWrite = "readwrite"
)

// BucketAccessLevels lists the levels.
var BucketAccessLevels = []string{BucketRead, BucketReadWrite}

// BucketAccessMax is how many buckets one app or job may name.
const BucketAccessMax = 10

// OutputBucketCredentialsSecret names the managed secret holding an app's or job's own keys (BKA-03).
const OutputBucketCredentialsSecret = "bucketCredentialsSecret"

// BucketCredentialsSecretFor is the managed secret holding a resource's own object store keys.
func BucketCredentialsSecretFor(name string) string { return name + "-bucket-credentials" }

// BucketAccessRegion is the region the variables name; the object store ignores it but SDKs want one.
const BucketAccessRegion = "us-east-1"

// BucketAccessEnv are the variables an app or job with bucket access receives (BKA-04), in order: its
// keys, the region, and the object store's address for S3 and for every AWS service.
var BucketAccessEnv = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"}

// validateBucketAccess checks the grants and that env and secretEnv leave the variables alone; existence
// is the service's job (BKA-07).
func validateBucketAccess(grants []BucketGrant, env, secretEnv map[string]string) []FieldError {
	var errs []FieldError
	if len(grants) > BucketAccessMax {
		errs = append(errs, FieldError{Field: "spec.bucketAccess", Code: FieldTooLong, Message: fmt.Sprintf("Name at most %d buckets.", BucketAccessMax)})
	}
	for i, g := range grants {
		f := fmt.Sprintf("spec.bucketAccess[%d]", i)
		switch {
		case ValidateName(g.Bucket) != nil:
			errs = append(errs, FieldError{Field: f + ".bucket", Code: FieldInvalidFormat, Message: "Name an existing bucket."})
		case slices.IndexFunc(grants, func(o BucketGrant) bool { return o.Bucket == g.Bucket }) < i:
			errs = append(errs, FieldError{Field: f + ".bucket", Code: FieldNotAllowed, Message: "This bucket is already in the list."})
		}
		if !slices.Contains(BucketAccessLevels, g.Access) {
			errs = append(errs, FieldError{Field: f + ".access", Code: FieldNotAllowed, Message: "Choose read or readwrite."})
		}
	}
	if len(grants) == 0 {
		return errs
	}
	for _, k := range BucketAccessEnv {
		if _, ok := env[k]; ok {
			errs = append(errs, FieldError{Field: "spec.env." + k, Code: FieldNotAllowed, Message: "With bucket access, EnclaveStack sets this variable."})
		}
		if _, ok := secretEnv[k]; ok {
			errs = append(errs, FieldError{Field: "spec.secretEnv." + k, Code: FieldNotAllowed, Message: "With bucket access, EnclaveStack sets this variable."})
		}
	}
	return errs
}
