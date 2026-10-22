package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/secure-io/sio-go"
	"golang.org/x/crypto/argon2"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/sigv4"
)

// Project bucket credentials (ADR-035): each project other than default gets an object store user whose
// policy allows only its buckets. MinIO's admin API is called directly: three signed requests, one of
// them with a body encrypted with the root secret (argon2id key, ChaCha20-Poly1305 in the DARE format of
// github.com/secure-io/sio-go), as the admin API requires for credentials.

// minioAdmin calls MinIO's admin API as the root user.
type minioAdmin struct {
	base   string // http://host:port
	access string
	secret string
	http   *http.Client
}

const adminPrefix = "/minio/admin/v3"

func (a minioAdmin) call(ctx context.Context, method, path string, q url.Values, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := a.base + adminPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Content-Sha256", sigv4.SHA256Hex(body))
	sigv4.Sign(req, body, a.access, a.secret, "us-east-1", "s3", time.Now())
	res, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("the object store's admin API can't be reached: %w", err)
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("the object store refused %s %s: %s %s", method, path, res.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// sealForAdmin encrypts data the way MinIO's admin API expects: salt(32) | AEAD id | nonce | DARE stream.
func sealForAdmin(password string, data []byte) ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	stream, err := sio.ChaCha20Poly1305.Stream(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, stream.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	out.WriteByte(0x01) // argon2id + ChaCha20-Poly1305
	out.Write(nonce)
	w := stream.EncryptWriter(&out, nonce, nil)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// projectPolicy allows everything on the named buckets and their objects, and nothing else.
func projectPolicy(buckets []string) []byte {
	var resources []string
	for _, b := range buckets {
		resources = append(resources, "arn:aws:s3:::"+b, "arn:aws:s3:::"+b+"/*")
	}
	doc, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{
		{"Effect": "Allow", "Action": []string{"s3:*"}, "Resource": resources},
	}})
	return doc
}

// SetProjectStoreAccess implements engine.Driver: the project's user reaches exactly buckets; with none,
// the user and its policy go.
func (d *Driver) SetProjectStoreAccess(ctx context.Context, user engine.ObjectStore, buckets []string, root engine.ObjectStore) error {
	c, err := d.inspect(ctx, engine.ObjectStoreName)
	if err != nil {
		return err
	}
	if c == nil || c.State == nil || !c.State.Running {
		return fmt.Errorf("the object store isn't running")
	}
	a := minioAdmin{base: "http://127.0.0.1:" + strconv.Itoa(hostPort(c, engine.ObjectStorePort)), access: root.AccessKey, secret: root.SecretKey, http: &http.Client{}}
	policy := user.AccessKey // one policy per project user, named after it
	if len(buckets) == 0 {
		_ = a.call(ctx, http.MethodDelete, "/remove-user", url.Values{"accessKey": {user.AccessKey}}, nil)
		_ = a.call(ctx, http.MethodDelete, "/remove-canned-policy", url.Values{"name": {policy}}, nil)
		return nil
	}
	buckets = slices.Sorted(slices.Values(buckets))
	if err := a.call(ctx, http.MethodPut, "/add-canned-policy", url.Values{"name": {policy}}, projectPolicy(buckets)); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"secretKey": user.SecretKey, "status": "enabled"})
	sealed, err := sealForAdmin(root.SecretKey, body)
	if err != nil {
		return err
	}
	if err := a.call(ctx, http.MethodPut, "/add-user", url.Values{"accessKey": {user.AccessKey}}, sealed); err != nil {
		return err
	}
	return a.call(ctx, http.MethodPut, "/set-user-or-group-policy", url.Values{"policyName": {policy}, "userOrGroup": {user.AccessKey}, "isGroup": {"false"}}, nil)
}
