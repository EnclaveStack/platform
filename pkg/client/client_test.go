package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

func fastClient(url string) (*Client, *[]time.Duration) {
	c := New(url, "es_pat_test")
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	return c, &waits
}

func TestRetriesTransientStatusesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer es_pat_test" || r.Header.Get(api.HeaderCSRF) != "1" {
			t.Errorf("missing auth or CSRF header")
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"name":"m","engine":{"driver":"fake"}}`))
	}))
	defer srv.Close()
	c, waits := fastClient(srv.URL)
	m, err := c.Machine(context.Background())
	if err != nil || m.Name != "m" || calls.Load() != 3 || len(*waits) != 2 {
		t.Fatalf("machine=%+v err=%v calls=%d waits=%v", m, err, calls.Load(), *waits)
	}
}

func TestHonoursRetryAfterAndGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMITED","message":"slow down","requestId":"req_1"}}`))
	}))
	defer srv.Close()
	c, waits := fastClient(srv.URL)
	_, err := c.GetResource(context.Background(), "x")
	if Code(err) != api.CodeRateLimited || calls.Load() != DefaultMaxAttempts {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	for _, w := range *waits {
		if w != 7*time.Second {
			t.Errorf("wait %s, want Retry-After 7s", w)
		}
	}
	var ae *api.Error
	if e, ok := err.(*api.Error); ok {
		ae = e
	}
	if ae == nil || ae.RequestID != "req_1" || ae.RetryAfterSeconds != 7 {
		t.Errorf("error details lost: %+v", ae)
	}
}

func TestCreateSendsIdempotencyKeyAndIsRetried(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get(api.HeaderIdempotencyKey))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"db_x","kind":"database","name":"orders"}`))
	}))
	defer srv.Close()
	c, _ := fastClient(srv.URL)
	r, err := c.CreateResource(context.Background(), api.KindDatabase, "orders", api.DatabaseSpec{})
	if err != nil || r.ID != "db_x" {
		t.Fatalf("%+v %v", r, err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("idempotency keys %v: want the same non-empty key on the retry", keys)
	}
}

func TestPatchWithoutIfMatchAndActionsAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := fastClient(srv.URL)
	_, _ = c.UpdateResource(context.Background(), "x", map[string]string{"image": "a"}, nil)
	_, _ = c.Action(context.Background(), "x", api.ActionRestart)
	if calls.Load() != 2 {
		t.Errorf("non-idempotent calls retried: %d requests", calls.Load())
	}
}

func TestNetworkErrorNamesTheServer(t *testing.T) {
	c, _ := fastClient("http://127.0.0.1:1")
	_, err := c.Machine(context.Background())
	var ne *NetworkError
	if err == nil || !asNetwork(err, &ne) || ne.URL != "http://127.0.0.1:1" {
		t.Fatalf("want NetworkError, got %v", err)
	}
}

func asNetwork(err error, target **NetworkError) bool {
	n, ok := err.(*NetworkError)
	if ok {
		*target = n
	}
	return ok
}

func TestNonEnvelopeErrorsAreMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) }))
	defer srv.Close()
	c, _ := fastClient(srv.URL)
	if _, err := c.GetResource(context.Background(), "x"); !IsNotFound(err) {
		t.Errorf("plain 404 not mapped: %v", err)
	}
}
