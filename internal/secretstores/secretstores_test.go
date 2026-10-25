package secretstores

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/sigv4"
	"github.com/enclavestack/platform/pkg/api"
)

var ctx = context.Background()

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestVaultReadsKVv2(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "s.root" || r.Header.Get("X-Vault-Namespace") != "team" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied for s.root"]}`))
			return
		}
		switch r.URL.Path {
		case "/v1/kv/data/app/stripe":
			_, _ = w.Write([]byte(`{"data":{"data":{"key":"sk_test","n":3},"metadata":{}}}`))
		case "/v1/kv/data/app/single":
			_, _ = w.Write([]byte(`{"data":{"data":{"only":"one"}}}`))
		case "/v1/auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	s, err := New(api.SecretStoreSpec{Provider: api.ProviderVault, Vault: &api.VaultStore{Address: url, Mount: "kv", Namespace: "team"}}, "s.root", nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Fetch(ctx, "app/stripe", "key"); err != nil || v != "sk_test" {
		t.Errorf("field: %q %v", v, err)
	}
	if v, err := s.Fetch(ctx, "app/stripe", "n"); err != nil || v != "3" {
		t.Errorf("number field: %q %v", v, err)
	}
	if v, err := s.Fetch(ctx, "app/single", ""); err != nil || v != "one" {
		t.Errorf("the only field: %q %v", v, err)
	}
	if _, err := s.Fetch(ctx, "app/stripe", ""); err == nil || !strings.Contains(err.Error(), "key, n") {
		t.Errorf("several fields without a name: %v", err)
	}
	if _, err := s.Fetch(ctx, "app/missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := s.Test(ctx); err != nil {
		t.Errorf("test: %v", err)
	}
	bad, _ := New(api.SecretStoreSpec{Provider: api.ProviderVault, Vault: &api.VaultStore{Address: url, Mount: "kv"}}, "s.root", nil, time.Now)
	if err := bad.Test(ctx); err == nil || strings.Contains(err.Error(), "s.root") {
		t.Errorf("a refusal leaks the token or passes: %v", err)
	}
}

