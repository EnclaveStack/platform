// Package gate serves the gate listener (ADR-053): the edge proxy's check for every gated request, and
// the sign-in pages a visitor meets on a public hostname. It answers only requests that carry the edge's
// key; everything else gets a plain 404.
package gate

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Headers the edge sends (ADR-053, internal/engine/docker/edge_public.go).
const (
	HeaderEdgeKey  = "X-Enclave-Edge-Key"
	HeaderTarget   = "X-Enclave-Gate-Target"
	HeaderClientIP = "X-Enclave-Client-IP"
)

// Paths on the public hostname (L3-002 §16.4).
const (
	PathCheck     = "/check"
	PathSignin    = "/.enclave/gate/signin"
	PathSignout   = "/.enclave/gate/signout"
	PathSignedOut = "/.enclave/gate/signedout"
	PathStyle     = "/.enclave/gate/static/gate.css"
)

//go:embed static/gate.css templates/*.html
var files embed.FS

var pages = template.Must(template.ParseFS(files, "templates/*.html"))

// Handler is the gate listener's handler.
type Handler struct {
	Gate    *service.Gate
	EdgeKey string
	Log     *slog.Logger
	Metrics *observability.Metrics
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.EdgeKey == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderEdgeKey)), []byte(h.EdgeKey)) != 1 {
		http.NotFound(w, r) // only the edge proxy may use the gate
		return
	}
	switch {
	case r.URL.Path == PathCheck && r.Method == http.MethodGet:
		h.check(w, r)
	case r.URL.Path == PathStyle && r.Method == http.MethodGet:
		css, _ := files.ReadFile("static/gate.css")
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(css)
	case r.URL.Path == PathSignin && r.Method == http.MethodGet:
		h.signinPage(w, r, "", http.StatusOK)
	case r.URL.Path == PathSignin && r.Method == http.MethodPost:
		h.signin(w, r)
	case r.URL.Path == PathSignout && r.Method == http.MethodPost:
		h.signout(w, r)
	case r.URL.Path == PathSignedOut && r.Method == http.MethodGet:
		h.render(w, "signedout.html", h.view(r), http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// host is the public hostname the visitor asked for, without a port.
func host(r *http.Request) string {
	h := r.Header.Get("X-Forwarded-Host")
	if h == "" {
		h = r.Host
	}
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.ToLower(h)
}

func cookie(r *http.Request) string {
	c, err := r.Cookie(service.GateCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func (h *Handler) count(result string) {
	if h.Metrics != nil {
		h.Metrics.GateChecks.WithLabelValues(result).Inc()
	}
}

// check answers the edge's forward_auth: 204 to let the request through, a redirect to the sign-in page
// for browsers, 401 for JSON clients, 503 when it can't tell (the gate fails closed).
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := h.Gate.Target(ctx, r.Header.Get(HeaderTarget))
	if errors.Is(err, store.ErrNotFound) {
		h.count("denied")
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.count("error")
		h.Log.Error("gate check failed", "error", err)
		http.Error(w, "The gate can't check this request right now.", http.StatusServiceUnavailable)
		return
	}
	email, result, err := h.Gate.Check(ctx, t, host(r), cookie(r))
	if err != nil {
		h.count("error")
		h.Log.Error("gate check failed", "error", err)
		http.Error(w, "The gate can't check this request right now.", http.StatusServiceUnavailable)
		return
	}
	if result == service.GateAllowed {
		h.count("allowed")
		w.Header().Set(api.UserHeader, email)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.count("signin")
	if strings.Contains(r.Header.Get("Accept"), "application/json") && !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"Sign in at ` + PathSignin + ` to open this environment."}}` + "\n"))
		return
	}
	next := safeNext(r.Header.Get("X-Forwarded-Uri"))
	http.Redirect(w, r, PathSignin+"?next="+url.QueryEscape(next), http.StatusFound)
}

// safeNext keeps only a path on the same host: no scheme, no other host, no control characters.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.ContainsAny(next, "\r\n\t\\") || strings.HasPrefix(next, api.GatePathPrefix) {
		return "/"
	}
	return next
}

type view struct {
	Host    string
	Next    string
	Email   string
	Error   string
	Denied  bool
	Session string
}

func (h *Handler) view(r *http.Request) view {
	return view{Host: host(r), Next: safeNext(r.URL.Query().Get("next"))}
}

func (h *Handler) signinPage(w http.ResponseWriter, r *http.Request, msg string, status int) {
	v := h.view(r)
	v.Error = msg
	v.Session = h.Gate.SessionEmail(r.Context(), cookie(r), v.Host)
	h.render(w, "signin.html", v, status)
}

// sameOrigin checks a form came from the page on this hostname (CSRF).
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin != "" && strings.EqualFold(origin, "https://"+host(r))
}

func (h *Handler) signin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !sameOrigin(r) {
		h.signinPage(w, r, "This form has to be sent from this page. Reload it and try again.", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		h.signinPage(w, r, "That form couldn't be read. Reload the page and try again.", http.StatusBadRequest)
		return
	}
	t, err := h.Gate.Target(ctx, r.Header.Get(HeaderTarget))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := view{Host: host(r), Next: safeNext(r.PostFormValue("next")), Email: r.PostFormValue("email")}
	token, err := h.Gate.SignIn(ctx, t, v.Host, r.PostFormValue("email"), r.PostFormValue("password"), r.Header.Get(HeaderClientIP))
	var ae *api.Error
	switch {
	case errors.Is(err, service.ErrGateNoAccess):
		v.Denied = true
		h.render(w, "private.html", v, http.StatusForbidden)
		return
	case errors.As(err, &ae) && ae.Code == api.CodeTooManyAttempts:
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfterSeconds))
		v.Error = ae.Message
		h.render(w, "signin.html", v, http.StatusTooManyRequests)
		return
	case errors.As(err, &ae):
		v.Error = "That email and password don't match, or you can't open this environment."
		h.render(w, "signin.html", v, http.StatusUnauthorized)
		return
	case err != nil:
		h.Log.Error("gate sign-in failed", "error", err)
		v.Error = "Signing in didn't work just now. Try again in a moment."
		h.render(w, "signin.html", v, http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: service.GateCookie, Value: token, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(service.GateSessionTTL / time.Second)})
	http.Redirect(w, r, v.Next, http.StatusSeeOther)
}

func (h *Handler) signout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "This form has to be sent from this page.", http.StatusForbidden)
		return
	}
	if err := h.Gate.SignOut(r.Context(), cookie(r), host(r), r.Header.Get(HeaderClientIP)); err != nil {
		h.Log.Error("gate sign-out failed", "error", err)
	}
	http.SetCookie(w, &http.Cookie{Name: service.GateCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, PathSignedOut, http.StatusSeeOther)
}

func (h *Handler) render(w http.ResponseWriter, page string, v view, status int) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, page, v); err != nil {
		h.Log.Error("render gate page failed", "page", page, "error", err)
	}
}

// Sweep removes expired sessions every interval until ctx ends.
func (h *Handler) Sweep(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.Gate.Sweep(ctx)
		}
	}
}
