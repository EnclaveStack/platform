package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/auth"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The gate in front of public apps (L2-014 GATE-01 – GATE-08, ADR-053). It signs people in with their
// EnclaveStack account for one hostname, and answers the edge's check. Its sessions are its own: a gate
// cookie never opens the console, and a console session never opens a gate.

// Gate policy (L3-007 §14).
const (
	GateSessionTTL = 12 * time.Hour
	// GateRecheck is how often a session's access is checked again, so removal ends it within a minute.
	GateRecheck = 30 * time.Second
	// GateCookie is the gate's session cookie; __Host- keeps it to the exact hostname, over HTTPS, at "/".
	GateCookie = "__Host-enclave_gate"
)

// Gate check results.
const (
	GateAllowed = "allowed"
	GateSignin  = "signin"
	GateDenied  = "denied"
)

// ErrGateNoAccess is a correct sign-in by someone without a role in the project.
var ErrGateNoAccess = errors.New("this person can't open this environment")

// Gate answers the edge proxy for gated hostnames.
type Gate struct {
	Store    *store.Store
	Throttle *auth.Throttle
	Now      func() time.Time
	Log      *slog.Logger
	Metrics  *observability.Metrics
}

// GateTarget is what a gated route protects: a public domain or an app link, in a project.
type GateTarget struct {
	ID      string
	Project string
	Name    string
	Kind    api.Kind
}

// Target returns the public domain or app link a route protects; anything else is ErrNotFound.
func (g *Gate) Target(ctx context.Context, id string) (GateTarget, error) {
	r, err := g.Store.GetResource(ctx, id)
	if err != nil {
		return GateTarget{}, err
	}
	switch r.Kind {
	case api.KindDomain:
		var s api.DomainSpec
		if json.Unmarshal(r.Spec, &s) != nil || !s.IsPublic() {
			return GateTarget{}, store.ErrNotFound
		}
	case api.KindTunnel:
		var s api.TunnelSpec
		if json.Unmarshal(r.Spec, &s) != nil || !api.IsAppLink(s.Provider) {
			return GateTarget{}, store.ErrNotFound
		}
	default:
		return GateTarget{}, store.ErrNotFound
	}
	return GateTarget{ID: r.ID, Project: r.ProjectName(), Name: r.Name, Kind: r.Kind}, nil
}

// hasAccess reports whether a user may open a project's public apps: any role in it, or a machine role
// (GATE-02).
func (g *Gate) hasAccess(ctx context.Context, userID, project string) (store.User, bool, error) {
	u, err := g.Store.UserByID(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return u, false, nil
	}
	if err != nil {
		return u, false, err
	}
	if u.MachineRole != "" {
		return u, true, nil
	}
	roles, err := g.Store.ProjectRoles(ctx, userID)
	if err != nil {
		return u, false, err
	}
	return u, roles[project] != "", nil
}

// Check answers whether the holder of a gate cookie may open host for target: allowed (with the user's
// email), signin (no valid session for this host) or denied (signed in without access).
func (g *Gate) Check(ctx context.Context, t GateTarget, host, token string) (string, string, error) {
	if token == "" {
		return "", GateSignin, nil
	}
	hash := auth.Hash(token)
	s, err := g.Store.GetGateSession(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return "", GateSignin, nil
	}
	if err != nil {
		return "", "", err
	}
	now := g.Now()
	if !s.ExpiresAt.After(now) || !strings.EqualFold(s.Hostname, host) {
		return "", GateSignin, nil
	}
	if now.Sub(s.CheckedAt) < GateRecheck {
		u, err := g.Store.UserByID(ctx, s.UserID)
		if err != nil {
			return "", GateSignin, nil
		}
		return u.Email, GateAllowed, nil
	}
	u, ok, err := g.hasAccess(ctx, s.UserID, t.Project)
	if err != nil {
		return "", "", err
	}
	if !ok {
		// Removed from the project: the session ends (GATE-03).
		if err := g.Store.DeleteGateSession(ctx, hash); err != nil {
			return "", "", err
		}
		return "", GateSignin, nil
	}
	if err := g.Store.TouchGateSession(ctx, hash, now); err != nil {
		return "", "", err
	}
	return u.Email, GateAllowed, nil
}

