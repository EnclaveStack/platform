// Package client is the Go SDK for the EnclaveStack control-plane API (L3-002). The CLI and the OpenTofu
// provider use it; so can anyone's programs. It authenticates with a bearer token (or a session
// cookie after SignIn), retries transient failures with backoff (L3-007 §1), sends an
// Idempotency-Key with every create so retries are safe, and returns *api.Error for API errors.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Retry policy defaults.
const (
	DefaultMaxAttempts = 3
	DefaultBaseDelay   = 250 * time.Millisecond
	MaxRetryAfter      = 30 * time.Second
)

// Client calls one control plane.
type Client struct {
	BaseURL     string
	Token       string
	UserAgent   string
	HTTP        *http.Client
	MaxAttempts int
	BaseDelay   time.Duration
	// Sleep waits between retries; tests replace it.
	Sleep func(context.Context, time.Duration) error
	// Project is where CreateResource creates and FindResource and ListResources look; "" means the
	// default project (ADR-035).
	Project string
}

// InProject returns a copy of the client working in project; it shares the connection.
func (c *Client) InProject(project string) *Client {
	cp := *c
	cp.Project = project
	return &cp
}

func (c *Client) project() string {
	if c.Project == "" {
		return api.DefaultProject
	}
	return c.Project
}

// New returns a client for baseURL (e.g. http://127.0.0.1:7070) using token (may be empty).
func New(baseURL, token string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), Token: token, UserAgent: "enclave-go-client",
		HTTP: &http.Client{Timeout: 60 * time.Second, Jar: jar}, MaxAttempts: DefaultMaxAttempts, BaseDelay: DefaultBaseDelay,
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// Code returns the API error code of err, or "".
func Code(err error) api.ErrorCode {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool { return Code(err) == api.CodeNotFound }

// NetworkError is returned when the server cannot be reached after all retries.
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("can't reach EnclaveStack at %s: %v", e.URL, e.Err)
}
func (e *NetworkError) Unwrap() error { return e.Err }

