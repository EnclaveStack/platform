package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// The auth and security list from L5-002 §3, one test per rule.

// protectedRoutes reads every non-public route from server.go, so a new route is covered the day it's
// added (path parameters get placeholder values).
func protectedRoutes(t *testing.T) [][2]string {
	t.Helper()
	src, err := os.ReadFile(repoFile("internal", "httpapi", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	fill := strings.NewReplacer("{id}", "x", "{runId}", "run_x", "{action}", "restart", "{name}", "default", "{userId}", "usr_x", "{tokenId}", "tok_x", "{emailId}", "x")
	var out [][2]string
	for _, m := range regexp.MustCompile(`s\.route\(mux, "([A-Z]+) (/[^"]*)", (\w+),`).FindAllStringSubmatch(string(src), -1) {
		if m[3] != "public" {
			out = append(out, [2]string{m[1], fill.Replace(m[2])})
		}
	}
	if len(out) < 15 {
		t.Fatalf("found only %d protected routes in server.go; has the route() call changed shape?", len(out))
	}
	return out
}

func TestEveryProtectedRouteRequiresCredentials(t *testing.T) {
	env := testenv.Start(t)
	env.Setup(t)
	anon := raw{t: t, base: env.URL}
	for _, rt := range protectedRoutes(t) {
		rep := anon.do(rt[0], rt[1], nil, nil)
		if rep.status != http.StatusUnauthorized || rep.errCode(t) != api.CodeUnauthenticated {
			t.Errorf("%s %s without credentials: %d", rt[0], rt[1], rep.status)
		}
	}
	bogus := raw{t: t, base: env.URL, token: "es_pat_notarealtoken"}
	if rep := bogus.do("GET", "/api/v1/machine", nil, nil); rep.status != 401 {
		t.Errorf("unknown token: %d", rep.status)
	}
	if rep := anon.do("GET", "/api/v1/machine", nil, map[string]string{"Authorization": "Basic YTpi"}); rep.status != 401 {
		t.Errorf("basic auth: %d", rep.status)
	}
}

func TestSetupRequiresTheCodeAndHappensOnce(t *testing.T) {
	env := testenv.Start(t)
	r := raw{t: t, base: env.URL}
	if len(env.SetupCode) != 14 {
		t.Fatalf("setup code %q", env.SetupCode)
	}
	var st api.SetupStatus
	r.do("GET", "/api/v1/setup", nil, nil).json(t, &st)
	if !st.Required {
		t.Fatal("fresh install should require setup")
	}
	if rep := r.do("POST", "/api/v1/session", map[string]string{"email": "a@b.c", "password": "whatever-123"}, nil); rep.status != 409 || rep.errCode(t) != api.CodeSetupRequired {
		t.Errorf("sign-in before setup: %d", rep.status)
	}
	body := map[string]string{"setupCode": "AAAA-BBBB-CCCC", "machineName": "M", "email": "a@b.c", "password": "long-enough-pw"}
	if rep := r.do("POST", "/api/v1/setup", body, nil); rep.status != 403 || rep.errCode(t) != api.CodeInvalidSetupCode {
		t.Errorf("wrong code: %d %s", rep.status, rep.body)
	}
	body["setupCode"] = strings.ToLower(strings.ReplaceAll(env.SetupCode, "-", " "))
	rep := r.do("POST", "/api/v1/setup", body, nil)
	if rep.status != 201 {
		t.Fatalf("setup: %d %s", rep.status, rep.body)
	}
	cookie := rep.header.Get("Set-Cookie")
	for _, attr := range []string{api.SessionCookie + "=", "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, attr) {
			t.Errorf("session cookie %q lacks %s", cookie, attr)
		}
	}
	if rep := r.do("POST", "/api/v1/setup", body, nil); rep.status != 409 || rep.errCode(t) != api.CodeSetupComplete {
		t.Errorf("second setup: %d", rep.status)
	}
	if strings.Contains(env.Logs.String(), env.SetupCode) {
		t.Error("setup code written to the log stream through slog (it must only be printed by the CLI)")
	}
}

func TestSetupCodeRotatesOnRestartUntilSetupIsDone(t *testing.T) {
	env := testenv.Start(t)
	first := env.SetupCode
	env = env.Restart(t)
	if env.SetupCode == "" || env.SetupCode == first {
		t.Fatalf("restart issued %q after %q", env.SetupCode, first)
	}
	r := raw{t: t, base: env.URL}
	old := map[string]string{"setupCode": first, "machineName": "M", "email": "a@b.c", "password": "long-enough-pw"}
	if rep := r.do("POST", "/api/v1/setup", old, nil); rep.status != 403 {
		t.Errorf("old code still works: %d", rep.status)
	}
	env.Setup(t)
	env = env.Restart(t)
	if env.SetupCode != "" {
		t.Error("a set-up machine printed a new setup code")
	}
}

func TestSignInThrottlingAndUniformErrors(t *testing.T) {
	env := testenv.Start(t)
	env.Setup(t)
	r := raw{t: t, base: env.URL}
	unknown := r.do("POST", "/api/v1/session", map[string]string{"email": "nobody@example.com", "password": "wrong-password"}, nil)
	wrong := r.do("POST", "/api/v1/session", map[string]string{"email": testenv.AdminEmail, "password": "wrong-password"}, nil)
	if unknown.status != 401 || wrong.status != 401 || unknown.errCode(t) != wrong.errCode(t) {
		t.Fatalf("unknown=%d wrong=%d", unknown.status, wrong.status)
	}
	if strings.Replace(string(unknown.body), unknown.header.Get(api.HeaderRequestID), "", 1) != strings.Replace(string(wrong.body), wrong.header.Get(api.HeaderRequestID), "", 1) {
		t.Error("unknown email and wrong password are distinguishable (user enumeration)")
	}
	for i := 0; i < 4; i++ {
		r.do("POST", "/api/v1/session", map[string]string{"email": testenv.AdminEmail, "password": "wrong-password"}, nil)
	}
	rep := r.do("POST", "/api/v1/session", map[string]string{"email": testenv.AdminEmail, "password": testenv.AdminPassword}, nil)
	if rep.status != 429 || rep.errCode(t) != api.CodeTooManyAttempts || rep.header.Get("Retry-After") == "" {
		t.Errorf("throttle: %d retry-after=%q", rep.status, rep.header.Get("Retry-After"))
	}
	other := r.do("POST", "/api/v1/session", map[string]string{"email": "nobody@example.com", "password": "x-wrong-pass"}, nil)
	if other.status != 401 {
		t.Errorf("throttle leaked to another email: %d", other.status)
	}
}

func TestCookieWritesNeedTheCSRFHeader(t *testing.T) {
	env := testenv.Start(t)
	env.Setup(t)
	r := raw{t: t, base: env.URL}
	rep := r.do("POST", "/api/v1/session", map[string]string{"email": testenv.AdminEmail, "password": testenv.AdminPassword}, nil)
	cookie := strings.SplitN(rep.header.Get("Set-Cookie"), ";", 2)[0]
	body := map[string]any{"kind": "secret", "name": "csrf-test", "spec": map[string]string{"value": "v"}}
	if rep := r.do("POST", "/api/v1/resources", body, map[string]string{"Cookie": cookie}); rep.status != 403 || rep.errCode(t) != api.CodeCSRFRequired {
		t.Errorf("cookie write without CSRF header: %d", rep.status)
	}
	if rep := r.do("POST", "/api/v1/resources", body, map[string]string{"Cookie": cookie, api.HeaderCSRF: "1"}); rep.status != 202 {
		t.Errorf("cookie write with CSRF header: %d %s", rep.status, rep.body)
	}
	if rep := r.do("GET", "/api/v1/machine", nil, map[string]string{"Cookie": cookie}); rep.status != 200 {
		t.Errorf("cookie read: %d", rep.status)
	}
	if rep := r.do("GET", "/api/v1/machine", nil, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); rep.header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header sent")
	}
	if rep := r.do("DELETE", "/api/v1/session", nil, map[string]string{"Cookie": cookie, api.HeaderCSRF: "1"}); rep.status != 204 {
		t.Errorf("sign out: %d", rep.status)
	}
	if rep := r.do("GET", "/api/v1/machine", nil, map[string]string{"Cookie": cookie}); rep.status != 401 {
		t.Errorf("session survived sign-out: %d", rep.status)
	}
}

