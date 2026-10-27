package gate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/pkg/api"
)

// A Git webhook on a public address reaches the same call as the API's, only from the edge proxy, and
// keeps the call's error codes (ADR-056, L3-002 §18.2).
func TestGitHookOnPublicAddresses(t *testing.T) {
	var gotID, gotBody, gotEvent string
	h := &Handler{EdgeKey: "edge-key", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hook: func(_ context.Context, id string, g service.GitHook) (api.HookResult, error) {
			gotID, gotBody, gotEvent = id, string(g.Body), g.Header.Get("X-GitHub-Event")
			if id == "res_unknown" {
				return api.HookResult{}, api.NewError(api.CodeNotFound, "No such app.")
			}
			return api.HookResult{Result: api.HookCreated, Preview: "web-pr-7"}, nil
		}}
	post := func(path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"action":"opened"}`))
		r.Header.Set("X-GitHub-Event", "pull_request")
		if key != "" {
			r.Header.Set(HeaderEdgeKey, key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := post(api.PublicHookPath+"res_web", "edge-key")
	var out api.HookResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Result != api.HookCreated || out.Preview != "web-pr-7" {
		t.Fatalf("hook: %d %s", w.Code, w.Body)
	}
	if gotID != "res_web" || gotBody != `{"action":"opened"}` || gotEvent != "pull_request" {
		t.Fatalf("the call got %q %q %q", gotID, gotBody, gotEvent)
	}

	// Only the edge proxy reaches the gate; a nested or empty ID is no app.
	for _, c := range []struct{ path, key string }{{api.PublicHookPath + "res_web", ""}, {api.PublicHookPath + "res_web", "wrong"}, {api.PublicHookPath + "a/b", "edge-key"}, {api.PublicHookPath, "edge-key"}} {
		gotID = ""
		if w := post(c.path, c.key); w.Code != http.StatusNotFound || gotID != "" {
			t.Errorf("%s with key %q: %d, call reached %q", c.path, c.key, w.Code, gotID)
		}
	}

	// The call's errors keep their status and code.
	w = post(api.PublicHookPath+"res_unknown", "edge-key")
	var env api.ErrorEnvelope
	if w.Code != http.StatusNotFound || json.Unmarshal(w.Body.Bytes(), &env) != nil || env.Error == nil || env.Error.Code != api.CodeNotFound {
		t.Fatalf("unknown app: %d %s", w.Code, w.Body)
	}

	// Without a hook call configured, the path is not served.
	h.Hook = nil
	if w := post(api.PublicHookPath+"res_web", "edge-key"); w.Code != http.StatusNotFound {
		t.Fatalf("no hook: %d", w.Code)
	}
}
