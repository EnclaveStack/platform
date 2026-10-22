package sigv4

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS's published examples: "get-vanilla" from the Signature Version 4 test suite, and the IAM
// ListUsers request from the "Create a signed AWS API request" guide. Both use the documentation
// credentials and 2015-08-30T12:36:00Z.
const (
	docAccessKey = "AKIDEXAMPLE"
	docSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

var docTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func signature(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	_, sig, _ := strings.Cut(auth, "Signature=")
	return sig
}

func TestSignatureMatchesAWSTestSuiteGetVanilla(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	Sign(req, nil, docAccessKey, docSecretKey, "us-east-1", "service", docTime)
	if got := signature(req); got != "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31" {
		t.Errorf("signature %s (Authorization %q)", got, req.Header.Get("Authorization"))
	}
	if !strings.Contains(req.Header.Get("Authorization"), "Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date,") {
		t.Errorf("authorization %q", req.Header.Get("Authorization"))
	}
}

func TestSignatureMatchesAWSGuideIAMListUsers(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	Sign(req, nil, docAccessKey, docSecretKey, "us-east-1", "iam", docTime)
	if got := signature(req); got != "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7" {
		t.Errorf("signature %s", got)
	}
}
