package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// raw is a minimal HTTP caller so tests see exact statuses, headers and bodies (no SDK retries).
type raw struct {
	t     *testing.T
	base  string
	token string
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r reply) errCode(t *testing.T) api.ErrorCode {
	t.Helper()
	var env api.ErrorEnvelope
	r.json(t, &env)
	if env.Error == nil {
		t.Fatalf("no error envelope in %s", r.body)
	}
	if env.Error.RequestID == "" || env.Error.RequestID != r.header.Get(api.HeaderRequestID) {
		t.Errorf("requestId %q does not match header %q", env.Error.RequestID, r.header.Get(api.HeaderRequestID))
	}
	return env.Error.Code
}

func (c raw) do(method, path string, body any, hdr map[string]string) reply {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			buf, _ := json.Marshal(b)
			rd = bytes.NewReader(buf)
		}
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: b}
}

func setup(t *testing.T) (*testenv.Env, raw, *client.Client) {
	t.Helper()
	env := testenv.Start(t)
	c, tok := env.Token(t, api.ScopeWrite)
	return env, raw{t: t, base: env.URL, token: tok}, c
}

func create(t *testing.T, r raw, kind api.Kind, name string, spec any) api.Resource {
	t.Helper()
	specJSON, _ := json.Marshal(spec)
	rep := r.do("POST", "/api/v1/resources", map[string]any{"kind": kind, "name": name, "spec": json.RawMessage(specJSON)}, nil)
	if rep.status != http.StatusAccepted {
		t.Fatalf("create %s %s: %d %s", kind, name, rep.status, rep.body)
	}
	var res api.Resource
	rep.json(t, &res)
	return res
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	_, r, _ := setup(t)
	for path, want := range map[string]string{"/healthz": "ok", "/readyz": "ready"} {
		rep := r.do("GET", path, nil, map[string]string{"Authorization": ""})
		if rep.status != 200 || !strings.Contains(string(rep.body), want) {
			t.Errorf("%s: %d %s", path, rep.status, rep.body)
		}
	}
	rep := r.do("GET", "/metrics", nil, nil)
	for _, m := range []string{"enclave_http_requests_total", "enclave_http_request_duration_seconds", "enclave_engine_up"} {
		if !strings.Contains(string(rep.body), m) {
			t.Errorf("metric %s missing", m)
		}
	}
}

func TestCreateGetListLifecycleForEveryKind(t *testing.T) {
	_, r, c := setup(t)
	sec := create(t, r, api.KindSecret, "api-key", api.SecretSpec{Value: "v1"})
	specs := map[api.Kind]any{
		api.KindDatabase: api.DatabaseSpec{},
		api.KindCache:    api.CacheSpec{MaxMemoryMiB: 128},
		api.KindBucket:   api.BucketSpec{Versioning: true},
		api.KindApp:      api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Publish: true, SecretEnv: map[string]string{"KEY": "api-key"}},
	}
	ids := map[api.Kind]string{api.KindSecret: sec.ID}
	for kind, spec := range specs {
		res := create(t, r, kind, "main-"+string(kind), spec)
		if res.Status.Phase != api.PhasePending || res.Generation != 1 || !strings.HasPrefix(res.ID, api.IDPrefix[kind]+"_") {
			t.Errorf("%s created as %+v", kind, res)
		}
		ids[kind] = res.ID
	}
	for kind, id := range ids {
		ready := testenv.WaitReady(t, c, id)
		if len(ready.Status.Steps) == 0 || ready.Status.ObservedGeneration != 1 {
			t.Errorf("%s ready without steps/observedGeneration: %+v", kind, ready.Status)
		}
		for _, s := range ready.Status.Steps {
			if s.State != api.StepDone {
				t.Errorf("%s step %s is %s", kind, s.Name, s.State)
			}
		}
		rep := r.do("GET", "/api/v1/resources/"+id, nil, nil)
		if rep.status != 200 || rep.header.Get("ETag") != `"1"` {
			t.Errorf("get %s: %d etag=%q", kind, rep.status, rep.header.Get("ETag"))
		}
	}
	db, _ := c.GetResource(context.Background(), ids[api.KindDatabase])
	for _, k := range []string{"host", "port", "internalHost", "internalPort", "database", "username", "passwordSecret"} {
		if _, ok := db.Outputs[k]; !ok {
			t.Errorf("database output %s missing: %v", k, db.Outputs)
		}
	}
	// The database's managed password secret and the object store credentials exist and are owned.
	pw, err := c.FindResource(context.Background(), api.KindSecret, "main-database-password")
	if err != nil || pw.OwnerID == nil || *pw.OwnerID != db.ID {
		t.Errorf("managed password secret: %+v %v", pw, err)
	}
	creds, err := c.FindResource(context.Background(), api.KindSecret, "objectstore-credentials")
	if err != nil || creds.OwnerID == nil || *creds.OwnerID != "system" {
		t.Errorf("object store credentials: %+v %v", creds, err)
	}
	var list api.ResourceList
	r.do("GET", "/api/v1/resources?kind=secret", nil, nil).json(t, &list)
	if len(list.Items) != 3 || list.NextCursor != nil {
		t.Errorf("secrets listed: %d", len(list.Items))
	}
	rep := r.do("GET", "/api/v1/resources?limit=2", nil, nil)
	rep.json(t, &list)
	if len(list.Items) != 2 || list.NextCursor == nil {
		t.Fatalf("paging: %s", rep.body)
	}
	r.do("GET", "/api/v1/resources?limit=2&cursor="+*list.NextCursor, nil, nil).json(t, &list)
	if len(list.Items) != 2 {
		t.Errorf("second page: %d", len(list.Items))
	}
}