type request struct {
	method   string
	path     string
	query    url.Values
	body     any
	ifMatch  *int64
	idemKey  string
	response *http.Response
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "es-" + hex.EncodeToString(b)
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// backoff returns the wait before retry n (n ≥ 1): base·2ⁿ⁻¹ ± 20 %.
func (c *Client) backoff(n int) time.Duration {
	d := float64(c.BaseDelay) * math.Pow(2, float64(n-1))
	return time.Duration(d * (0.8 + 0.4*mrand.Float64()))
}

func (c *Client) do(ctx context.Context, req *request, out any) error {
	var payload []byte
	if req.body != nil {
		b, err := json.Marshal(req.body)
		if err != nil {
			return err
		}
		payload = b
	}
	u := c.BaseURL + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	attempts := max(c.MaxAttempts, 1)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		hreq, err := http.NewRequestWithContext(ctx, req.method, u, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if payload != nil {
			hreq.Header.Set("Content-Type", "application/json")
		}
		hreq.Header.Set("Accept", "application/json")
		hreq.Header.Set("User-Agent", c.UserAgent)
		hreq.Header.Set(api.HeaderCSRF, "1") // required for cookie sessions; harmless with tokens
		if c.Token != "" {
			hreq.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if req.idemKey != "" {
			hreq.Header.Set(api.HeaderIdempotencyKey, req.idemKey)
		}
		if req.ifMatch != nil {
			hreq.Header.Set("If-Match", `"`+strconv.FormatInt(*req.ifMatch, 10)+`"`)
		}
		resp, err := c.HTTP.Do(hreq)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = &NetworkError{URL: c.BaseURL, Err: err}
			if attempt < attempts && c.safeToRetry(req) {
				if err := c.Sleep(ctx, c.backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return lastErr
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		req.response = resp
		if readErr != nil {
			lastErr = &NetworkError{URL: c.BaseURL, Err: readErr}
			if attempt < attempts && c.safeToRetry(req) {
				if err := c.Sleep(ctx, c.backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return lastErr
		}
		if resp.StatusCode >= 400 {
			apiErr := decodeError(resp, body)
			if retryable(resp.StatusCode) && attempt < attempts && c.safeToRetry(req) {
				wait := c.backoff(attempt)
				if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
					wait = min(time.Duration(ra)*time.Second, MaxRetryAfter)
				}
				if err := c.Sleep(ctx, wait); err != nil {
					return err
				}
				lastErr = apiErr
				continue
			}
			return apiErr
		}
		if raw, ok := out.(*[]byte); ok {
			*raw = body
			return nil
		}
		if out != nil && len(body) > 0 {
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decode %s %s response: %w", req.method, req.path, err)
			}
		}
		return nil
	}
	return lastErr
}

// safeToRetry: reads and deletes are idempotent; POSTs only with an idempotency key; PATCH with If-Match.
func (c *Client) safeToRetry(req *request) bool {
	switch req.method {
	case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut:
		return true
	case http.MethodPost:
		return req.idemKey != "" || strings.HasSuffix(req.path, "/actions/reconcile")
	case http.MethodPatch:
		return req.ifMatch != nil
	}
	return false
}

func decodeError(resp *http.Response, body []byte) error {
	var env api.ErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Code != "" {
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			env.Error.RetryAfterSeconds = ra
		}
		if env.Error.RequestID == "" {
			env.Error.RequestID = resp.Header.Get(api.HeaderRequestID)
		}
		return env.Error
	}
	code := api.CodeInternal
	for c, status := range api.ErrorCodes {
		if status == resp.StatusCode && (c == api.CodeNotFound || c == api.CodeUnauthenticated || c == api.CodeRateLimited || c == api.CodeEngineUnavailable) {
			code = c
		}
	}
	return &api.Error{Code: code, Message: fmt.Sprintf("Unexpected %s from the server.", resp.Status), RequestID: resp.Header.Get(api.HeaderRequestID)}
}

// GetSetup reports whether first-run setup is still required.
func (c *Client) GetSetup(ctx context.Context) (api.SetupStatus, error) {
	var out api.SetupStatus
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/setup"}, &out)
}

// Setup completes first-run setup; the client keeps the session cookie.
func (c *Client) Setup(ctx context.Context, req api.SetupRequest) (api.Session, error) {
	var out api.Session
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/setup", body: req}, &out)
}

// SignIn starts a cookie session (kept in the client's jar).
func (c *Client) SignIn(ctx context.Context, email, password string) (api.Session, error) {
	var out api.Session
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/session", body: api.SignInRequest{Email: email, Password: password}}, &out)
}

// Session returns the current principal.
func (c *Client) Session(ctx context.Context) (api.Session, error) {
	var out api.Session
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/session"}, &out)
}

// SignOut ends a cookie session.
func (c *Client) SignOut(ctx context.Context) error {
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/session"}, nil)
}

// ListTokens lists the caller's API tokens; all lists everyone's (machine admins).
func (c *Client) ListTokens(ctx context.Context, all bool) ([]api.Token, error) {
	q := url.Values{}
	if all {
		q.Set("all", "true")
	}
	var out api.TokenList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/tokens", query: q}, &out)
}

// ChangePassword changes the signed-in user's password (sessions only).
func (c *Client) ChangePassword(ctx context.Context, req api.ChangePassword) error {
	return c.do(ctx, &request{method: http.MethodPut, path: "/api/v1/session/password", body: req}, nil)
}

// InviteInfo says who an invite or reset code is for.
func (c *Client) InviteInfo(ctx context.Context, code string) (api.InviteInfo, error) {
	var out api.InviteInfo
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/invite", query: url.Values{"code": {code}}}, &out)
}

// AcceptInvite sets a password from a link; the client keeps the session cookie.
func (c *Client) AcceptInvite(ctx context.Context, code, password string) (api.Session, error) {
	var out api.Session
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/invite", body: api.AcceptInvite{Code: code, Password: password}}, &out)
}

// ListProjects returns the projects the caller can see.
func (c *Client) ListProjects(ctx context.Context) ([]api.Project, error) {
	var out api.ProjectList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/projects"}, &out)
}

// GetProject returns one project.
func (c *Client) GetProject(ctx context.Context, name string) (api.Project, error) {
	var out api.Project
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/projects/" + url.PathEscape(name)}, &out)
}

// CreateProject makes a project.
func (c *Client) CreateProject(ctx context.Context, req api.CreateProject) (api.Project, error) {
	var out api.Project
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/projects", body: req, idemKey: newIdempotencyKey()}, &out)
}

// UpdateProject changes a project's description.
func (c *Client) UpdateProject(ctx context.Context, name string, req api.UpdateProject) (api.Project, error) {
	var out api.Project
	return out, c.do(ctx, &request{method: http.MethodPatch, path: "/api/v1/projects/" + url.PathEscape(name), body: req}, &out)
}

// DeleteProject deletes an empty project.
func (c *Client) DeleteProject(ctx context.Context, name string) error {
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/projects/" + url.PathEscape(name)}, nil)
}

// ListUsers returns the people the caller administers.
func (c *Client) ListUsers(ctx context.Context) ([]api.UserSummary, error) {
	var out api.UserList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/users"}, &out)
}

// FindUser looks a person up by email; a missing one returns a NOT_FOUND *api.Error.
func (c *Client) FindUser(ctx context.Context, email string) (api.UserSummary, error) {
	users, err := c.ListUsers(ctx)
	if err != nil {
		return api.UserSummary{}, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return api.UserSummary{}, &api.Error{Code: api.CodeNotFound, Message: "There's no person with the email " + email + "."}
}

// GetUser returns one person.
func (c *Client) GetUser(ctx context.Context, id string) (api.UserSummary, error) {
	var out api.UserSummary
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/users/" + url.PathEscape(id)}, &out)
}

// InviteUser invites a person; the result holds the link, shown once.
func (c *Client) InviteUser(ctx context.Context, req api.InviteUser) (api.Invitation, error) {
	var out api.Invitation
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/users", body: req}, &out)
}

// UpdateUser changes a person's roles. A nil machineRole with setMachineRole removes it.
func (c *Client) UpdateUser(ctx context.Context, id string, setMachineRole bool, machineRole *string, projectRoles map[string]*string) (api.UserSummary, error) {
	body := map[string]any{}
	if setMachineRole {
		body["machineRole"] = machineRole
	}
	if len(projectRoles) > 0 {
		body["projectRoles"] = projectRoles
	}
	var out api.UserSummary
	return out, c.do(ctx, &request{method: http.MethodPatch, path: "/api/v1/users/" + url.PathEscape(id), body: body}, &out)
}

// DeleteUser removes a person.
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/users/" + url.PathEscape(id)}, nil)
}

// ResetUser issues a new invite or password-reset link.
func (c *Client) ResetUser(ctx context.Context, id string) (api.Invitation, error) {
	var out api.Invitation
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/users/" + url.PathEscape(id) + "/reset"}, &out)
}

// Audit returns a page of the audit list, newest first.
func (c *Client) Audit(ctx context.Context, limit int, cursor string) (api.AuditList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out api.AuditList
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/audit", query: q}, &out)
}

// CreateToken issues a token; the secret is in the result and is never shown again.
func (c *Client) CreateToken(ctx context.Context, req api.CreateTokenRequest) (api.CreatedToken, error) {
	var out api.CreatedToken
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/tokens", body: req, idemKey: newIdempotencyKey()}, &out)
}

// RevokeToken revokes a token by ID.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/tokens/" + url.PathEscape(id)}, nil)
}

// Machine describes the host.
func (c *Client) Machine(ctx context.Context) (api.Machine, error) {
	var out api.Machine
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machine"}, &out)
}

// UpdateMachine sets the machine's public address and ACME email; nil or "" clears a setting.
func (c *Client) UpdateMachine(ctx context.Context, settings api.MachineSettings) (api.Machine, error) {
	var out api.Machine
	return out, c.do(ctx, &request{method: http.MethodPatch, path: "/api/v1/machine", body: settings}, &out)
}

// RootCA returns the PEM root certificate of the edge's internal CA.
func (c *Client) RootCA(ctx context.Context) ([]byte, error) {
	var out []byte
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machine/ca"}, &out)
}

// ListOptions filter ListResources. Without Project, the client's project is listed; AllProjects lists
// every project the caller can see.
type ListOptions struct {
	Project     string
	AllProjects bool
	Kind        api.Kind
	Name        string
	// Machine and Stack list what runs on a machine and a stack's members (v0.8).
	Machine string
	Stack   string
	Limit   int
	Cursor  string
}

// ListResources returns one page.
func (c *Client) ListResources(ctx context.Context, o ListOptions) (api.ResourceList, error) {
	q := url.Values{}
	switch {
	case o.Project != "":
		q.Set("project", o.Project)
	case !o.AllProjects:
		q.Set("project", c.project())
	}
	if o.Kind != "" {
		q.Set("kind", string(o.Kind))
	}
	if o.Name != "" {
		q.Set("name", o.Name)
	}
	if o.Machine != "" {
		q.Set("machine", o.Machine)
	}
	if o.Stack != "" {
		q.Set("stack", o.Stack)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	var out api.ResourceList
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources", query: q}, &out)
}

// ListAllResources follows cursors until the last page, in the client's project.
func (c *Client) ListAllResources(ctx context.Context, kind api.Kind) ([]api.Resource, error) {
	return c.ListAllResourcesIn(ctx, kind, false)
}

// ListAllResourcesIn is ListAllResources, across every project the caller can see when allProjects is set.
func (c *Client) ListAllResourcesIn(ctx context.Context, kind api.Kind, allProjects bool) ([]api.Resource, error) {
	return c.ListAll(ctx, ListOptions{Kind: kind, AllProjects: allProjects})
}

// ListAll pages through every resource matching o.
func (c *Client) ListAll(ctx context.Context, o ListOptions) ([]api.Resource, error) {
	var all []api.Resource
	o.Limit, o.Cursor = 200, ""
	for {
		page, err := c.ListResources(ctx, o)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			return all, nil
		}
		o.Cursor = *page.NextCursor
	}
}

// FindResource looks a resource up by kind and name; a missing one returns a NOT_FOUND *api.Error.
func (c *Client) FindResource(ctx context.Context, kind api.Kind, name string) (api.Resource, error) {
	page, err := c.ListResources(ctx, ListOptions{Kind: kind, Name: name, Limit: 1})
	if err != nil {
		return api.Resource{}, err
	}
	if len(page.Items) == 0 {
		msg := fmt.Sprintf("There's no %s named %s.", kind, name)
		if c.project() != api.DefaultProject {
			msg = fmt.Sprintf("There's no %s named %s in the project %s.", kind, name, c.project())
		}
		return api.Resource{}, &api.Error{Code: api.CodeNotFound, Message: msg}
	}
	return page.Items[0], nil
}

// GetResource returns a resource by ID.
func (c *Client) GetResource(ctx context.Context, id string) (api.Resource, error) {
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id)}, &out)
}

