// Package webhook posts to URLs the user gave EnclaveStack: topic subscriptions (ADR-029) and notification
// channels (ADR-039). Redirects aren't followed, each request has a deadline, replies are read at most
// 4 KiB and never kept, and a 2xx reply is delivery.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Timeout bounds one request; BodyLimit is how much of a reply is read.
const (
	Timeout   = 10 * time.Second
	BodyLimit = 4 << 10
)

// SignatureHeader carries the HMAC-SHA256 of the body when the target has a secret.
const SignatureHeader = "X-Enclave-Signature"

// Signature is "sha256=<hex HMAC-SHA256(key, body)>".
func Signature(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Post sends body with headers to url and reports anything but a 2xx as an error. client may be nil.
func Post(ctx context.Context, client *http.Client, url string, body []byte, headers map[string]string) error {
	return PostWithin(ctx, client, Timeout, url, body, headers)
}

// PostWithin is Post with its own deadline (log export batches, ADR-040).
func PostWithin(ctx context.Context, client *http.Client, timeout time.Duration, url string, body []byte, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "EnclaveStack-Webhooks/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = &http.Client{}
	}
	noRedirects := *client
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := noRedirects.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't be reached: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, BodyLimit))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("answered %s", res.Status)
	}
	return nil
}
