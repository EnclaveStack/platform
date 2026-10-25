package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/pkg/api"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyPrincipal
	keyRoute
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

func principal(ctx context.Context) service.Principal {
	p, _ := ctx.Value(keyPrincipal).(service.Principal)
	return p
}

// routeHolder lets the inner handler report its route pattern to the outer logging middleware.
type routeHolder struct{ pattern string }

func newRequestID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// observe assigns the request ID, recovers panics, applies security headers, and logs and measures
// every request once (ADR-011, ADR-012).
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(api.HeaderRequestID)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(api.HeaderRequestID, id)
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")

		route := &routeHolder{}
		log := s.log.With("request_id", id)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = context.WithValue(ctx, keyRoute, route)
		ctx = observability.WithLogger(ctx, log)
		r = r.WithContext(ctx)
		sw := &statusWriter{ResponseWriter: w}

		defer func() {
			if p := recover(); p != nil {
				log.Error("handler panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if sw.status == 0 {
					s.writeError(sw, r, fmt.Errorf("panic: %v", p))
				}
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			pattern := route.pattern
			if pattern == "" {
				pattern = "static"
			}
			dur := time.Since(start)
			s.metrics.HTTPRequests.WithLabelValues(r.Method, pattern, strconv.Itoa(status)).Inc()
			s.metrics.HTTPDuration.WithLabelValues(r.Method, pattern).Observe(dur.Seconds())
			if pattern == "static" && status < 400 {
				return
			}
			attrs := []any{"method", r.Method, "route", pattern, "status", status, "duration_ms", dur.Milliseconds(), "client_ip", clientIP(r)}
			if p := principal(r.Context()); p.UserID != "" {
				attrs = append(attrs, "principal", p.Key())
			}
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			}
			log.Log(r.Context(), level, "request", attrs...)
		}()
		next.ServeHTTP(sw, r)
	})
}

// limiter is a per-client token bucket (L3-002 §1: 100/s, burst 200).
type limiter struct {
	mu      sync.Mutex
	rps     rate.Limit
	burst   int
	clients map[string]*clientLimit
}

type clientLimit struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{rps: rate.Limit(rps), burst: burst, clients: map[string]*clientLimit{}}
}

func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clients[key]
	if c == nil {
		if len(l.clients) > 10000 {
			for k, v := range l.clients {
				if now.Sub(v.seen) > time.Minute {
					delete(l.clients, k)
				}
			}
		}
		c = &clientLimit{l: rate.NewLimiter(l.rps, l.burst)}
		l.clients[key] = c
	}
	c.seen = now
	res := c.l.ReserveN(now, 1)
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return false, d
	}
	return true, 0
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := s.limiter.allow(clientIP(r), time.Now()); !ok {
			secs := int(wait.Seconds()) + 1
			s.writeError(w, r, &api.Error{Code: api.CodeRateLimited, Message: "Too many requests. Slow down and retry.", RetryAfterSeconds: secs})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// access describes who may call a route (ADR-036). Routes on one resource ({id}) name the role they need
// in that resource's project; the others are checked by the service.
type access int

const (
	public   access = iota
	signedIn        // any principal; the service checks roles (projects, people, machine, lists, creates)
	account         // a session, or a token of editor or above: your own tokens
	viewer          // the {id} resource's project: viewer
	editor          // the {id} resource's project: editor (changes, actions, secret values, message bodies)
)

// route registers pattern with its access rule, CSRF protection and route reporting.
func (s *Server) route(mux *http.ServeMux, pattern string, acc access, h func(http.ResponseWriter, *http.Request)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if holder, ok := r.Context().Value(keyRoute).(*routeHolder); ok {
			holder.pattern = pattern
		}
		if acc != public {
			p, err := s.authenticate(r)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			p.ClientIP = clientIP(r)
			if p.Method == "session" && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(api.HeaderCSRF) != "1" {
				s.writeError(w, r, api.NewError(api.CodeCSRFRequired, "This request needs the "+api.HeaderCSRF+" header."))
				return
			}
			switch acc {
			case account:
				if p.Method != "session" && !api.RoleAtLeast(p.TokenRole, api.RoleEditor) {
					s.writeError(w, r, api.NewError(api.CodeForbidden, "A viewer token can't manage tokens. Use an editor token or the console."))
					return
				}
			case viewer, editor:
				need := api.RoleViewer
				if acc == editor {
					need = api.RoleEditor
				}
				if err := s.resources.Authorize(r.Context(), p, r.PathValue("id"), need); err != nil {
					s.writeError(w, r, err)
					return
				}
			}
			ctx := context.WithValue(r.Context(), keyPrincipal, p)
			ctx = observability.WithLogger(ctx, observability.Logger(ctx, s.log).With("principal", p.Key()))
			r = r.WithContext(ctx)
		}
		h(w, r)
	})
}

func (s *Server) authenticate(r *http.Request) (service.Principal, error) {
	var bearer string
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, value, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return service.Principal{}, api.NewError(api.CodeUnauthenticated, "Use Authorization: Bearer <token>.")
		}
		bearer = strings.TrimSpace(value)
	}
	var cookie string
	if c, err := r.Cookie(api.SessionCookie); err == nil {
		cookie = c.Value
	}
	return s.auth.Authenticate(r.Context(), cookie, bearer)
}