// CreateResource creates a resource; spec is any JSON-encodable spec (api.AppSpec, …).
func (c *Client) CreateResource(ctx context.Context, kind api.Kind, name string, spec any) (api.Resource, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return api.Resource{}, err
	}
	var out api.Resource
	err = c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/resources", idemKey: newIdempotencyKey(),
		body: api.CreateResourceRequest{Kind: kind, Name: name, Project: c.project(), Spec: raw}}, &out)
	return out, err
}

// UpdateResource patches a resource's spec. ifMatch (may be nil) guards against concurrent edits.
func (c *Client) UpdateResource(ctx context.Context, id string, patch any, ifMatch *int64) (api.Resource, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return api.Resource{}, err
	}
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodPatch, path: "/api/v1/resources/" + url.PathEscape(id), ifMatch: ifMatch,
		body: api.UpdateResourceRequest{Spec: raw}}, &out)
}

// DeleteResource starts deleting a resource.
func (c *Client) DeleteResource(ctx context.Context, id string, ifMatch *int64) (api.Resource, error) {
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/resources/" + url.PathEscape(id), ifMatch: ifMatch}, &out)
}

// Action runs restart, redeploy or reconcile.
func (c *Client) Action(ctx context.Context, id, action string) (api.Resource, error) {
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/resources/" + url.PathEscape(id) + "/actions/" + url.PathEscape(action)}, &out)
}

