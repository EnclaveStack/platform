package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	storepkg "github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// External secret stores (L2-009 EXT-*, ADR-041), against a fake Vault.

type fakeVault struct {
	mu    sync.Mutex
	value string
	down  bool
}

func (v *fakeVault) set(value string, down bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.value, v.down = value, down
}

func (v *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("X-Vault-Token") != "s.good" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		return
	}
	switch r.URL.Path {
	case "/v1/auth/token/lookup-self":
		_, _ = w.Write([]byte(`{"data":{}}`))
	case "/v1/secret/data/shop/stripe":
		_, _ = fmt.Fprintf(w, `{"data":{"data":{"key":%q}}}`, v.value)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestSecretsSyncFromAStoreAndKeepTheirValueWhenItIsDown(t *testing.T) {
	ctx := context.Background()
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	vault := &fakeVault{value: "sk_test_1"}
	srv := httptest.NewServer(vault)
	t.Cleanup(srv.Close)
	mk := func(kind api.Kind, name string, spec any) api.Resource {
		t.Helper()
		r, err := c.CreateResource(ctx, kind, name, spec)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r
	}
	testenv.WaitReady(t, c, mk(api.KindSecret, "vault-token", api.SecretSpec{Value: "s.good"}).ID)
	testenv.WaitReady(t, c, mk(api.KindSecret, "wrong-token", api.SecretSpec{Value: "s.bad"}).ID)
	store := testenv.WaitReady(t, c, mk(api.KindSecretStore, "vault", api.SecretStoreSpec{Provider: api.ProviderVault, Vault: &api.VaultStore{Address: srv.URL, TokenSecret: "vault-token"}}).ID)
	if res, err := c.Test(ctx, store.ID); err != nil || !res.OK {
		t.Errorf("store test: %+v %v", res, err)
	}

	// Wrong credentials fail the store with the provider's answer and never the token (EXT-07).
	bad := mk(api.KindSecretStore, "bad", api.SecretStoreSpec{Provider: api.ProviderVault, Vault: &api.VaultStore{Address: srv.URL, TokenSecret: "wrong-token"}})
	testenv.Eventually(t, 5*time.Second, "the bad store to fail", func() bool {
		r, _ := c.GetResource(ctx, bad.ID)
		return r.Status.Phase == api.PhaseFailed
	})
	if r, _ := c.GetResource(ctx, bad.ID); !strings.Contains(r.Status.Message, "403") || strings.Contains(r.Status.Message, "s.bad") {
		t.Errorf("bad store message: %q", r.Status.Message)
	}

	sec := testenv.WaitReady(t, c, mk(api.KindSecret, "stripe", api.SecretSpec{Source: &api.SecretSource{Store: "vault", Key: "shop/stripe", Field: "key"}}).ID)
	if v, err := c.RevealSecret(ctx, sec.ID); err != nil || v != "sk_test_1" {
		t.Fatalf("synced value: %q %v", v, err)
	}
	if !strings.Contains(string(sec.Spec), `"store":"vault"`) || strings.Contains(string(sec.Spec), "sk_test") || sec.Outputs["store"] != "vault" {
		t.Errorf("synced secret as read: %s %v", sec.Spec, sec.Outputs)
	}

	// A rotation arrives on refresh, as version 2 with an event (EXT-03, EXT-04).
	vault.set("sk_test_2", false)
	if _, err := c.Action(ctx, sec.ID, api.ActionRefresh); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "the new value", func() bool {
		v, _ := c.RevealSecret(ctx, sec.ID)
		return v == "sk_test_2"
	})
	if r, _ := c.GetResource(ctx, sec.ID); fmt.Sprint(r.Outputs["version"]) != "2" {
		t.Errorf("version after a rotation: %v", r.Outputs)
	}

	// The store goes down: the secret keeps its value and says why (EXT-05).
	vault.set("sk_test_3", true)
	if _, err := c.Action(ctx, sec.ID, api.ActionRefresh); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "the message", func() bool {
		r, _ := c.GetResource(ctx, sec.ID)
		return strings.Contains(r.Status.Message, "Keeping the last value")
	})
	if v, _ := c.RevealSecret(ctx, sec.ID); v != "sk_test_2" {
		t.Errorf("value while the store is down: %q", v)
	}

	// The engine is down: a due refresh still reads the store, which needs no engine (EXT-05, ADR-041).
	vault.set("sk_test_4", false)
	env.Fake.SetAvailable(false)
	past := time.Now().Add(-time.Minute)
	if err := env.Store.WithTx(ctx, func(q *storepkg.Queries) error { return q.SetNextRun(ctx, sec.ID, &past) }); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "a refresh while the engine is down", func() bool {
		v, _ := c.RevealSecret(ctx, sec.ID)
		return v == "sk_test_4"
	})
	env.Fake.SetAvailable(true)

	// A synced value can't be set; a store in use can't go; a local secret can't be refreshed.
	_, err := c.UpdateResource(ctx, sec.ID, map[string]any{"value": "x"}, nil)
	if client.Code(err) != api.CodeValidationFailed {
		t.Errorf("setting a synced value: %v", err)
	}
	if _, err := c.DeleteResource(ctx, store.ID, nil); client.Code(err) != api.CodeResourceInUse {
		t.Errorf("deleting a store in use: %v", err)
	}
	tok, _ := c.FindResource(ctx, api.KindSecret, "vault-token")
	if _, err := c.Action(ctx, tok.ID, api.ActionRefresh); client.Code(err) != api.CodeActionNotSupported {
		t.Errorf("refreshing a local secret: %v", err)
	}
	// A key that isn't there fails the secret (it never had a value).
	missing := mk(api.KindSecret, "missing", api.SecretSpec{Source: &api.SecretSource{Store: "vault", Key: "shop/nope"}})
	vault.set("sk", false)
	testenv.Eventually(t, 5*time.Second, "the missing key to fail", func() bool {
		r, _ := c.GetResource(ctx, missing.ID)
		return r.Status.Phase == api.PhaseFailed && strings.Contains(r.Status.Message, "shop/nope")
	})
}
