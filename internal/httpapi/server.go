// Package httpapi is the control plane's HTTP surface (L3-002): routing, middleware, JSON encoding and
// the embedded console. Handlers translate HTTP to service calls and nothing else.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/webui"
	"github.com/enclavestack/platform/pkg/api"
)

// Config tunes the HTTP layer.
type Config struct {
	// RateLimit is requests per second per client (burst is twice this). 0 uses 100.
	RateLimit float64
	// SecureCookies sets the Secure flag (when served over TLS).
	SecureCookies bool
	// Assets are the console files; nil serves a "not built" page.
	Assets fs.FS
	// Machines serves /machines (v0.8).
	Machines *service.Machines
}

// Server wires handlers to services.
type Server struct {
	auth      *service.Auth
	resources *service.Resources
	idem      *service.Idempotency
	ready     func(context.Context) error
	log       *slog.Logger
	metrics   *observability.Metrics
	limiter   *limiter
	cfg       Config
	handler   http.Handler
}

// New builds the HTTP handler.
func New(a *service.Auth, res *service.Resources, idem *service.Idempotency, ready func(context.Context) error,
	log *slog.Logger, m *observability.Metrics, cfg Config) *Server {
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 100
	}
	s := &Server{auth: a, resources: res, idem: idem, ready: ready, log: log, metrics: m,
		limiter: newLimiter(cfg.RateLimit, int(cfg.RateLimit*2)), cfg: cfg}

	mux := http.NewServeMux()
	s.route(mux, "GET /healthz", public, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.route(mux, "GET /readyz", public, s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	s.route(mux, "GET /api/v1/setup", public, s.getSetup)
	s.route(mux, "POST /api/v1/setup", public, s.postSetup)
	s.route(mux, "POST /api/v1/session", public, s.postSession)
	s.route(mux, "GET /api/v1/session", signedIn, s.getSession)
	s.route(mux, "DELETE /api/v1/session", signedIn, s.deleteSession)
	s.route(mux, "PUT /api/v1/session/password", signedIn, s.changePassword)
	s.route(mux, "GET /api/v1/invite", public, s.getInvite)
	s.route(mux, "POST /api/v1/invite", public, s.acceptInvite)

	s.route(mux, "GET /api/v1/tokens", account, s.listTokens)
	s.route(mux, "POST /api/v1/tokens", account, s.createToken)
	s.route(mux, "DELETE /api/v1/tokens/{tokenId}", account, s.revokeToken)

	s.route(mux, "GET /api/v1/projects", signedIn, s.listProjects)
	s.route(mux, "POST /api/v1/projects", signedIn, s.createProject)
	s.route(mux, "GET /api/v1/projects/{name}", signedIn, s.getProject)
	s.route(mux, "PATCH /api/v1/projects/{name}", signedIn, s.updateProject)
	s.route(mux, "DELETE /api/v1/projects/{name}", signedIn, s.deleteProject)
	s.route(mux, "GET /api/v1/users", signedIn, s.listUsers)
	s.route(mux, "POST /api/v1/users", signedIn, s.inviteUser)
	s.route(mux, "GET /api/v1/users/{userId}", signedIn, s.getUser)
	s.route(mux, "PATCH /api/v1/users/{userId}", signedIn, s.updateUser)
	s.route(mux, "DELETE /api/v1/users/{userId}", signedIn, s.deleteUser)
	s.route(mux, "POST /api/v1/users/{userId}/reset", signedIn, s.resetUser)
	s.route(mux, "GET /api/v1/audit", signedIn, s.listAudit)

	// v0.8 (L3-002 §11.2).
	s.route(mux, "GET /api/v1/machines", signedIn, s.listMachines)
	s.route(mux, "POST /api/v1/machines", signedIn, s.addMachine)
	s.route(mux, "POST /api/v1/machines/join", public, s.joinMachine)
	s.route(mux, "GET /api/v1/machines/{name}", signedIn, s.getMachineByName)
	s.route(mux, "PATCH /api/v1/machines/{name}", signedIn, s.updateMachine)
	s.route(mux, "DELETE /api/v1/machines/{name}", signedIn, s.removeMachine)
	s.route(mux, "POST /api/v1/machines/{name}/join-code", signedIn, s.machineJoinCode)
	s.route(mux, "POST /api/v1/machines/{name}/drain", signedIn, s.drainMachine)
	s.route(mux, "GET /api/v1/machines/{name}/events", signedIn, s.machineEvents)
	s.route(mux, "GET /api/v1/machines/{name}/metrics", signedIn, s.machineMetrics)
	// v0.8 (L3-002 §11.5).
	s.route(mux, "GET /api/v1/catalogue", signedIn, s.listCatalogue)
	s.route(mux, "POST /api/v1/catalogue", signedIn, s.addTemplate)
	s.route(mux, "GET /api/v1/catalogue/{name}", signedIn, s.getCatalogueEntry)
	s.route(mux, "GET /api/v1/catalogue/{name}/template", signedIn, s.getTemplateSource)
	s.route(mux, "DELETE /api/v1/catalogue/{name}", signedIn, s.removeTemplate)
	s.route(mux, "GET /api/v1/machine", signedIn, s.getMachine)
	s.route(mux, "PATCH /api/v1/machine", signedIn, s.patchMachine)
	s.route(mux, "GET /api/v1/machine/ca", signedIn, s.getRootCA)
	s.route(mux, "GET /api/v1/machine/metrics", signedIn, s.machineMetrics)
	s.route(mux, "GET /api/v1/resources", signedIn, s.listResources)
	s.route(mux, "POST /api/v1/resources", signedIn, s.createResource)
	s.route(mux, "GET /api/v1/resources/{id}", viewer, s.getResource)
	s.route(mux, "PATCH /api/v1/resources/{id}", editor, s.updateResource)
	s.route(mux, "DELETE /api/v1/resources/{id}", editor, s.deleteResource)
	s.route(mux, "POST /api/v1/resources/{id}/actions/{action}", editor, s.action)
	s.route(mux, "GET /api/v1/resources/{id}/events", viewer, s.events)
	s.route(mux, "GET /api/v1/resources/{id}/logs", viewer, s.logs)
	s.route(mux, "GET /api/v1/resources/{id}/value", editor, s.revealSecret)
	s.route(mux, "GET /api/v1/resources/{id}/runs", viewer, s.listRuns)
	// A run's output may hold anything a command printed, so reading it needs an editor (L3-002 §10.3).
	s.route(mux, "GET /api/v1/resources/{id}/runs/{runId}", editor, s.getRun)
	// Data endpoints of queues, topics and email services (L3-002 §9.3). Bodies need an editor, as secrets do.
	s.route(mux, "POST /api/v1/resources/{id}/messages", editor, s.sendMessage)
	s.route(mux, "GET /api/v1/resources/{id}/messages", editor, s.listMessages)
	s.route(mux, "POST /api/v1/resources/{id}/messages/receive", editor, s.receiveMessages)
	s.route(mux, "POST /api/v1/resources/{id}/messages/delete", editor, s.deleteMessages)
	s.route(mux, "GET /api/v1/resources/{id}/deliveries", viewer, s.deliveries)
	s.route(mux, "POST /api/v1/resources/{id}/emails", editor, s.sendEmail)
	s.route(mux, "GET /api/v1/resources/{id}/emails", editor, s.listEmails)
	s.route(mux, "GET /api/v1/resources/{id}/emails/{emailId}", editor, s.getEmail)
	// Bucket objects (L3-002 §14): contents need an editor, as message bodies do.
	s.route(mux, "GET /api/v1/resources/{id}/objects", editor, s.listObjects)
	s.route(mux, "PUT /api/v1/resources/{id}/objects/{key...}", editor, s.putObject)
	s.route(mux, "GET /api/v1/resources/{id}/objects/{key...}", editor, s.getObject)
	s.route(mux, "DELETE /api/v1/resources/{id}/objects/{key...}", editor, s.deleteObject)
	// v0.7 (L3-002 §10.4, §10.5).
	s.route(mux, "GET /api/v1/resources/{id}/metrics", viewer, s.resourceMetrics)
	s.route(mux, "GET /api/v1/resources/{id}/incidents", viewer, s.incidents)
	s.route(mux, "GET /api/v1/resources/{id}/notifications", viewer, s.notifications)

	console := webui.Handler(cfg.Assets)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// Unknown API paths get the JSON envelope, never the console's index.html.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.notFoundRoute(w, r)
			return
		}
		console.ServeHTTP(w, r)
	})

	s.handler = s.observe(s.rateLimit(methodNotAllowedAsJSON(mux, s)))
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) notFoundRoute(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, api.NewError(api.CodeNotFound, "There's no API route "+r.Method+" "+r.URL.Path+"."))
}