// RunNow starts a run of a job (the run action). It is not retried: a retry could start a second run.
func (c *Client) RunNow(ctx context.Context, id string) (api.Run, error) {
	var out api.Run
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/resources/" + url.PathEscape(id) + "/actions/" + api.ActionRun}, &out)
}

// Restore restores a backup run into a new database or cache, or over the source (mode replace). It
// returns the resource being restored into.
func (c *Client) Restore(ctx context.Context, id string, req api.RestoreRequest) (api.Resource, error) {
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/resources/" + url.PathEscape(id) + "/actions/" + api.ActionRestore, body: req}, &out)
}

// ListRuns returns one page of a job's or backup's runs, newest first.
func (c *Client) ListRuns(ctx context.Context, id string, limit int, cursor string) (api.RunList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out api.RunList
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/runs", query: q}, &out)
}

// GetRun returns one run with its output tail.
func (c *Client) GetRun(ctx context.Context, id, runID string) (api.RunDetail, error) {
	var out api.RunDetail
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/runs/" + url.PathEscape(runID)}, &out)
}

// Events returns a resource's newest events.
func (c *Client) Events(ctx context.Context, id string, limit int) ([]api.Event, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out api.EventList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/events", query: q}, &out)
}

// Logs returns the last tail lines.
// BuildLogs returns the end of a source app's latest build log (ADR-019).
func (c *Client) BuildLogs(ctx context.Context, id string, tail int) ([]api.LogLine, error) {
	q := url.Values{"build": {"true"}}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	}
	var out api.LogList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/logs", query: q}, &out)
}