func TestValidationErrorsCarryFieldDetails(t *testing.T) {
	_, r, _ := setup(t)
	rep := r.do("POST", "/api/v1/resources", map[string]any{"kind": "app", "name": "Bad Name", "spec": map[string]any{"port": 99999}}, nil)
	if rep.status != 422 || rep.errCode(t) != api.CodeValidationFailed {
		t.Fatalf("%d %s", rep.status, rep.body)
	}
	var env api.ErrorEnvelope
	rep.json(t, &env)
	fields := map[string]bool{}
	for _, d := range env.Error.Details {
		fields[d.Field] = true
	}
	if !fields["name"] || !fields["spec.image"] || !fields["spec.port"] {
		t.Errorf("details %+v", env.Error.Details)
	}
	rep = r.do("POST", "/api/v1/resources", map[string]any{"kind": "app", "name": "web", "spec": map[string]any{"image": "nginx", "secretEnv": map[string]string{"K": "missing-secret"}}}, nil)
	if rep.status != 422 || !strings.Contains(string(rep.body), "spec.secretEnv.K") {
		t.Errorf("unknown secret reference: %d %s", rep.status, rep.body)
	}
	rep = r.do("POST", "/api/v1/resources", map[string]any{"kind": "vm", "name": "abc", "spec": map[string]any{}}, nil)
	if rep.status != 422 {
		t.Errorf("unknown kind: %d", rep.status)
	}
}

func TestNameTakenAndManagedNameCollision(t *testing.T) {
	_, r, _ := setup(t)
	create(t, r, api.KindCache, "sessions", api.CacheSpec{})
	rep := r.do("POST", "/api/v1/resources", map[string]any{"kind": "cache", "name": "sessions", "spec": map[string]any{}}, nil)
	if rep.status != 409 || rep.errCode(t) != api.CodeNameTaken {
		t.Errorf("duplicate: %d %s", rep.status, rep.body)
	}
	create(t, r, api.KindSecret, "orders-password", api.SecretSpec{Value: "mine"})
	rep = r.do("POST", "/api/v1/resources", map[string]any{"kind": "database", "name": "orders", "spec": map[string]any{}}, nil)
	if rep.status != 409 || !strings.Contains(string(rep.body), "orders-password") {
		t.Errorf("managed-name collision: %d %s", rep.status, rep.body)
	}
	if rep := r.do("GET", "/api/v1/resources?kind=database&name=orders", nil, nil); strings.Contains(string(rep.body), `"orders"`) {
		t.Error("database half-created despite the collision (transaction not rolled back)")
	}
}