func TestOnePasswordFindsVaultAndItemByName(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer op-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/vaults" && r.URL.Query().Get("filter") == `name eq "Shop"`:
			_, _ = w.Write([]byte(`[{"id":"v1","name":"Shop"}]`))
		case r.URL.Path == "/v1/vaults/v1/items" && r.URL.Query().Get("filter") == `title eq "Stripe"`:
			_, _ = w.Write([]byte(`[{"id":"i1","title":"Stripe"}]`))
		case r.URL.Path == "/v1/vaults/v1/items/i1":
			_, _ = w.Write([]byte(`{"fields":[{"id":"username","label":"username","value":"acct"},{"id":"password","label":"password","purpose":"PASSWORD","value":"pw1"},{"id":"api","label":"credential","value":"sk_live"}]}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	s, _ := New(api.SecretStoreSpec{Provider: api.ProviderOnePassword, OnePassword: &api.OnePasswordStore{ConnectURL: url, Vault: "Shop"}}, "op-token", nil, time.Now)
	if v, err := s.Fetch(ctx, "Stripe", ""); err != nil || v != "pw1" {
		t.Errorf("default field is the password: %q %v", v, err)
	}
	if v, err := s.Fetch(ctx, "Stripe", "credential"); err != nil || v != "sk_live" {
		t.Errorf("by label: %q %v", v, err)
	}
	if _, err := s.Fetch(ctx, "Stripe", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing field: %v", err)
	}
	if err := s.Test(ctx); err != nil {
		t.Errorf("test: %v", err)
	}
}

func TestAWSSecretsManagerSignsWithSigV4(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		check, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.String(), bytes.NewReader(body))
		for _, h := range []string{"Content-Type", "X-Amz-Target"} {
			check.Header.Set(h, r.Header.Get(h))
		}
		sigv4.Sign(check, body, "AKID", "aws-secret", "eu-west-1", "secretsmanager", now)
		if check.Header.Get("Authorization") != r.Header.Get("Authorization") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"__type":"InvalidSignatureException","message":"bad signature"}`))
			return
		}
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		switch r.Header.Get("X-Amz-Target") {
		case "secretsmanager.GetSecretValue":
			switch in["SecretId"] {
			case "prod/db":
				_, _ = w.Write([]byte(`{"SecretString":"{\"password\":\"pg-pw\",\"user\":\"app\"}"}`))
			case "plain":
				_, _ = w.Write([]byte(`{"SecretString":"just-text"}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"not found"}`))
			}
		case "secretsmanager.ListSecrets":
			_, _ = w.Write([]byte(`{"SecretList":[]}`))
		}
	})
	creds := `{"accessKey":"AKID","secretKey":"aws-secret"}`
	s, err := New(api.SecretStoreSpec{Provider: api.ProviderAWS, AWS: &api.AWSStore{Region: "eu-west-1", Endpoint: url}}, creds, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Fetch(ctx, "prod/db", "password"); err != nil || v != "pg-pw" {
		t.Errorf("JSON field: %q %v", v, err)
	}
	if v, err := s.Fetch(ctx, "prod/db", ""); err != nil || !strings.Contains(v, "pg-pw") {
		t.Errorf("whole value: %q %v", v, err)
	}
	if v, err := s.Fetch(ctx, "plain", "ignored"); err != nil || v != "just-text" {
		t.Errorf("plain text ignores the field: %q %v", v, err)
	}
	if _, err := s.Fetch(ctx, "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := s.Test(ctx); err != nil {
		t.Errorf("test: %v", err)
	}
	wrong, _ := New(api.SecretStoreSpec{Provider: api.ProviderAWS, AWS: &api.AWSStore{Region: "eu-west-1", Endpoint: url}}, `{"accessKey":"AKID","secretKey":"other"}`, nil, func() time.Time { return now })
	if err := wrong.Test(ctx); err == nil || strings.Contains(err.Error(), "other") {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := New(api.SecretStoreSpec{Provider: api.ProviderAWS, AWS: &api.AWSStore{Region: "eu-west-1"}}, "not json", nil, time.Now); err == nil {
		t.Error("credentials that aren't JSON were accepted")
	}
}

func TestDopplerAndInfisical(t *testing.T) {
	doppler := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dp.st.x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v3/configs/config/secret":
			if r.URL.Query().Get("name") != "STRIPE" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"value":{"raw":"${X}","computed":"sk_doppler"}}`))
		case "/v3/configs/config/secrets/names":
			_, _ = w.Write([]byte(`{"names":["STRIPE"]}`))
		}
	})
	d, _ := New(api.SecretStoreSpec{Provider: api.ProviderDoppler, Doppler: &api.DopplerStore{Endpoint: doppler}}, "dp.st.x", nil, time.Now)
	if v, err := d.Fetch(ctx, "STRIPE", ""); err != nil || v != "sk_doppler" {
		t.Errorf("doppler: %q %v", v, err)
	}
	if err := d.Test(ctx); err != nil {
		t.Errorf("doppler test: %v", err)
	}

	inf := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/universal-auth/login":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["clientSecret"] != "cs" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"bad cs credentials"}`))
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"tok-1"}`))
		case "/api/v3/secrets/raw/DB_URL":
			q := r.URL.Query()
			if r.Header.Get("Authorization") != "Bearer tok-1" || q.Get("workspaceId") != "proj" || q.Get("environment") != "prod" || q.Get("secretPath") != "/" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"secret":{"secretValue":"postgres://x"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	spec := api.SecretStoreSpec{Provider: api.ProviderInfisical, Infisical: &api.InfisicalStore{SiteURL: inf, ProjectID: "proj", Environment: "prod", Path: "/"}}
	s, _ := New(spec, `{"clientId":"ci","clientSecret":"cs"}`, nil, time.Now)
	if v, err := s.Fetch(ctx, "DB_URL", ""); err != nil || v != "postgres://x" {
		t.Errorf("infisical: %q %v", v, err)
	}
	bad, _ := New(spec, `{"clientId":"ci","clientSecret":"wrong-secret"}`, nil, time.Now)
	if err := bad.Test(ctx); err == nil || strings.Contains(err.Error(), "wrong-secret") {
		t.Errorf("infisical wrong secret: %v", err)
	}
}
