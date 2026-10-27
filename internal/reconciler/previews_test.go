package reconciler_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Previews (ADR-056, L2-016 PRV-01 – PRV-09) through the real API, webhooks included, on the fake engine.

// hook posts a GitHub webhook signed with key and returns the status and result.
func hook(t *testing.T, c *client.Client, appID, key, event string, body any) (int, api.HookResult) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, c.BaseURL+api.HookPath+appID, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	if key != "" {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(b)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out api.HookResult
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func pr(action string, n int) map[string]any {
	return map[string]any{"action": action, "number": n, "pull_request": map[string]any{"head": map[string]any{"ref": "feature"}}}
}

func TestPreviewsFromWebhooksAndByHand(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	env.Git.Push(repoURL, "refs/pull/7/head", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	create(t, c, api.KindSecret, "web-hook", api.SecretSpec{Value: "s3cret"})
	app := create(t, c, api.KindApp, "web", api.AppSpec{Port: 8080, Env: map[string]string{"LEVEL": "1"},
		Source:   &api.GitSource{Git: repoURL, Ref: "main", WebhookSecret: "web-hook", DeployOnPush: true},
		Previews: &api.Previews{Enabled: true, MaxActive: 1, Domain: &api.PreviewDomain{Suffix: "preview.example.com"}}})
	if app.Outputs["webhookUrl"] != c.BaseURL+api.HookPath+app.ID && app.Outputs["webhookUrl"] != api.HookPath+app.ID {
		t.Errorf("webhook URL %v", app.Outputs["webhookUrl"])
	}

	// Unsigned or wrongly signed: 401; ping: pong.
	if st, _ := hook(t, c, app.ID, "", "pull_request", pr("opened", 7)); st != http.StatusUnauthorized {
		t.Errorf("unsigned: %d", st)
	}
	if st, _ := hook(t, c, app.ID, "wrong", "pull_request", pr("opened", 7)); st != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", st)
	}
	if st, res := hook(t, c, app.ID, "s3cret", "ping", map[string]any{"zen": "hi"}); st != 200 || res.Result != api.HookPong {
		t.Errorf("ping: %d %+v", st, res)
	}

	// Opened: a preview app owned by web, at the pull request's head, with its domain (PRV-02, PRV-08).
	st, res := hook(t, c, app.ID, "s3cret", "pull_request", pr("opened", 7))
	if st != 200 || res.Result != api.HookCreated || res.Preview != "web-pr-7" {
		t.Fatalf("opened: %d %+v", st, res)
	}
	pv, err := c.FindResource(ctx, api.KindApp, "web-pr-7")
	if err != nil {
		t.Fatal(err)
	}
	pv = waitReadyWithin(t, c, pv.ID, 20*time.Second)
	s := mustSpec(t, pv)
	if s.Source.Ref != "refs/pull/7/head" || s.Previews != nil || s.Source.WebhookSecret != "" || s.Env[api.EnvPreview] != "web-pr-7" || s.Env["LEVEL"] != "1" {
		t.Errorf("preview spec %+v %+v", s, s.Source)
	}
	if pv.Outputs["previewOf"] != "web" || pv.Outputs["pullRequest"] != float64(7) || pv.Outputs["expiresAt"] == nil {
		t.Errorf("preview outputs %v", pv.Outputs)
	}
	if d, err := c.FindResource(ctx, api.KindDomain, "web-pr-7"); err != nil || mustDomain(t, d).Hostname != "web-pr-7.preview.example.com" {
		t.Errorf("preview domain: %v", err)
	}
	list, err := c.ListPreviews(ctx, app.ID)
	if err != nil || len(list) != 1 || list[0].PullRequest != 7 || list[0].Ref != "refs/pull/7/head" {
		t.Errorf("list %+v %v", list, err)
	}
	// A preview follows its app: no edits.
	if _, err := c.UpdateResource(ctx, pv.ID, map[string]any{"replicas": 2}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("patching a preview: %v", err)
	}

	// Synchronize redeploys; past maxActive another pull request is refused (PRV-07).
	if _, res := hook(t, c, app.ID, "s3cret", "pull_request", pr("synchronize", 7)); res.Result != api.HookRedeployed {
		t.Errorf("synchronize: %+v", res)
	}
	if st, _ := hook(t, c, app.ID, "s3cret", "pull_request", pr("opened", 8)); st != http.StatusConflict {
		t.Errorf("past the limit: %d", st)
	}
	if _, err := c.CreatePreview(ctx, app.ID, api.CreatePreview{Ref: "feature/login"}); apiCode(err) != api.CodePreviewLimit {
		t.Errorf("by hand past the limit: %v", err)
	}

	// A push to the app's ref redeploys it; to another branch, nothing.
	if _, res := hook(t, c, app.ID, "s3cret", "push", map[string]any{"ref": "refs/heads/other"}); res.Result != api.HookIgnored {
		t.Errorf("push elsewhere: %+v", res)
	}
	waitReadyWithin(t, c, app.ID, 20*time.Second)
	if _, res := hook(t, c, app.ID, "s3cret", "push", map[string]any{"ref": "refs/heads/main"}); res.Result != api.HookDeployed {
		t.Errorf("push: %+v", res)
	}

	// Closed: the preview and its domain go (PRV-04).
	waitReadyWithin(t, c, pv.ID, 20*time.Second)
	if _, res := hook(t, c, app.ID, "s3cret", "pull_request", pr("closed", 7)); res.Result != api.HookDeleted {
		t.Errorf("closed: %+v", res)
	}
	if err := c.WaitGone(ctx, pv.ID, 50*time.Millisecond); err != nil {
		t.Errorf("preview not deleted: %v", err)
	}
	if _, err := c.FindResource(ctx, api.KindDomain, "web-pr-7"); apiCode(err) != api.CodeNotFound {
		t.Errorf("preview domain left: %v", err)
	}
	if eventTypes(t, c, app.ID)["preview_created"] != 1 || eventTypes(t, c, app.ID)["preview_deleted"] != 1 {
		t.Errorf("events %v", eventTypes(t, c, app.ID))
	}

	// By hand; deleting the app takes its previews with it (PRV-06, PRV-09).
	hand, err := c.CreatePreview(ctx, app.ID, api.CreatePreview{Ref: "feature/login"})
	if err != nil || hand.Name != "web-feature-login" {
		t.Fatalf("by hand: %+v %v", hand, err)
	}
	waitReadyWithin(t, c, hand.ID, 20*time.Second)
	waitReadyWithin(t, c, app.ID, 20*time.Second)
	if _, err := c.DeleteResource(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{hand.ID, app.ID} {
		if err := c.WaitGone(ctx, id, 50*time.Millisecond); err != nil {
			t.Errorf("%s not deleted: %v", id, err)
		}
	}
	// With the app gone, its hook is not found.
	if st, _ := hook(t, c, app.ID, "s3cret", "ping", map[string]any{}); st != http.StatusNotFound {
		t.Errorf("hook of a deleted app: %d", st)
	}
}

func mustDomain(t *testing.T, r api.Resource) api.DomainSpec {
	t.Helper()
	var d api.DomainSpec
	if err := json.Unmarshal(r.Spec, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