func TestIdempotencyKeyReplaysAndDetectsReuse(t *testing.T) {
	_, r, _ := setup(t)
	body := map[string]any{"kind": "secret", "name": "once", "spec": map[string]any{"value": "x"}}
	first := r.do("POST", "/api/v1/resources", body, map[string]string{api.HeaderIdempotencyKey: "key-1"})
	second := r.do("POST", "/api/v1/resources", body, map[string]string{api.HeaderIdempotencyKey: "key-1"})
	if first.status != 202 || second.status != 202 || !bytes.Equal(first.body, second.body) || second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d replayed=%q", first.status, second.status, second.header.Get("Idempotent-Replayed"))
	}
	var list api.ResourceList
	r.do("GET", "/api/v1/resources?kind=secret&name=once", nil, nil).json(t, &list)
	if len(list.Items) != 1 {
		t.Errorf("idempotent create made %d resources", len(list.Items))
	}
	body["name"] = "twice"
	rep := r.do("POST", "/api/v1/resources", body, map[string]string{api.HeaderIdempotencyKey: "key-1"})
	if rep.status != 422 || rep.errCode(t) != api.CodeIdempotencyKeyReused {
		t.Errorf("reuse: %d %s", rep.status, rep.body)
	}
}

func TestUpdateIfMatchImmutableAndNoop(t *testing.T) {
	_, r, c := setup(t)
	app := create(t, r, api.KindApp, "web", api.AppSpec{Image: "nginx:1.26"})
	testenv.WaitReady(t, c, app.ID)
	rep := r.do("PATCH", "/api/v1/resources/"+app.ID, map[string]any{"spec": map[string]any{"image": "nginx:1.27"}}, map[string]string{"If-Match": `"1"`})
	if rep.status != 202 || rep.header.Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", rep.status, rep.body)
	}
	var updated api.Resource
	rep.json(t, &updated)
	if updated.Status.Phase != api.PhaseUpdating {
		t.Errorf("phase after update %s", updated.Status.Phase)
	}
	ready := testenv.WaitReady(t, c, app.ID)
	var spec api.AppSpec
	_ = json.Unmarshal(ready.Spec, &spec)
	if spec.Image != "nginx:1.27" || ready.Status.ObservedGeneration != 2 {
		t.Errorf("after update: %+v observed=%d", spec, ready.Status.ObservedGeneration)
	}
	rep = r.do("PATCH", "/api/v1/resources/"+app.ID, map[string]any{"spec": map[string]any{"image": "nginx:1.28"}}, map[string]string{"If-Match": `"1"`})
	if rep.status != 412 || rep.errCode(t) != api.CodePreconditionFailed {
		t.Errorf("stale If-Match: %d", rep.status)
	}
	rep = r.do("PATCH", "/api/v1/resources/"+app.ID, map[string]any{"spec": map[string]any{"image": "nginx:1.27"}}, nil)
	if rep.status != 200 {
		t.Errorf("no-op patch: %d, want 200", rep.status)
	}
	db := create(t, r, api.KindDatabase, "orders", api.DatabaseSpec{Version: "16"})
	rep = r.do("PATCH", "/api/v1/resources/"+db.ID, map[string]any{"spec": map[string]any{"version": "17"}}, nil)
	if rep.status != 422 || rep.errCode(t) != api.CodeImmutableField {
		t.Errorf("immutable version: %d %s", rep.status, rep.body)
	}
	pw, _ := c.FindResource(context.Background(), api.KindSecret, "orders-password")
	rep = r.do("PATCH", "/api/v1/resources/"+pw.ID, map[string]any{"spec": map[string]any{"value": "x"}}, nil)
	if rep.status != 422 {
		t.Errorf("editing a managed secret: %d", rep.status)
	}
}