func (c *Client) Logs(ctx context.Context, id string, tail int) ([]api.LogLine, error) {
	q := url.Values{}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	}
	var out api.LogList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/logs", query: q}, &out)
}

// RevealSecret returns a secret's value (audited on the server).
func (c *Client) RevealSecret(ctx context.Context, id string) (string, error) {
	var out api.SecretValue
	return out.Value, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/value"}, &out)
}

// FailedError is returned by WaitReady when the resource ends in the failed phase.
type FailedError struct {
	Resource api.Resource
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("%s %s failed: %s", e.Resource.Kind, e.Resource.Name, e.Resource.Status.Message)
}

// WaitReady polls until the resource is ready at its current generation, failed, or ctx ends.
// progress (may be nil) is called whenever the resource changes.
func (c *Client) WaitReady(ctx context.Context, id string, poll time.Duration, progress func(api.Resource)) (api.Resource, error) {
	var last api.Resource
	for {
		r, err := c.GetResource(ctx, id)
		if err != nil {
			return r, err
		}
		if progress != nil && (r.UpdatedAt != last.UpdatedAt || r.Status.Phase != last.Status.Phase) {
			progress(r)
		}
		last = r
		switch {
		case r.Status.Phase == api.PhaseReady && r.Status.ObservedGeneration >= r.Generation:
			return r, nil
		case r.Status.Phase == api.PhaseFailed:
			return r, &FailedError{Resource: r}
		}
		if err := c.Sleep(ctx, poll); err != nil {
			return r, err
		}
	}
}

