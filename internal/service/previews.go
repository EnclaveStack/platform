package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Preview environments (ADR-056, L2-016 PRV-01 – PRV-09): an app per pull request, owned by its parent
// app, made and removed by verified Git webhooks or by hand.

// Preview outputs, kept by the reconciler across reconciles.
const (
	OutputPreviewOf   = "previewOf"
	OutputPreviewRef  = "previewRef"
	OutputPullRequest = "pullRequest"
	OutputExpiresAt   = "expiresAt"
)

// isPreview reports whether r is a preview: an app owned by another app.
func isPreview(r *domain.Resource) bool {
	return r.Kind == api.KindApp && r.OwnerID != nil && *r.OwnerID != domain.SystemOwner
}

// ownedRuntimes lists what goes when id is deleted besides its managed secrets: an app's previews and
// each preview's domain, deepest first.
func ownedRuntimes(ctx context.Context, q *store.Queries, id string) ([]*domain.Resource, error) {
	owned, err := q.OwnedBy(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []*domain.Resource
	for _, o := range owned {
		if o.Kind == api.KindSecret {
			continue
		}
		below, err := ownedRuntimes(ctx, q, o.ID)
		if err != nil {
			return nil, err
		}
		out = append(append(out, below...), o)
	}
	return out, nil
}

func appSpec(r *domain.Resource) (api.AppSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindApp, r.Spec)
	if err != nil {
		return api.AppSpec{}, err
	}
	return raw.(api.AppSpec), nil
}

// previewSpec is the parent's spec at ref, as one replica, unpublished, without previews, webhooks or
// volumes, with ENCLAVE_PREVIEW and ENCLAVE_PREVIEW_REF set (ADR-056).
func previewSpec(parent api.AppSpec, name, ref string) api.AppSpec {
	s := parent
	src := *parent.Source
	src.Ref, src.WebhookSecret, src.DeployOnPush = ref, "", false
	s.Source, s.Previews, s.Volumes, s.Publish = &src, nil, nil, false
	s.Replicas = 1
	s.Env = maps.Clone(parent.Env)
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	s.Env[api.EnvPreview], s.Env[api.EnvPreviewRef] = name, ref
	return s
}

// previewRefs references an app's webhook secret and its previews' zone and tunnel (PRV-03, PRV-08).
func previewRefs(ctx context.Context, q *store.Queries, project string, a api.AppSpec) ([]store.Ref, error) {
	if a.Source == nil {
		return nil, nil
	}
	type need struct {
		kind        api.Kind
		name, field string
	}
	wants := []need{{api.KindSecret, a.Source.WebhookSecret, "spec.source.webhookSecret"}}
	if a.Previews != nil && a.Previews.Domain != nil {
		wants = append(wants, need{api.KindZone, a.Previews.Domain.Zone, "spec.previews.domain.zone"}, need{api.KindTunnel, a.Previews.Domain.Tunnel, "spec.previews.domain.tunnel"})
	}
	var refs []store.Ref
	for _, w := range wants {
		if w.name == "" {
			continue
		}
		r, err := live(ctx, q, project, w.kind, w.name, w.field)
		if err != nil {
			return nil, err
		}
		refs = append(refs, store.Ref{ToID: r.ID, Field: w.field})
	}
	return refs, nil
}