// SignIn checks an email and password for host and starts a gate session. It is throttled per client
// address and recorded in the audit log (GATE-08).
func (g *Gate) SignIn(ctx context.Context, t GateTarget, host, email, password, clientIP string) (string, error) {
	key := "gate|" + clientIP
	if blocked, wait := g.Throttle.Blocked(key); blocked {
		g.count("throttled")
		return "", &api.Error{Code: api.CodeTooManyAttempts, RetryAfterSeconds: int(wait.Seconds()) + 1,
			Message: fmt.Sprintf("Too many attempts. Try again in %s.", humanDuration(wait))}
	}
	defer auth.ReleaseMemory()
	email = strings.TrimSpace(email)
	u, err := g.Store.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.PasswordHash == "") {
		auth.VerifyDummy(password)
		g.fail(ctx, key, clientIP, email, host, "Unknown email.")
		return "", invalidCredentials()
	}
	if err != nil {
		return "", err
	}
	if err := auth.VerifyPassword(password, u.PasswordHash); err != nil {
		g.fail(ctx, key, clientIP, u.Email, host, "Wrong password.")
		return "", invalidCredentials()
	}
	g.Throttle.Succeed(key)
	_, ok, err := g.hasAccess(ctx, u.ID, t.Project)
	if err != nil {
		return "", err
	}
	if !ok {
		g.count("no_access")
		g.audit(ctx, Principal{UserID: u.ID, Email: u.Email, ClientIP: clientIP}, "gate.signin_failed", host, "No role in the project "+t.Project+".")
		return "", ErrGateNoAccess
	}
	token, err := newGateToken()
	if err != nil {
		return "", err
	}
	now := g.Now()
	if err := g.Store.InsertGateSession(ctx, store.GateSession{TokenHash: auth.Hash(token), UserID: u.ID, Hostname: strings.ToLower(host),
		CreatedAt: now, ExpiresAt: now.Add(GateSessionTTL), CheckedAt: now}); err != nil {
		return "", err
	}
	g.count("ok")
	g.audit(ctx, Principal{UserID: u.ID, Email: u.Email, ClientIP: clientIP}, "gate.signin", host, "")
	g.Log.Info("gate sign-in", "user_id", u.ID, "hostname", host)
	return token, nil
}

// SignOut ends the session of a gate cookie.
func (g *Gate) SignOut(ctx context.Context, token, host, clientIP string) error {
	if token == "" {
		return nil
	}
	hash := auth.Hash(token)
	if s, err := g.Store.GetGateSession(ctx, hash); err == nil {
		if u, err := g.Store.UserByID(ctx, s.UserID); err == nil {
			g.audit(ctx, Principal{UserID: u.ID, Email: u.Email, ClientIP: clientIP}, "gate.signout", host, "")
		}
	}
	return g.Store.DeleteGateSession(ctx, hash)
}

// SessionEmail is who holds a gate cookie for host, "" for nobody.
func (g *Gate) SessionEmail(ctx context.Context, token, host string) string {
	if token == "" {
		return ""
	}
	s, err := g.Store.GetGateSession(ctx, auth.Hash(token))
	if err != nil || !s.ExpiresAt.After(g.Now()) || !strings.EqualFold(s.Hostname, host) {
		return ""
	}
	u, err := g.Store.UserByID(ctx, s.UserID)
	if err != nil {
		return ""
	}
	return u.Email
}

// Sweep removes expired gate sessions (L3-007 §14).
func (g *Gate) Sweep(ctx context.Context) {
	if n, err := g.Store.DeleteExpiredGateSessions(ctx, g.Now()); err != nil {
		g.Log.Warn("sweep gate sessions failed", "error", err)
	} else if n > 0 {
		g.Log.Info("expired gate sessions removed", "count", n)
	}
}

func (g *Gate) fail(ctx context.Context, key, clientIP, email, host, why string) {
	g.Throttle.Fail(key)
	g.count("wrong")
	g.Log.Warn("gate sign-in failed", "hostname", host, "client_ip", clientIP)
	g.audit(ctx, Principal{ClientIP: clientIP}, "gate.signin_failed", host, email+": "+why)
}

func (g *Gate) count(result string) {
	if g.Metrics != nil {
		g.Metrics.GateSignins.WithLabelValues(result).Inc()
	}
}

func (g *Gate) audit(ctx context.Context, p Principal, action, target, detail string) {
	writeAudit(ctx, g.Store, g.Now(), g.Log, p, action, target, detail)
}

func newGateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