// WaitGone polls until the resource no longer exists.
func (c *Client) WaitGone(ctx context.Context, id string, poll time.Duration) error {
	for {
		_, err := c.GetResource(ctx, id)
		if IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := c.Sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// ─── v0.6 data endpoints (L3-002 §9.3) ─────────────────────────────────────

func resourcePath(id, rest string) string { return "/api/v1/resources/" + url.PathEscape(id) + rest }

// SendMessage sends to a queue or publishes to a topic. It is not retried: a repeat would send twice
// unless the message carries a deduplication ID.
func (c *Client) SendMessage(ctx context.Context, id string, m api.SendMessage) (api.SentMessage, error) {
	var out api.SentMessage
	return out, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/messages"), body: m}, &out)
}

// Messages returns a queue's stats and up to limit waiting messages, without receiving them.
func (c *Client) Messages(ctx context.Context, id string, limit int) (api.MessageList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out api.MessageList
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/messages"), query: q}, &out)
}

// ReceiveMessages takes messages from a queue, long-polling up to the request's wait.
func (c *Client) ReceiveMessages(ctx context.Context, id string, req api.ReceiveMessages) ([]api.ReceivedMessage, error) {
	var out api.ReceivedList
	return out.Items, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/messages/receive"), body: req}, &out)
}

// DeleteMessages deletes received messages by receipt.
func (c *Client) DeleteMessages(ctx context.Context, id string, receipts []string) (api.DeletedMessages, error) {
	var out api.DeletedMessages
	return out, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/messages/delete"), body: api.DeleteMessages{Receipts: receipts}}, &out)
}

// Deliveries returns a topic's delivery counts per subscription.
func (c *Client) Deliveries(ctx context.Context, id string) (api.DeliveryStats, error) {
	var out api.DeliveryStats
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/deliveries")}, &out)
}

// SendEmail sends a message through an email service.
func (c *Client) SendEmail(ctx context.Context, id string, m api.SendEmail) (api.SentEmail, error) {
	var out api.SentEmail
	return out, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/emails"), body: m}, &out)
}

// Emails lists an email service's kept messages, newest first.
func (c *Client) Emails(ctx context.Context, id string, limit int) (api.EmailList, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out api.EmailList
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/emails"), query: q}, &out)
}

// Email returns one kept message.
func (c *Client) Email(ctx context.Context, id, emailID string) (api.Email, error) {
	var out api.Email
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/emails/"+url.PathEscape(emailID))}, &out)
}

// Transcode converts one file of a transcoder's input folder now.
func (c *Client) Transcode(ctx context.Context, id, key string) (api.Run, error) {
	var out api.Run
	return out, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/actions/"+api.ActionRun), body: api.TranscodeRequest{Key: key}}, &out)
}

// v0.7 (L3-002 §10.4, §10.5).

func metricQuery(metrics []string, from, to time.Time) url.Values {
	q := url.Values{}
	for _, m := range metrics {
		q.Add("metric", m)
	}
	if !from.IsZero() {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if !to.IsZero() {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}
	return q
}

// ResourceMetrics returns a resource's series; no metrics means all of its kind's. Zero times mean the
// last hour.
func (c *Client) ResourceMetrics(ctx context.Context, id string, metrics []string, from, to time.Time) (api.MetricData, error) {
	var out api.MetricData
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/metrics"), query: metricQuery(metrics, from, to)}, &out)
}

// MachineMetrics returns the machine's series.
func (c *Client) MachineMetrics(ctx context.Context, metrics []string, from, to time.Time) (api.MetricData, error) {
	var out api.MetricData
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machine/metrics", query: metricQuery(metrics, from, to)}, &out)
}

// MachineMetricsOf returns one machine's metrics, by its name.
func (c *Client) MachineMetricsOf(ctx context.Context, name string, metrics []string, from, to time.Time) (api.MetricData, error) {
	var out api.MetricData
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machines/" + url.PathEscape(name) + "/metrics", query: metricQuery(metrics, from, to)}, &out)
}

// Test runs the test action of an alert, channel or secret store and returns each outcome.
func (c *Client) Test(ctx context.Context, id string) (api.TestResult, error) {
	var out api.TestResult
	return out, c.do(ctx, &request{method: http.MethodPost, path: resourcePath(id, "/actions/test")}, &out)
}

// Incidents lists an alert's incidents, newest first.
func (c *Client) Incidents(ctx context.Context, id string, limit int) ([]api.Incident, error) {
	var out api.IncidentList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/incidents"), query: url.Values{"limit": {strconv.Itoa(limit)}}}, &out)
}

// Notifications lists a channel's deliveries, newest first.
func (c *Client) Notifications(ctx context.Context, id string, limit int) ([]api.Notification, error) {
	var out api.NotificationList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/notifications"), query: url.Values{"limit": {strconv.Itoa(limit)}}}, &out)
}

// Machines (v0.8, L3-002 §11.2).

// ListMachines returns every machine, the main machine first.
func (c *Client) ListMachines(ctx context.Context) ([]api.MachineSummary, error) {
	var out api.MachineList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machines"}, &out)
}

// GetMachineByName returns one machine.
func (c *Client) GetMachineByName(ctx context.Context, name string) (api.MachineSummary, error) {
	var out api.MachineSummary
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machines/" + url.PathEscape(name)}, &out)
}

// AddMachine adds a machine waiting for its agent and returns its one-time join code.
func (c *Client) AddMachine(ctx context.Context, req api.AddMachine) (api.AddedMachine, error) {
	var out api.AddedMachine
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/machines", body: req}, &out)
}

// UpdateMachineByName changes a machine's labels or whether it is schedulable.
func (c *Client) UpdateMachineByName(ctx context.Context, name string, req api.UpdateMachine) (api.MachineSummary, error) {
	var out api.MachineSummary
	return out, c.do(ctx, &request{method: http.MethodPatch, path: "/api/v1/machines/" + url.PathEscape(name), body: req}, &out)
}

// RemoveMachine removes a machine; force removes an offline one with resources on it.
func (c *Client) RemoveMachine(ctx context.Context, name string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/machines/" + url.PathEscape(name), query: q}, nil)
}

// NewJoinCode replaces a waiting machine's join code.
func (c *Client) NewJoinCode(ctx context.Context, name string) (api.AddedMachine, error) {
	var out api.AddedMachine
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/machines/" + url.PathEscape(name) + "/join-code"}, &out)
}

// DrainMachine marks a machine not schedulable and moves what isn't pinned there.
func (c *Client) DrainMachine(ctx context.Context, name string) (api.DrainResult, error) {
	var out api.DrainResult
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/machines/" + url.PathEscape(name) + "/drain"}, &out)
}

// MachineEvents returns a machine's newest events.
func (c *Client) MachineEvents(ctx context.Context, name string) ([]api.Event, error) {
	var out api.EventList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/machines/" + url.PathEscape(name) + "/events"}, &out)
}

// JoinMachine is the agent's call with its join code (ADR-043); it needs no credentials.
func (c *Client) JoinMachine(ctx context.Context, req api.JoinRequest) (api.JoinResponse, error) {
	var out api.JoinResponse
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/machines/join", body: req}, &out)
}

// The catalogue (v0.8, L3-002 §11.5).

// Catalogue lists the catalogue's entries.
func (c *Client) Catalogue(ctx context.Context) ([]api.CatalogueEntry, error) {
	var out api.CatalogueList
	return out.Items, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/catalogue"}, &out)
}

// CatalogueEntry returns one entry ("" for its newest version).
func (c *Client) CatalogueEntry(ctx context.Context, name, version string) (api.CatalogueEntry, error) {
	q := url.Values{}
	if version != "" {
		q.Set("version", version)
	}
	var out api.CatalogueEntry
	return out, c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/catalogue/" + url.PathEscape(name), query: q}, &out)
}

// TemplateSource returns an entry's template as written ("" for its newest version).
func (c *Client) TemplateSource(ctx context.Context, name, version string) (string, error) {
	q := url.Values{}
	if version != "" {
		q.Set("version", version)
	}
	var out []byte
	err := c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/catalogue/" + url.PathEscape(name) + "/template", query: q}, &out)
	return string(out), err
}

// AddTemplate adds one of the machine's own templates (YAML or JSON).
func (c *Client) AddTemplate(ctx context.Context, body string) (api.CatalogueEntry, error) {
	var out api.CatalogueEntry
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/catalogue", body: api.AddTemplate{Body: body}}, &out)
}

// RemoveTemplate removes one of the machine's own templates.
func (c *Client) RemoveTemplate(ctx context.Context, name string) error {
	return c.do(ctx, &request{method: http.MethodDelete, path: "/api/v1/catalogue/" + url.PathEscape(name)}, nil)
}