// previewsOf lists an app's previews, oldest first.
func previewsOf(ctx context.Context, q *store.Queries, appID string) ([]*domain.Resource, error) {
	owned, err := q.OwnedBy(ctx, appID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(owned, func(o *domain.Resource) bool { return o.Kind != api.KindApp }), nil
}

func toPreview(r *domain.Resource) api.Preview {
	p := api.Preview{ID: r.ID, Name: r.Name, Phase: r.Phase, CreatedAt: r.CreatedAt}
	p.Ref, _ = r.Outputs[OutputPreviewRef].(string)
	if n, ok := r.Outputs[OutputPullRequest].(float64); ok {
		p.PullRequest = int(n)
	} else if n, ok := r.Outputs[OutputPullRequest].(int); ok {
		p.PullRequest = n
	}
	if at, ok := r.Outputs[OutputExpiresAt].(string); ok {
		p.ExpiresAt, _ = time.Parse(time.RFC3339, at)
	}
	p.URL, _ = r.Outputs["url"].(string)
	return p
}

// ListPreviews lists an app's previews (PRV-07).
func (s *Resources) ListPreviews(ctx context.Context, id string) (api.PreviewList, error) {
	app, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.PreviewList{}, notFound()
	}
	if err != nil {
		return api.PreviewList{}, err
	}
	if app.Kind != api.KindApp {
		return api.PreviewList{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no previews.", app.Kind))
	}
	list, err := previewsOf(ctx, s.Store.Queries, id)
	if err != nil {
		return api.PreviewList{}, err
	}
	out := api.PreviewList{Items: make([]api.Preview, 0, len(list))}
	for _, r := range list {
		out.Items = append(out.Items, toPreview(r))
	}
	return out, nil
}

// CreatePreview makes a preview of an app from a ref, or redeploys it when it exists (PRV-06).
func (s *Resources) CreatePreview(ctx context.Context, p Principal, id string, req api.CreatePreview) (api.Resource, error) {
	req.Ref, req.Name = strings.TrimSpace(req.Ref), strings.TrimSpace(req.Name)
	if req.Ref == "" || len(req.Ref) > api.GitRefMaxLen || strings.ContainsAny(req.Ref, " \t\n") {
		return api.Resource{}, api.Validation([]api.FieldError{{Field: "ref", Code: api.FieldInvalidFormat, Message: "Enter a branch, tag, commit or ref, like feature/login or refs/pull/7/head."}})
	}
	var out *domain.Resource
	var queued []string
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		app, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		name := req.Name
		if name == "" {
			name = api.PreviewNameForRef(app.Name, req.Ref)
		}
		if errs := api.ValidateName(name); len(errs) > 0 {
			return api.Validation(errs)
		}
		out, queued, _, err = s.upsertPreview(ctx, q, p, app, name, req.Ref, 0)
		return err
	})
	if err != nil {
		return api.Resource{}, err
	}
	for _, q := range queued {
		s.Queue.Enqueue(q)
	}
	return out.ToAPI(), nil
}