func TestSecretValueIsWriteOnlyAndRevealIsAudited(t *testing.T) {
	env, r, c := setup(t)
	sec := create(t, r, api.KindSecret, "stripe", api.SecretSpec{Value: "sk_live_TOPSECRET"})
	testenv.WaitReady(t, c, sec.ID)
	for _, path := range []string{"/api/v1/resources/" + sec.ID, "/api/v1/resources?kind=secret", "/api/v1/resources/" + sec.ID + "/events"} {
		if rep := r.do("GET", path, nil, nil); strings.Contains(string(rep.body), "TOPSECRET") {
			t.Errorf("%s leaks the value: %s", path, rep.body)
		}
	}
	rep := r.do("GET", "/api/v1/resources/"+sec.ID+"/value", nil, nil)
	if rep.status != 200 || !strings.Contains(string(rep.body), "sk_live_TOPSECRET") || rep.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %s", rep.status, rep.body)
	}
	events, _ := c.Events(context.Background(), sec.ID, 10)
	if events[0].Type != "secret_revealed" {
		t.Errorf("reveal not audited: %+v", events)
	}
	rep = r.do("PATCH", "/api/v1/resources/"+sec.ID, map[string]any{"spec": map[string]any{"value": "rotated"}}, nil)
	if rep.status != 202 {
		t.Fatalf("rotate: %d %s", rep.status, rep.body)
	}
	if v, _ := c.RevealSecret(context.Background(), sec.ID); v != "rotated" {
		t.Errorf("rotated value %q", v)
	}
	app := create(t, r, api.KindApp, "web", api.AppSpec{Image: "nginx"})
	if rep := r.do("GET", "/api/v1/resources/"+app.ID+"/value", nil, nil); rep.status != 409 {
		t.Errorf("reveal on an app: %d", rep.status)
	}
	if strings.Contains(env.Logs.String(), "TOPSECRET") || strings.Contains(env.Logs.String(), "rotated\"") {
		t.Error("secret value appears in server logs")
	}
}