func TestTokenScopesRevocationAndExpiry(t *testing.T) {
	env := testenv.Start(t)
	writer, writeTok := env.Token(t, api.ScopeWrite)
	_, readTok := env.Token(t, api.ScopeRead)
	reader := raw{t: t, base: env.URL, token: readTok}
	if rep := reader.do("GET", "/api/v1/resources", nil, nil); rep.status != 200 {
		t.Errorf("read token read: %d", rep.status)
	}
	if rep := reader.do("POST", "/api/v1/resources", map[string]any{"kind": "secret", "name": "nope", "spec": map[string]string{"value": "v"}}, nil); rep.status != 403 || rep.errCode(t) != api.CodeForbidden {
		t.Errorf("read token write: %d", rep.status)
	}
	if rep := reader.do("GET", "/api/v1/tokens", nil, nil); rep.status != 403 {
		t.Errorf("read token lists tokens: %d", rep.status)
	}
	ctx := context.Background()
	toks, err := writer.ListTokens(ctx, false)
	if err != nil || len(toks) < 2 {
		t.Fatalf("list tokens: %v %v", toks, err)
	}
	listed := raw{t: t, base: env.URL, token: writeTok}.do("GET", "/api/v1/tokens", nil, nil)
	if strings.Contains(string(listed.body), writeTok) || strings.Contains(string(listed.body), readTok) {
		t.Error("token secret returned after creation")
	}
	var readID string
	for _, tk := range toks {
		if tk.Scope == api.ScopeRead {
			readID = tk.ID
		}
		if !strings.HasPrefix(tk.Prefix, "es_pat_") || len(tk.Prefix) != 13 {
			t.Errorf("prefix %q", tk.Prefix)
		}
	}
	if err := writer.RevokeToken(ctx, readID); err != nil {
		t.Fatal(err)
	}
	if rep := reader.do("GET", "/api/v1/resources", nil, nil); rep.status != 401 {
		t.Errorf("revoked token works: %d", rep.status)
	}
	if err := writer.RevokeToken(ctx, readID); client.Code(err) != api.CodeNotFound {
		t.Errorf("double revoke: %v", err)
	}
	// Expiry: a token whose expiry has passed is refused.
	created, err := writer.CreateToken(ctx, api.CreateTokenRequest{Name: "short", Scope: api.ScopeRead, ExpiresInDays: 1})
	if err != nil || created.Token.ExpiresAt == nil {
		t.Fatalf("expiring token: %+v %v", created, err)
	}
	expireToken(t, env, created.Token.ID)
	if rep := (raw{t: t, base: env.URL, token: created.Secret}).do("GET", "/api/v1/machine", nil, nil); rep.status != 401 {
		t.Errorf("expired token works: %d", rep.status)
	}
}

func expireToken(t *testing.T, env *testenv.Env, id string) {
	t.Helper()
	if err := env.ExpireTokenForTest(id); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitReturns429WithRetryAfter(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.RateLimit = 2 }))
	r := raw{t: t, base: env.URL}
	limited := 0
	for i := 0; i < 20; i++ {
		rep := r.do("GET", "/healthz", nil, nil)
		if rep.status == 429 {
			limited++
			if rep.header.Get("Retry-After") == "" || rep.errCode(t) != api.CodeRateLimited {
				t.Error("429 without Retry-After or code")
			}
		}
	}
	if limited == 0 {
		t.Error("no request was rate limited")
	}
}