// upsertPreview makes the preview name of app at ref, or redeploys it, and moves its expiry. It returns
// the preview, the IDs to enqueue and what happened (api.HookCreated or api.HookRedeployed).
func (s *Resources) upsertPreview(ctx context.Context, q *store.Queries, p Principal, app *domain.Resource, name, ref string, pr int) (*domain.Resource, []string, string, error) {
	if app.Kind != api.KindApp || isPreview(app) {
		return nil, nil, "", api.NewError(api.CodeActionNotSupported, "Only an app built from Git has previews.")
	}
	if app.Phase == api.PhaseDeleting {
		return nil, nil, "", api.NewError(api.CodeResourceBusy, "This app is being deleted.")
	}
	parent, err := appSpec(app)
	if err != nil {
		return nil, nil, "", err
	}
	if parent.Source == nil || !parent.Previews.On() {
		return nil, nil, "", api.NewError(api.CodeActionNotSupported, "Turn previews on for this app first: they need an app built from Git.")
	}
	now := s.Now()
	expires := now.Add(time.Duration(parent.Previews.TTLHours) * time.Hour).UTC().Format(time.RFC3339)
	marks := map[string]any{OutputPreviewOf: app.Name, OutputPreviewRef: ref, OutputExpiresAt: expires}
	if pr > 0 {
		marks[OutputPullRequest] = pr
	}
	spec := previewSpec(parent, name, ref)
	existing, err := q.GetResourceByName(ctx, app.ProjectName(), api.KindApp, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		previews, err := previewsOf(ctx, q, app.ID)
		if err != nil {
			return nil, nil, "", err
		}
		active := slices.DeleteFunc(previews, func(r *domain.Resource) bool { return r.Phase == api.PhaseDeleting })
		if len(active) >= parent.Previews.MaxActive {
			return nil, nil, "", api.NewError(api.CodePreviewLimit, fmt.Sprintf("%s already has %d previews, its limit. Delete one, or raise previews.maxActive.", app.Name, len(active)))
		}
		raw, _ := json.Marshal(spec)
		r, created, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Project: app.ProjectName(), Kind: api.KindApp, Name: name, Spec: raw},
			createOpts{outputs: marks, owner: app.ID, note: fmt.Sprintf(" A preview of %s at %s.", app.Name, ref)})
		if err != nil {
			return nil, nil, "", err
		}
		if d := parent.Previews.Domain; d != nil {
			ds, _ := json.Marshal(api.DomainSpec{Hostname: name + "." + d.Suffix, App: name, TLS: d.TLS, Zone: d.Zone, Tunnel: d.Tunnel})
			_, more, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Project: app.ProjectName(), Kind: api.KindDomain, Name: name, Spec: ds}, createOpts{owner: r.ID})
			if err != nil {
				return nil, nil, "", err
			}
			created = append(created, more...)
		}
		return r, created, api.HookCreated, q.AddEvent(ctx, app.ID, "preview_created", fmt.Sprintf("Preview %s created from %s.", name, ref), now)
	case err != nil:
		return nil, nil, "", err
	}
	if existing.OwnerID == nil || *existing.OwnerID != app.ID {
		return nil, nil, "", api.Validation([]api.FieldError{{Field: "name", Code: api.FieldNotAllowed, Message: "There's already an app named " + name + " that isn't a preview of " + app.Name + ". Choose another name."}})
	}
	if existing.Phase == api.PhaseDeleting {
		return nil, nil, "", api.NewError(api.CodeResourceBusy, "This preview is being deleted. Try again once it's gone.")
	}
	existing.Outputs = withOutputs(existing.Outputs, marks)
	existing.Attempts, existing.NextAttemptAt, existing.UpdatedAt = 0, nil, now
	if before, err := appSpec(existing); err == nil && specEqual(before, spec) {
		// Same spec: build the ref's newest commit (a redeploy resolves the ref again).
		if err := existing.SetPhase(api.PhaseUpdating, "Redeploying", now); err != nil {
			return nil, nil, "", err
		}
		existing.RequestedAction = api.ActionRedeploy
	} else {
		if err := s.setSpec(existing, spec); err != nil {
			return nil, nil, "", err
		}
		existing.Generation++
		if err := existing.SetPhase(api.PhaseUpdating, "", now); err != nil {
			return nil, nil, "", err
		}
	}
	existing.Steps = domain.NewSteps(domain.StepNames[api.KindApp]...)
	if err := q.SaveResource(ctx, existing); err != nil {
		return nil, nil, "", err
	}
	if err := q.AddEvent(ctx, existing.ID, "redeploying", fmt.Sprintf("Preview redeployed from %s by %s.", ref, p.Actor()), now); err != nil {
		return nil, nil, "", err
	}
	return existing, []string{existing.ID}, api.HookRedeployed, nil
}

// GitHook is a Git webhook as the handler read it.
type GitHook struct {
	Header http.Header
	Body   []byte
}

// gitPush, gitPullRequest and gitLab are the parts of GitHub and GitLab webhooks a hook reads.
type gitPayload struct {
	// GitHub.
	Action      string `json:"action"`
	Number      int    `json:"number"`
	Ref         string `json:"ref"`
	PullRequest *struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	// GitLab.
	ObjectKind string `json:"object_kind"`
	Attributes *struct {
		IID    int    `json:"iid"`
		Action string `json:"action"`
	} `json:"object_attributes"`
}

// hookPrincipal is who a webhook acts as: EnclaveStack itself, once the signature checked.
var hookPrincipal = Principal{System: true}

