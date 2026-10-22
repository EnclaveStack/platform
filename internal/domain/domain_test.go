package domain

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

func TestNewResourceIDShapeAndUniqueness(t *testing.T) {
	pattern := regexp.MustCompile(`^db_[a-z2-7]{16}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewResourceID(api.KindDatabase)
		if !pattern.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestPhaseTransitions(t *testing.T) {
	legal := [][2]api.Phase{{api.PhasePending, api.PhaseProvisioning}, {api.PhaseProvisioning, api.PhaseReady}, {api.PhaseReady, api.PhaseUpdating},
		{api.PhaseUpdating, api.PhaseReady}, {api.PhaseFailed, api.PhaseProvisioning}, {api.PhaseReady, api.PhaseDeleting}, {api.PhaseReady, api.PhaseReady}, {api.PhaseReady, api.PhaseFailed},
		{api.PhaseProvisioning, api.PhaseFailed}, {api.PhaseReady, api.PhaseProvisioning}}
	for _, c := range legal {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%s → %s should be legal", c[0], c[1])
		}
	}
	illegal := [][2]api.Phase{{api.PhaseDeleting, api.PhaseReady}, {api.PhaseDeleting, api.PhaseFailed}, {api.PhaseFailed, api.PhaseReady}}
	for _, c := range illegal {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%s → %s should be illegal", c[0], c[1])
		}
	}
	r := &Resource{Phase: api.PhaseDeleting}
	if err := r.SetPhase(api.PhaseReady, "", time.Now()); err == nil {
		t.Error("SetPhase allowed deleting → ready")
	}
	now := time.Now()
	r = &Resource{Phase: api.PhasePending}
	if err := r.SetPhase(api.PhaseProvisioning, "go", now); err != nil || r.LastTransitionAt != now || r.Message != "go" {
		t.Errorf("SetPhase: %v %+v", err, r)
	}
}

func TestToAPINeverExposesSecretMaterial(t *testing.T) {
	r := &Resource{ID: "sec_x", Kind: api.KindSecret, Spec: json.RawMessage(`{"value":"leak"}`), SecretCiphertext: []byte("ct")}
	out := r.ToAPI()
	if string(out.Spec) != "{}" {
		t.Errorf("secret spec exposed: %s", out.Spec)
	}
	b, _ := json.Marshal(out)
	if regexp.MustCompile(`leak|Y3Q`).Match(b) { // the value, or the ciphertext base64-encoded
		t.Errorf("secret material in API JSON: %s", b)
	}
}

func TestMemoryReserved(t *testing.T) {
	cases := []struct {
		r    Resource
		want int
	}{
		{Resource{Kind: api.KindApp, Spec: json.RawMessage(`{"image":"x","memoryMiB":512}`)}, 512},
		{Resource{Kind: api.KindApp, Spec: json.RawMessage(`{}`)}, api.AppMemoryDefaultMiB},
		{Resource{Kind: api.KindDatabase}, DatabaseMemoryMiB},
		{Resource{Kind: api.KindCache, Spec: json.RawMessage(`{"maxMemoryMiB":128}`)}, 128 + CacheOverheadMiB},
		{Resource{Kind: api.KindSecret}, 0},
		{Resource{Kind: api.KindBucket}, 0},
	}
	for _, c := range cases {
		if got := MemoryReservedMiB(&c.r); got != c.want {
			t.Errorf("%s: got %d want %d", c.r.Kind, got, c.want)
		}
	}
}

func TestSecretRefsAndNames(t *testing.T) {
	refs := SecretRefs(api.AppSpec{SecretEnv: map[string]string{"B": "two", "A": "one", "C": "one"}})
	if len(refs) != 2 || refs[0] != "one" || refs[1] != "two" {
		t.Errorf("refs %v", refs)
	}
	if DatabaseIdentifier("orders-db") != "orders_db" || ManagedPasswordSecret("orders") != "orders-password" {
		t.Error("naming helpers wrong")
	}
}

func TestToAPIShowsASyncedSecretsSourceOnly(t *testing.T) {
	r := &Resource{ID: "sec_x", Kind: api.KindSecret, Spec: json.RawMessage(`{"value":"leak","source":{"store":"vault","key":"app/db"}}`)}
	if got := string(r.ToAPI().Spec); got != `{"source":{"store":"vault","key":"app/db"}}` {
		t.Errorf("spec = %s", got)
	}
}

func TestQualifiedAndBrokerKeysKeepDefaultNames(t *testing.T) {
	for _, c := range []struct{ project, qualified, key string }{{"", "web", "web"}, {api.DefaultProject, "web", "web"}, {"shop", "web.shop", "shop_web"}} {
		if q := Qualified(c.project, "web"); q != c.qualified {
			t.Errorf("Qualified(%q) = %q", c.project, q)
		}
		if k := BrokerKey(c.project, "web"); k != c.key {
			t.Errorf("BrokerKey(%q) = %q", c.project, k)
		}
		p, n := SplitBrokerKey(c.key)
		if want := c.project; (want == "" && p != api.DefaultProject) || (want != "" && p != want) || n != "web" {
			t.Errorf("SplitBrokerKey(%q) = %q, %q", c.key, p, n)
		}
	}
}