func TestDeleteChecksDependantsAndRemovesManagedChildren(t *testing.T) {
	_, r, c := setup(t)
	sec := create(t, r, api.KindSecret, "key", api.SecretSpec{Value: "v"})
	app := create(t, r, api.KindApp, "web", api.AppSpec{Image: "nginx", SecretEnv: map[string]string{"K": "key"}})
	testenv.WaitReady(t, c, app.ID)
	rep := r.do("DELETE", "/api/v1/resources/"+sec.ID, nil, nil)
	if rep.status != 409 || rep.errCode(t) != api.CodeResourceInUse || !strings.Contains(string(rep.body), "app/web") {
		t.Fatalf("delete used secret: %d %s", rep.status, rep.body)
	}
	if rep := r.do("DELETE", "/api/v1/resources/"+app.ID, nil, map[string]string{"If-Match": `"9"`}); rep.status != 412 {
		t.Errorf("delete with stale If-Match: %d", rep.status)
	}
	rep = r.do("DELETE", "/api/v1/resources/"+app.ID, nil, nil)
	var deleting api.Resource
	rep.json(t, &deleting)
	if rep.status != 202 || deleting.Status.Phase != api.PhaseDeleting {
		t.Fatalf("delete app: %d %s", rep.status, rep.body)
	}
	if rep := r.do("PATCH", "/api/v1/resources/"+app.ID, map[string]any{"spec": map[string]any{"image": "x"}}, nil); rep.status != 409 && rep.status != 404 {
		t.Errorf("update while deleting: %d", rep.status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.WaitGone(ctx, app.ID, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if rep := r.do("DELETE", "/api/v1/resources/"+sec.ID, nil, nil); rep.status != 202 {
		t.Errorf("secret still blocked after app deleted: %d %s", rep.status, rep.body)
	}
	db := create(t, r, api.KindDatabase, "orders", api.DatabaseSpec{})
	testenv.WaitReady(t, c, db.ID)
	pw, _ := c.FindResource(context.Background(), api.KindSecret, "orders-password")
	if rep := r.do("DELETE", "/api/v1/resources/"+pw.ID, nil, nil); rep.status != 409 {
		t.Errorf("deleting a managed secret directly: %d", rep.status)
	}
	r.do("DELETE", "/api/v1/resources/"+db.ID, nil, nil)
	if err := c.WaitGone(ctx, db.ID, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetResource(context.Background(), pw.ID); !client.IsNotFound(err) {
		t.Errorf("managed password outlived its database: %v", err)
	}
}

func TestActionsRestartRedeployReconcile(t *testing.T) {
	env, r, c := setup(t)
	app := create(t, r, api.KindApp, "web", api.AppSpec{Image: "nginx"})
	testenv.WaitReady(t, c, app.ID)
	if rep := r.do("POST", "/api/v1/resources/"+app.ID+"/actions/restart", nil, nil); rep.status != 202 {
		t.Fatalf("restart: %d %s", rep.status, rep.body)
	}
	testenv.Eventually(t, 5*time.Second, "restarted event", func() bool {
		evs, _ := c.Events(context.Background(), app.ID, 5)
		return len(evs) > 0 && evs[0].Type == "restarted"
	})
	calls := env.Fake.EnsureCalls("web")
	if rep := r.do("POST", "/api/v1/resources/"+app.ID+"/actions/redeploy", nil, nil); rep.status != 202 {
		t.Fatalf("redeploy: %d", rep.status)
	}
	testenv.WaitReady(t, c, app.ID)
	if env.Fake.EnsureCalls("web") != calls+1 {
		t.Error("redeploy did not run the driver again")
	}
	sec := create(t, r, api.KindSecret, "plain-key", api.SecretSpec{Value: "v"})
	if rep := r.do("POST", "/api/v1/resources/"+sec.ID+"/actions/restart", nil, nil); rep.status != 409 || rep.errCode(t) != api.CodeActionNotSupported {
		t.Errorf("restart a secret: %d", rep.status)
	}
	if rep := r.do("POST", "/api/v1/resources/"+app.ID+"/actions/restart", map[string]any{"force": true}, nil); rep.status != 400 || rep.errCode(t) != api.CodeMalformedRequest {
		t.Errorf("restart with a body: %d %s", rep.status, rep.body) // L3-002 §6.4: actions without a body reject one
	}
	if rep := r.do("POST", "/api/v1/resources/"+app.ID+"/actions/restart", "", nil); rep.status != 202 {
		t.Errorf("restart with an empty body: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", "/api/v1/resources/"+app.ID+"/actions/dance", nil, nil); rep.status != 404 {
		t.Errorf("unknown action: %d", rep.status)
	}
}

func TestLogsEventsAndMachine(t *testing.T) {
	env, r, c := setup(t)
	app := create(t, r, api.KindApp, "web", api.AppSpec{Image: "nginx", MemoryMiB: 300})
	testenv.WaitReady(t, c, app.ID)
	var logs api.LogList
	if rep := r.do("GET", "/api/v1/resources/"+app.ID+"/logs?tail=2", nil, nil); rep.status != 200 {
		t.Fatalf("logs: %d", rep.status)
	} else {
		rep.json(t, &logs)
	}
	if len(logs.Items) != 2 {
		t.Errorf("tail=2 returned %d lines", len(logs.Items))
	}
	if rep := r.do("GET", "/api/v1/resources/"+app.ID+"/logs?tail=5000", nil, nil); rep.status != 422 {
		t.Errorf("tail out of range: %d", rep.status)
	}
	var m api.Machine
	r.do("GET", "/api/v1/machine", nil, nil).json(t, &m)
	if m.Name != "Test machine" || !m.Engine.Available || m.Engine.Driver != "fake" || m.MemoryReservedBytes != 300<<20 {
		t.Errorf("machine %+v", m)
	}
	env.Fake.SetAvailable(false)
	if rep := r.do("GET", "/api/v1/resources/"+app.ID+"/logs", nil, nil); rep.status != 503 || rep.errCode(t) != api.CodeEngineUnavailable {
		t.Errorf("logs with engine down: %d", rep.status)
	}
	r.do("GET", "/api/v1/machine", nil, nil).json(t, &m)
	if m.Engine.Available || m.Engine.Message == "" {
		t.Errorf("engine outage not reported: %+v", m.Engine)
	}
}

func TestTransportRules(t *testing.T) {
	_, r, _ := setup(t)
	if rep := r.do("POST", "/api/v1/resources", "kind=app", map[string]string{"Content-Type": "text/plain"}); rep.status != 415 || rep.errCode(t) != api.CodeUnsupportedMediaType {
		t.Errorf("non-JSON body: %d", rep.status)
	}
	if rep := r.do("POST", "/api/v1/resources", `{"kind":"app","nmae":"x"}`, nil); rep.status != 400 || rep.errCode(t) != api.CodeMalformedRequest {
		t.Errorf("unknown field: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", "/api/v1/resources", `{"kind":`, nil); rep.status != 400 {
		t.Errorf("broken JSON: %d", rep.status)
	}
	big := `{"kind":"secret","name":"big","spec":{"value":"` + strings.Repeat("x", 1<<20) + `"}}`
	if rep := r.do("POST", "/api/v1/resources", big, nil); rep.status != 413 || rep.errCode(t) != api.CodePayloadTooLarge {
		t.Errorf("oversized body: %d", rep.status)
	}
	if rep := r.do("GET", "/api/v1/nope", nil, nil); rep.status != 404 || rep.errCode(t) != api.CodeNotFound {
		t.Errorf("unknown route: %d %s", rep.status, rep.body)
	}
	if rep := r.do("PUT", "/api/v1/resources", nil, nil); rep.status != 405 || rep.errCode(t) != api.CodeMethodNotAllowed {
		t.Errorf("wrong method: %d %s", rep.status, rep.body)
	}
	if rep := r.do("GET", "/api/v1/resources/app_nothere", nil, nil); rep.status != 404 {
		t.Errorf("missing resource: %d", rep.status)
	}
	if rep := r.do("GET", "/api/v1/resources?cursor=@@", nil, nil); rep.status != 422 {
		t.Errorf("bad cursor: %d", rep.status)
	}
	rep := r.do("GET", "/api/v1/machine", nil, map[string]string{api.HeaderRequestID: "client-chosen.id"})
	if rep.header.Get(api.HeaderRequestID) != "client-chosen.id" {
		t.Errorf("client request id not echoed: %q", rep.header.Get(api.HeaderRequestID))
	}
	rep = r.do("GET", "/api/v1/machine", nil, map[string]string{api.HeaderRequestID: "bad id with spaces"})
	if !strings.HasPrefix(rep.header.Get(api.HeaderRequestID), "req_") {
		t.Errorf("malformed request id accepted: %q", rep.header.Get(api.HeaderRequestID))
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy"} {
		if rep.header.Get(h) == "" {
			t.Errorf("security header %s missing", h)
		}
	}
}

func TestConsoleIsServedWithSPAFallback(t *testing.T) {
	env := testenv.Start(t)
	r := raw{t: t, base: env.URL}
	rep := r.do("GET", "/apps/app_123", nil, nil)
	if rep.status != 200 || !strings.Contains(string(rep.body), "<") {
		t.Errorf("console route: %d", rep.status)
	}
}

func TestFakeFaultsFromConfig(t *testing.T) {
	faults, err := fake.ParseFaults("a:transient=2; b:permanent")
	if err != nil || faults["a"].TransientFailures != 2 || !faults["b"].Permanent {
		t.Fatalf("%+v %v", faults, err)
	}
	for _, bad := range []string{"a", "a:transient=0", "a:explode"} {
		if _, err := fake.ParseFaults(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBackupRunsAndRestoreOverHTTP(t *testing.T) {
	env, r, c := setup(t)
	db := create(t, r, api.KindDatabase, "shop", api.DatabaseSpec{})
	create(t, r, api.KindBucket, "vault", api.BucketSpec{})
	testenv.WaitReady(t, c, db.ID)
	bk := create(t, r, api.KindBackup, "nightly", api.BackupSpec{Source: api.BackupSource{Database: "shop"}, Destination: api.BackupDestination{Bucket: "vault"}})
	testenv.WaitReady(t, c, bk.ID)
	env.Fake.SetData(db.ID, "rows=3")

	rep := r.do("POST", "/api/v1/resources/"+bk.ID+"/actions/run", nil, nil)
	var run api.Run
	if rep.status != http.StatusAccepted || json.Unmarshal(rep.body, &run) != nil || run.Trigger != api.TriggerManual {
		t.Fatalf("run: %d %s", rep.status, rep.body)
	}
	testenv.Eventually(t, 10*time.Second, "backup run succeeds", func() bool {
		d, err := c.GetRun(context.Background(), bk.ID, run.ID)
		return err == nil && d.Status == api.RunSucceeded
	})
	rep = r.do("GET", "/api/v1/resources/"+bk.ID+"/runs", nil, nil)
	var list api.RunList
	if rep.status != 200 || json.Unmarshal(rep.body, &list) != nil || len(list.Items) != 1 || list.Items[0].Artifact == nil || list.Items[0].Artifact.SHA256 == "" {
		t.Fatalf("runs: %d %s", rep.status, rep.body)
	}

	restore := "/api/v1/resources/" + bk.ID + "/actions/restore"
	if rep := r.do("POST", restore, "{not json", nil); rep.status != http.StatusBadRequest {
		t.Errorf("malformed body: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", restore, nil, nil); rep.status != http.StatusUnsupportedMediaType {
		t.Errorf("no body: %d %s", rep.status, rep.body) // a restore needs its JSON body, like a create
	}
	if rep := r.do("POST", restore, map[string]any{}, nil); rep.status != http.StatusUnprocessableEntity || rep.errCode(t) != api.CodeValidationFailed {
		t.Errorf("empty request: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", restore, map[string]any{"runId": run.ID, "mode": "new", "name": "shop-copy", "extra": 1}, nil); rep.status != http.StatusBadRequest {
		t.Errorf("unknown field: %d %s", rep.status, rep.body)
	}
	_, readTok := env.Token(t, api.ScopeRead)
	reader := raw{t: t, base: env.URL, token: readTok}
	if rep := reader.do("POST", restore, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "shop-copy"}, nil); rep.status != http.StatusForbidden {
		t.Errorf("read token restored: %d", rep.status)
	}
	if rep := reader.do("GET", "/api/v1/resources/"+bk.ID+"/runs", nil, nil); rep.status != 200 {
		t.Errorf("read token can't list runs: %d", rep.status)
	}
	if rep := r.do("POST", "/api/v1/resources/"+db.ID+"/actions/restore", api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "x-copy"}, nil); rep.status != 409 || rep.errCode(t) != api.CodeActionNotSupported {
		t.Errorf("restore on a database: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", restore, api.RestoreRequest{RunID: "run_aaaaaaaaaaaaaaaa", Mode: api.RestoreNew, Name: "x-copy"}, nil); rep.status != 409 || rep.errCode(t) != api.CodeRunNotRestorable {
		t.Errorf("unknown run: %d %s", rep.status, rep.body)
	}
	if rep := r.do("POST", restore, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreReplace, ConfirmName: "shopp"}, nil); rep.status != 422 {
		t.Errorf("wrong confirm name: %d %s", rep.status, rep.body)
	}
	rep = r.do("POST", restore, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "shop-copy"}, nil)
	var target api.Resource
	if rep.status != http.StatusAccepted || json.Unmarshal(rep.body, &target) != nil || target.Kind != api.KindDatabase || target.Name != "shop-copy" {
		t.Fatalf("restore new: %d %s", rep.status, rep.body)
	}
	testenv.WaitReady(t, c, target.ID)
	if got := env.Fake.Data(target.ID); got != "rows=3" {
		t.Errorf("restored data %q", got)
	}
}

func TestBuildLogsOverHTTP(t *testing.T) {
	_, r, c := setup(t)
	app := create(t, r, api.KindApp, "site", api.AppSpec{Source: &api.GitSource{Git: "https://git.example.com/acme/site.git", Ref: "main"}})
	testenv.WaitReady(t, c, app.ID)
	rep := r.do("GET", "/api/v1/resources/"+app.ID+"/logs?build=true&tail=2", nil, nil)
	var list api.LogList
	if rep.status != 200 || json.Unmarshal(rep.body, &list) != nil || len(list.Items) != 2 || !strings.Contains(list.Items[1].Message, "naming to enclave.local/site:r1-") {
		t.Fatalf("build log: %d %s", rep.status, rep.body)
	}
	if rep := r.do("GET", "/api/v1/resources/"+app.ID+"/logs?build=yes", nil, nil); rep.status != 422 {
		t.Errorf("build=yes: %d", rep.status)
	}
	db := create(t, r, api.KindDatabase, "shop", api.DatabaseSpec{})
	if rep := r.do("GET", "/api/v1/resources/"+db.ID+"/logs?build=true", nil, nil); rep.status != 409 || rep.errCode(t) != api.CodeActionNotSupported {
		t.Errorf("build log of a database: %d %s", rep.status, rep.body)
	}
	plain := create(t, r, api.KindApp, "plain", api.AppSpec{Image: "nginx"})
	if rep := r.do("GET", "/api/v1/resources/"+plain.ID+"/logs?build=true", nil, nil); rep.status != 200 || !strings.Contains(string(rep.body), `"items":[]`) {
		t.Errorf("an image app has no builds: %d %s", rep.status, rep.body)
	}
}