// methodNotAllowedAsJSON turns the mux's plain-text 404/405 for API paths into the error envelope.
// A 405 caused only by the console's "GET /" catch-all is really a 404.
func methodNotAllowedAsJSON(mux *http.ServeMux, s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" && strings.HasPrefix(r.URL.Path, "/api/") {
			rec := &bufferedWriter{header: http.Header{}}
			mux.ServeHTTP(rec, r)
			allow := rec.header.Get("Allow")
			if rec.status == http.StatusMethodNotAllowed && allow != "GET, HEAD" {
				w.Header().Set("Allow", allow)
				s.writeError(w, r, api.NewError(api.CodeMethodNotAllowed, r.Method+" isn't allowed on "+r.URL.Path+"."))
				return
			}
			s.notFoundRoute(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedWriter) WriteHeader(code int)        { b.status = code }

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: api.SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.cfg.SecureCookies})
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	required, err := s.auth.SetupRequired(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, api.SetupStatus{Required: required, SuggestedMachineName: api.MachineNameFromHostname(host)})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	var req api.SetupRequest
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	sess, sid, err := s.auth.Setup(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sid, int(service.SessionTTL.Seconds()))
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) postSession(w http.ResponseWriter, r *http.Request) {
	var req api.SignInRequest
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	sess, sid, err := s.auth.SignIn(r.Context(), req, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sid, int(service.SessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Session(r.Context(), principal(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.SignOut(r.Context(), principal(r.Context())); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	items, err := s.auth.ListTokens(r.Context(), principal(r.Context()), all)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.TokenList{Items: items})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req api.CreateTokenRequest
	body, err := decode(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.idempotent(w, r, body, func() (int, any, error) {
		out, err := s.auth.CreateToken(r.Context(), principal(r.Context()), req)
		return http.StatusCreated, out, err
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeToken(r.Context(), principal(r.Context()), r.PathValue("tokenId")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// idempotent runs fn once per Idempotency-Key and replays its response afterwards (ADR-010).
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, body []byte, fn func() (int, any, error)) {
	key := r.Header.Get(api.HeaderIdempotencyKey)
	if key == "" {
		status, out, err := fn()
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeResource(w, status, out)
		return
	}
	if len(key) > 255 {
		s.writeError(w, r, api.Validation([]api.FieldError{{Field: api.HeaderIdempotencyKey, Code: api.FieldTooLong, Message: "Use at most 255 characters."}}))
		return
	}
	p := principal(r.Context())
	hash := service.RequestHash(r.Method, r.URL.Path, body)
	status, stored, found, err := s.idem.Lookup(r.Context(), p, key, hash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if found {
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(stored)
		return
	}
	status, out, err := fn()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	buf, _ := json.Marshal(out)
	buf = append(buf, '\n')
	if err := s.idem.Remember(r.Context(), p, key, hash, status, buf); err != nil {
		observability.Logger(r.Context(), s.log).Warn("store idempotency record failed", "error", err)
	}
	if res, ok := out.(api.Resource); ok {
		w.Header().Set("ETag", etag(res.Generation))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func (s *Server) writeResource(w http.ResponseWriter, status int, out any) {
	if res, ok := out.(api.Resource); ok {
		w.Header().Set("ETag", etag(res.Generation))
	}
	writeJSON(w, status, out)
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	m, err := s.resources.Machine(r.Context(), s.auth.MachineName(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) patchMachine(w http.ResponseWriter, r *http.Request) {
	var req api.MachineSettings
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	m, err := s.resources.UpdateMachine(r.Context(), principal(r.Context()), s.auth.MachineName(r.Context()), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// getRootCA serves the internal CA's root certificate for operators to trust (L2-005 DOM-04).
func (s *Server) getRootCA(w http.ResponseWriter, r *http.Request) {
	pem, err := s.resources.RootCA(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="enclave-root-ca.pem"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pem)
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	out, err := s.resources.List(r.Context(), principal(r.Context()), q.Get("project"), store.ListFilter{Kind: api.Kind(q.Get("kind")), Name: q.Get("name"),
		Machine: q.Get("machine"), Stack: q.Get("stack"), Limit: limit, Cursor: q.Get("cursor")})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createResource(w http.ResponseWriter, r *http.Request) {
	var req api.CreateResourceRequest
	body, err := decode(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.idempotent(w, r, body, func() (int, any, error) {
		out, err := s.resources.Create(r.Context(), principal(r.Context()), req)
		return http.StatusAccepted, out, err
	})
}

func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeResource(w, http.StatusOK, out)
}

func (s *Server) updateResource(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateResourceRequest
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	match, err := ifMatch(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, changed, err := s.resources.Update(r.Context(), principal(r.Context()), r.PathValue("id"), req, match)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if !changed {
		status = http.StatusOK
	}
	s.writeResource(w, status, out)
}

func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request) {
	match, err := ifMatch(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Delete(r.Context(), principal(r.Context()), r.PathValue("id"), match)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeResource(w, http.StatusAccepted, out)
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("action") == api.ActionRun && hasBody(r) { // a transcoder's run names its file (L3-002 §9.2)
		var req api.TranscodeRequest
		if _, err := decode(r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
		run, err := s.resources.RunTranscode(r.Context(), principal(r.Context()), r.PathValue("id"), req)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	if r.PathValue("action") == api.ActionTest && !hasBody(r) { // alerts, channels and secret stores answer at once
		out, err := s.resources.TestAction(r.Context(), principal(r.Context()), r.PathValue("id"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if r.PathValue("action") != api.ActionRestore && hasBody(r) {
		// Only restore (and a transcoder's run) take a body; anything sent to the others is a client mistake, not something to ignore (L3-002 §6.4).
		s.writeError(w, r, api.NewError(api.CodeMalformedRequest, "This action takes no request body."))
		return
	}
	if r.PathValue("action") == api.ActionRestore {
		var req api.RestoreRequest
		if _, err := decode(r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
		out, err := s.resources.Restore(r.Context(), principal(r.Context()), r.PathValue("id"), req)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeResource(w, http.StatusAccepted, out)
		return
	}
	if r.PathValue("action") == api.ActionRun {
		run, err := s.resources.RunNow(r.Context(), principal(r.Context()), r.PathValue("id"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	out, err := s.resources.Action(r.Context(), principal(r.Context()), r.PathValue("id"), r.PathValue("action"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeResource(w, http.StatusAccepted, out)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100, 1, 500)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Events(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	tail, err := queryInt(r, "tail", 200, 1, 1000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	build := false
	switch r.URL.Query().Get("build") {
	case "", "false":
	case "true":
		build = true
	default:
		s.writeError(w, r, api.Validation([]api.FieldError{{Field: "build", Code: api.FieldInvalidFormat, Message: "Use build=true for the latest build's log."}}))
		return
	}
	out, err := s.resources.Logs(r.Context(), r.PathValue("id"), tail, build)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Runs(r.Context(), r.PathValue("id"), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.Run(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) revealSecret(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.RevealSecret(r.Context(), principal(r.Context()), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req api.SendMessage
	if _, err := decodeLimit(r, &req, MaxDataBodyBytes); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.SendMessage(r.Context(), principal(r.Context()), r.PathValue("id"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", api.PeekLimitDefault, 1, api.PeekLimitMax)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Messages(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) receiveMessages(w http.ResponseWriter, r *http.Request) {
	var req api.ReceiveMessages
	if hasBody(r) {
		if _, err := decode(r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	out, err := s.resources.ReceiveMessages(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteMessages(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteMessages
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.DeleteMessages(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deliveries(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.Deliveries(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sendEmail(w http.ResponseWriter, r *http.Request) {
	var req api.SendEmail
	if _, err := decodeLimit(r, &req, MaxDataBodyBytes); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.SendEmail(r.Context(), principal(r.Context()), r.PathValue("id"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) listEmails(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", api.EmailListDefault, 1, api.EmailListMax)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Emails(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getEmail(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.Email(r.Context(), r.PathValue("id"), r.PathValue("emailId"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