// Hook handles a Git webhook for an app (PRV-03 – PRV-05). The app answers not found unless it has a
// webhook secret, so a guess learns nothing.
func (s *Resources) Hook(ctx context.Context, appID string, h GitHook) (api.HookResult, error) {
	app, err := s.Store.GetResource(ctx, appID)
	if err != nil || app.Kind != api.KindApp || app.Phase == api.PhaseDeleting {
		return api.HookResult{}, notFound()
	}
	spec, err := appSpec(app)
	if err != nil || spec.Source == nil || spec.Source.WebhookSecret == "" {
		return api.HookResult{}, notFound()
	}
	key, err := s.secretOf(ctx, app.ProjectName(), spec.Source.WebhookSecret)
	if err != nil {
		return api.HookResult{}, notFound()
	}
	host, err := verifyHook(h, key)
	if err != nil {
		return api.HookResult{}, err
	}
	var body gitPayload
	if err := json.Unmarshal(h.Body, &body); err != nil {
		return api.HookResult{}, api.NewError(api.CodeMalformedRequest, "The webhook's body isn't JSON.")
	}
	event := h.Header.Get("X-GitHub-Event")
	if host == "gitlab" {
		event = body.ObjectKind
	}
	result, preview, err := s.hookAction(ctx, app, spec, host, event, body)
	if err != nil {
		return api.HookResult{}, err
	}
	s.Log.Info("git webhook", "resource_id", app.ID, "host", host, "event", event, "result", result, "preview", preview)
	return api.HookResult{Result: result, Preview: preview}, nil
}

// verifyHook checks a GitHub signature or a GitLab token in constant time, and says which it was.
func verifyHook(h GitHook, key string) (string, error) {
	if sig := h.Header.Get("X-Hub-Signature-256"); sig != "" {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(h.Body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(sig), []byte(want)) {
			return "github", nil
		}
	} else if tok := h.Header.Get("X-Gitlab-Token"); tok != "" {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(key)) == 1 {
			return "gitlab", nil
		}
	}
	return "", api.NewError(api.CodeUnauthenticated, "The webhook's signature doesn't match. Use the app's webhook secret as the webhook's secret (GitHub) or token (GitLab).")
}

func (s *Resources) hookAction(ctx context.Context, app *domain.Resource, spec api.AppSpec, host, event string, b gitPayload) (string, string, error) {
	switch {
	case event == "ping":
		return api.HookPong, "", nil
	case (event == "push" && host == "github") || (event == "push" && host == "gitlab"):
		if !spec.Source.DeployOnPush || b.Ref != "refs/heads/"+spec.Source.Ref {
			return api.HookIgnored, "", nil
		}
		if _, err := s.Action(ctx, hookPrincipal, app.ID, api.ActionRedeploy); err != nil {
			return "", "", err
		}
		return api.HookDeployed, "", nil
	}
	n, action := b.Number, b.Action
	if host == "gitlab" && event == "merge_request" && b.Attributes != nil {
		n, action = b.Attributes.IID, map[string]string{"open": "opened", "reopen": "reopened", "update": "synchronize", "close": "closed", "merge": "closed"}[b.Attributes.Action]
	} else if host != "github" || event != "pull_request" {
		return api.HookIgnored, "", nil
	}
	if n <= 0 || !spec.Previews.On() {
		return api.HookIgnored, "", nil
	}
	name := api.PreviewNameForPR(app.Name, n)
	switch action {
	case "opened", "reopened", "synchronize":
		var out *domain.Resource
		var queued []string
		var result string
		err := s.Store.WithTx(ctx, func(q *store.Queries) error {
			var err error
			out, queued, result, err = s.upsertPreview(ctx, q, hookPrincipal, app, name, api.PullRequestRef(host, n), n)
			return err
		})
		if err != nil {
			return "", "", err
		}
		for _, id := range queued {
			s.Queue.Enqueue(id)
		}
		return result, out.Name, nil
	case "closed":
		pv, err := s.Store.GetResourceByName(ctx, app.ProjectName(), api.KindApp, name)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (pv.OwnerID == nil || *pv.OwnerID != app.ID)) {
			return api.HookIgnored, "", nil
		}
		if err != nil {
			return "", "", err
		}
		if _, err := s.Delete(ctx, hookPrincipal, pv.ID, nil); err != nil {
			return "", "", err
		}
		_ = s.Store.AddEvent(ctx, app.ID, "preview_deleted", fmt.Sprintf("Preview %s deleted: pull request %d was closed.", name, n), s.Now())
		return api.HookDeleted, name, nil
	}
	return api.HookIgnored, "", nil
}
