// Package service holds the control plane's use cases. Handlers call it; it validates, enforces the
// rules in L2-004 and L3-002, writes through the store in transactions, and hands work to the
// reconciler. It never changes runtime state itself (L3-001 §2).
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/auth"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Settings keys.
const (
	settingMachineName   = "machine_name"
	settingSetupCodeHash = "setup_code_hash"
)

// Session lifetime (ADR-007).
const (
	SessionTTL         = 7 * 24 * time.Hour
	tokenTouchInterval = time.Minute
	// AuditTTL is how long audit entries are kept (IAM-09).
	AuditTTL = 90 * 24 * time.Hour
)

// Principal is who is calling, with the roles they hold now (ADR-036).
type Principal struct {
	UserID      string
	Email       string
	Method      string // "session" or "token"
	TokenID     string
	SessionHash string
	ClientIP    string
	// MachineRole applies to every project; ProjectRoles to one each. "" means none.
	MachineRole  string
	ProjectRoles map[string]string
	// TokenRole and TokenProjects narrow a token's user (nil projects: every project).
	TokenRole     string
	TokenProjects []string
	// System marks the control plane acting on its own (re-checks after a settings change).
	System bool
}

// Key identifies the principal for idempotency records.
func (p Principal) Key() string {
	if p.TokenID != "" {
		return "tok:" + p.TokenID
	}
	return "usr:" + p.UserID
}

// Actor names the principal in events and the audit list (IAM-08).
func (p Principal) Actor() string {
	switch {
	case p.System:
		return "EnclaveStack"
	case p.Email == "":
		return p.Key()
	case p.TokenID != "":
		return p.Email + " (token " + p.TokenID + ")"
	}
	return p.Email
}

// RoleIn is the principal's effective role in a project: the stronger of the machine and project roles,
// narrowed by the token's role and projects ("" for none).
func (p Principal) RoleIn(project string) string {
	if p.System {
		return api.RoleAdmin
	}
	role := p.MachineRole
	if pr := p.ProjectRoles[project]; api.RoleRank(pr) > api.RoleRank(role) {
		role = pr
	}
	if p.TokenID != "" {
		if p.TokenProjects != nil && !slices.Contains(p.TokenProjects, project) {
			return ""
		}
		if api.RoleRank(p.TokenRole) < api.RoleRank(role) {
			role = p.TokenRole
		}
	}
	return role
}

// MachineRoleNow is the principal's effective machine role: a token limited to projects has none.
func (p Principal) MachineRoleNow() string {
	if p.System {
		return api.RoleAdmin
	}
	if p.TokenID != "" {
		if p.TokenProjects != nil {
			return ""
		}
		if api.RoleRank(p.TokenRole) < api.RoleRank(p.MachineRole) {
			return p.TokenRole
		}
	}
	return p.MachineRole
}

// IsMachineAdmin reports whether the principal may manage projects, people and the machine.
func (p Principal) IsMachineAdmin() bool { return p.MachineRoleNow() == api.RoleAdmin }

// Require checks the principal holds need in project. A project it can't see answers not found, so names
// don't leak (PRJ-08); a role too weak answers forbidden with the role needed (IAM-10).
func (p Principal) Require(project, need string) error {
	have := p.RoleIn(project)
	if have == "" {
		return notFound()
	}
	if !api.RoleAtLeast(have, need) {
		return forbidden(need, project)
	}
	return nil
}

// VisibleProjects lists the projects the principal can see among all; nil means all of them.
func (p Principal) VisibleProjects(all []string) []string {
	if p.MachineRoleNow() != "" && p.TokenProjects == nil {
		return nil
	}
	out := []string{}
	for _, name := range all {
		if p.RoleIn(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

func forbidden(need, project string) error {
	article := "an"
	if need == api.RoleViewer {
		article = "a"
	}
	return api.NewError(api.CodeForbidden, fmt.Sprintf("This needs %s %s role in %s. Ask an admin of %s.", article, need, project, project))
}

// Auth implements setup, sign-in, sessions, people and API tokens.
type Auth struct {
	Store    *store.Store
	Throttle *auth.Throttle
	Now      func() time.Time
	Log      *slog.Logger
	// ConsoleURL builds invite links: the console's base URL, without a trailing slash.
	ConsoleURL func(ctx context.Context) string
	// SendMail sends EnclaveStack's own email through an email service of a project (invite links, IAM-04);
	// nil on servers that can't send.
	SendMail func(ctx context.Context, project, service string, m api.SendEmail) error
}

// SetupRequired reports whether no admin exists yet.
func (a *Auth) SetupRequired(ctx context.Context) (bool, error) {
	n, err := a.Store.CountUsers(ctx)
	return n == 0, err
}

// PrepareSetupCode issues a fresh one-time setup code while no admin exists; it returns "" once set up.
// Each start replaces the previous code (L4-001 §5).
func (a *Auth) PrepareSetupCode(ctx context.Context) (string, error) {
	required, err := a.SetupRequired(ctx)
	if err != nil || !required {
		return "", err
	}
	code := auth.NewSetupCode()
	if err := a.Store.SetSetting(ctx, settingSetupCodeHash, auth.Hash(code)); err != nil {
		return "", err
	}
	return code, nil
}

// Setup creates the admin account and signs them in. It returns the session and its cookie value.
func (a *Auth) Setup(ctx context.Context, req api.SetupRequest) (api.Session, string, error) {
	if details := api.ValidateSetup(req); len(details) > 0 {
		return api.Session{}, "", api.Validation(details)
	}
	hash, err := auth.HashPassword(req.Password)
	auth.ReleaseMemory()
	if err != nil {
		return api.Session{}, "", err
	}
	now := a.Now()
	user := store.User{ID: domain.NewID("usr"), Email: strings.TrimSpace(req.Email), PasswordHash: hash, MachineRole: api.RoleAdmin, CreatedAt: now}
	sid := auth.NewSessionID()
	err = a.Store.WithTx(ctx, func(q *store.Queries) error {
		n, err := q.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return api.NewError(api.CodeSetupComplete, "This machine is already set up. Sign in instead.")
		}
		want, err := q.GetSetting(ctx, settingSetupCodeHash)
		if err != nil || !auth.EqualHash(want, auth.Hash(auth.NormalizeSetupCode(req.SetupCode))) {
			return api.NewError(api.CodeInvalidSetupCode, "That setup code isn't right. Use the code printed where enclave server is running.")
		}
		if err := q.CreateUser(ctx, user); err != nil {
			return err
		}
		if err := q.SetSetting(ctx, settingMachineName, strings.TrimSpace(req.MachineName)); err != nil {
			return err
		}
		if err := q.DeleteSetting(ctx, settingSetupCodeHash); err != nil {
			return err
		}
		if err := q.TouchSignIn(ctx, user.ID, now); err != nil {
			return err
		}
		return q.CreateSession(ctx, store.Session{IDHash: auth.Hash(sid), UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL), LastSeenAt: now})
	})
	if err != nil {
		return api.Session{}, "", err
	}
	a.Log.Info("setup completed", "user_id", user.ID)
	a.audit(ctx, Principal{UserID: user.ID, Email: user.Email}, "setup", user.Email, "Machine set up; this person is its admin.")
	admin := api.RoleAdmin
	return api.Session{User: api.User{ID: user.ID, Email: user.Email}, MachineName: strings.TrimSpace(req.MachineName), AuthMethod: "session",
		Scope: api.ScopeFull, MachineRole: &admin, ProjectRoles: map[string]string{}}, sid, nil
}

// SignIn checks credentials and starts a session. clientIP feeds the throttle.
func (a *Auth) SignIn(ctx context.Context, req api.SignInRequest, clientIP string) (api.Session, string, error) {
	key := strings.ToLower(strings.TrimSpace(req.Email)) + "|" + clientIP
	if blocked, wait := a.Throttle.Blocked(key); blocked {
		secs := int(wait.Seconds()) + 1
		return api.Session{}, "", &api.Error{Code: api.CodeTooManyAttempts, RetryAfterSeconds: secs,
			Message: fmt.Sprintf("Too many attempts. Try again in %s.", humanDuration(wait))}
	}
	if required, err := a.SetupRequired(ctx); err != nil {
		return api.Session{}, "", err
	} else if required {
		return api.Session{}, "", api.NewError(api.CodeSetupRequired, "Finish first-run setup before signing in.")
	}
	defer auth.ReleaseMemory()
	user, err := a.Store.UserByEmail(ctx, strings.TrimSpace(req.Email))
	if errors.Is(err, store.ErrNotFound) || (err == nil && user.PasswordHash == "") { // an open invite can't sign in
		auth.VerifyDummy(req.Password)
		a.Throttle.Fail(key)
		a.audit(ctx, Principal{ClientIP: clientIP}, "sign_in_failed", strings.TrimSpace(req.Email), "Unknown email or invite not accepted yet.")
		return api.Session{}, "", invalidCredentials()
	}
	if err != nil {
		return api.Session{}, "", err
	}
	if err := auth.VerifyPassword(req.Password, user.PasswordHash); err != nil {
		a.Throttle.Fail(key)
		a.Log.Warn("sign-in failed", "user_id", user.ID, "client_ip", clientIP)
		a.audit(ctx, Principal{ClientIP: clientIP}, "sign_in_failed", user.Email, "Wrong password.")
		return api.Session{}, "", invalidCredentials()
	}
	a.Throttle.Succeed(key)
	return a.startSession(ctx, user, clientIP)
}

func (a *Auth) startSession(ctx context.Context, user store.User, clientIP string) (api.Session, string, error) {
	now := a.Now()
	sid := auth.NewSessionID()
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.TouchSignIn(ctx, user.ID, now); err != nil {
			return err
		}
		return q.CreateSession(ctx, store.Session{IDHash: auth.Hash(sid), UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL), LastSeenAt: now})
	})
	if err != nil {
		return api.Session{}, "", err
	}
	p, err := a.principalFor(ctx, user, "session")
	if err != nil {
		return api.Session{}, "", err
	}
	p.ClientIP = clientIP
	a.audit(ctx, p, "sign_in", user.Email, "")
	sess, err := a.Session(ctx, p)
	return sess, sid, err
}

func invalidCredentials() error {
	return api.NewError(api.CodeInvalidCredentials, "That email and password don't match.")
}

func humanDuration(d time.Duration) string {
	if m := int(d.Round(time.Minute).Minutes()); m >= 1 {
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds())+1)
}

// SignOut ends a cookie session.
func (a *Auth) SignOut(ctx context.Context, p Principal) error {
	if p.SessionHash == "" {
		return nil
	}
	return a.Store.DeleteSession(ctx, p.SessionHash)
}

// principalFor loads a user's current roles.
func (a *Auth) principalFor(ctx context.Context, user store.User, method string) (Principal, error) {
	roles, err := a.Store.ProjectRoles(ctx, user.ID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{UserID: user.ID, Email: user.Email, Method: method, MachineRole: user.MachineRole, ProjectRoles: roles}, nil
}

// Authenticate resolves a session cookie or bearer token to a principal with its current roles.
func (a *Auth) Authenticate(ctx context.Context, cookie, bearer string) (Principal, error) {
	now := a.Now()
	unauth := api.NewError(api.CodeUnauthenticated, "Sign in to continue.")
	if bearer != "" {
		if !strings.HasPrefix(bearer, api.TokenPrefix) {
			return Principal{}, unauth
		}
		t, err := a.Store.TokenByHash(ctx, auth.Hash(bearer))
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, unauth
		}
		if err != nil {
			return Principal{}, err
		}
		if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
			return Principal{}, api.NewError(api.CodeUnauthenticated, "This token was revoked or has expired. Create a new one.")
		}
		user, err := a.Store.UserByID(ctx, t.UserID)
		if err != nil {
			return Principal{}, unauth
		}
		if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > tokenTouchInterval {
			if err := a.Store.TouchToken(ctx, t.ID, now); err != nil {
				a.Log.Warn("record token use failed", "token_id", t.ID, "error", err)
			}
		}
		p, err := a.principalFor(ctx, user, "token")
		if err != nil {
			return Principal{}, err
		}
		p.TokenID, p.TokenRole, p.TokenProjects = t.ID, t.Role, t.Projects
		return p, nil
	}
	if cookie == "" {
		return Principal{}, unauth
	}
	hash := auth.Hash(cookie)
	s, err := a.Store.SessionByHash(ctx, hash, now)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, unauth
	}
	if err != nil {
		return Principal{}, err
	}
	user, err := a.Store.UserByID(ctx, s.UserID)
	if err != nil {
		return Principal{}, unauth
	}
	if now.Sub(s.LastSeenAt) > time.Minute {
		if err := a.Store.TouchSession(ctx, hash, now, now.Add(SessionTTL)); err != nil {
			a.Log.Warn("extend session failed", "error", err)
		}
	}
	p, err := a.principalFor(ctx, user, "session")
	if err != nil {
		return Principal{}, err
	}
	p.SessionHash = hash
	return p, nil
}

// Session describes the principal.
func (a *Auth) Session(ctx context.Context, p Principal) (api.Session, error) {
	name, err := a.Store.GetSetting(ctx, settingMachineName)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return api.Session{}, err
	}
	sess := api.Session{User: api.User{ID: p.UserID, Email: p.Email}, MachineName: name, AuthMethod: p.Method, Scope: api.ScopeFull,
		ProjectRoles: p.ProjectRoles}
	if sess.ProjectRoles == nil {
		sess.ProjectRoles = map[string]string{}
	}
	if p.MachineRole != "" {
		role := p.MachineRole
		sess.MachineRole = &role
	}
	if p.TokenID != "" {
		sess.Scope = api.ScopeForRole(p.TokenRole)
		sess.Token = &api.TokenLimits{Role: p.TokenRole, Projects: p.TokenProjects}
	}
	return sess, nil
}

// MachineName returns the configured machine name.
func (a *Auth) MachineName(ctx context.Context) string {
	name, _ := a.Store.GetSetting(ctx, settingMachineName)
	return name
}

// ListTokens returns the principal's tokens, or every user's when all is set (machine admins, TOK-04).
func (a *Auth) ListTokens(ctx context.Context, p Principal, all bool) ([]api.Token, error) {
	var rows []store.Token
	var err error
	if all {
		if !p.IsMachineAdmin() {
			return nil, api.NewError(api.CodeForbidden, "Only machine admins see everyone's tokens.")
		}
		rows, err = a.Store.ListAllTokens(ctx)
	} else {
		rows, err = a.Store.ListTokens(ctx, p.UserID)
	}
	if err != nil {
		return nil, err
	}
	emails := map[string]string{p.UserID: p.Email}
	out := make([]api.Token, 0, len(rows))
	for _, t := range rows {
		if _, ok := emails[t.UserID]; !ok {
			if u, err := a.Store.UserByID(ctx, t.UserID); err == nil {
				emails[t.UserID] = u.Email
			}
		}
		out = append(out, toAPIToken(t, emails[t.UserID]))
	}
	return out, nil
}

// CreateToken issues a token; the secret is returned once and only its hash is kept. A token can't hold
// a role its user doesn't hold anywhere, nor name projects the user can't see (TOK-02).
func (a *Auth) CreateToken(ctx context.Context, p Principal, req api.CreateTokenRequest) (api.CreatedToken, error) {
	if details := api.ValidateCreateToken(req); len(details) > 0 {
		return api.CreatedToken{}, api.Validation(details)
	}
	role := req.TokenRole()
	best := p.MachineRoleNow()
	for proj := range p.ProjectRoles {
		if r := p.RoleIn(proj); api.RoleRank(r) > api.RoleRank(best) {
			best = r
		}
	}
	if role == "" { // a token does what its owner can, unless the request narrows it
		role = best
	}
	if role == "" {
		return api.CreatedToken{}, api.NewError(api.CodeForbidden, "You have no role yet, so a token couldn't do anything. Ask an admin for one.")
	}
	if api.RoleRank(role) > api.RoleRank(best) {
		return api.CreatedToken{}, api.Validation([]api.FieldError{{Field: "role", Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("You can't give a token the %s role: your strongest role is %s.", role, orNone(best))}})
	}
	var projects []string
	if req.Projects != nil {
		for i, proj := range req.Projects {
			if _, err := a.Store.GetProject(ctx, proj); err != nil || p.RoleIn(proj) == "" {
				return api.CreatedToken{}, api.Validation([]api.FieldError{{Field: fmt.Sprintf("projects[%d]", i), Code: api.FieldNotFound, Message: "There's no project named " + proj + "."}})
			}
			if !slices.Contains(projects, proj) {
				projects = append(projects, proj)
			}
		}
	} else if p.TokenProjects != nil {
		projects = slices.Clone(p.TokenProjects) // a token never makes a wider one
	}
	now := a.Now()
	secret, prefix := auth.NewToken()
	t := store.Token{ID: domain.NewID("tok"), UserID: p.UserID, Name: strings.TrimSpace(req.Name), Prefix: prefix,
		SecretHash: auth.Hash(secret), Role: role, Projects: projects, CreatedAt: now}
	if req.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		t.ExpiresAt = &exp
	}
	if err := a.Store.CreateToken(ctx, t); err != nil {
		return api.CreatedToken{}, err
	}
	a.Log.Info("token created", "token_id", t.ID, "role", t.Role, "by", p.Key())
	scope := "all projects"
	if projects != nil {
		scope = strings.Join(projects, ", ")
	}
	a.audit(ctx, p, "token_created", t.ID, fmt.Sprintf("%s: %s on %s.", t.Name, t.Role, scope))
	return api.CreatedToken{Token: toAPIToken(t, p.Email), Secret: secret}, nil
}

func orNone(role string) string {
	if role == "" {
		return "none"
	}
	return role
}

// RevokeToken revokes one of the principal's tokens; machine admins may revoke anyone's (TOK-04).
func (a *Auth) RevokeToken(ctx context.Context, p Principal, id string) error {
	owner := p.UserID
	if p.IsMachineAdmin() {
		owner = ""
	}
	err := a.Store.RevokeToken(ctx, owner, id, a.Now())
	if errors.Is(err, store.ErrNotFound) {
		return api.NewError(api.CodeNotFound, "That token doesn't exist or was already revoked.")
	}
	if err == nil {
		a.Log.Info("token revoked", "token_id", id, "by", p.Key())
		a.audit(ctx, p, "token_revoked", id, "")
	}
	return err
}

func toAPIToken(t store.Token, email string) api.Token {
	return api.Token{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scope: api.ScopeForRole(t.Role), Role: t.Role, Projects: t.Projects,
		UserEmail: email, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt}
}

// ChangePassword sets a new password after checking the current one, and ends the user's other
// sessions (IAM-07).
func (a *Auth) ChangePassword(ctx context.Context, p Principal, req api.ChangePassword) error {
	if p.Method != "session" {
		return api.NewError(api.CodeForbidden, "Change your password from a signed-in session, not with a token.")
	}
	if errs := api.ValidatePassword(req.NewPassword); len(errs) > 0 {
		errs[0].Field = "newPassword"
		return api.Validation(errs)
	}
	defer auth.ReleaseMemory()
	user, err := a.Store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	key := strings.ToLower(user.Email) + "|" + p.ClientIP
	if blocked, wait := a.Throttle.Blocked(key); blocked {
		return &api.Error{Code: api.CodeTooManyAttempts, RetryAfterSeconds: int(wait.Seconds()) + 1, Message: fmt.Sprintf("Too many attempts. Try again in %s.", humanDuration(wait))}
	}
	if auth.VerifyPassword(req.CurrentPassword, user.PasswordHash) != nil {
		a.Throttle.Fail(key)
		return api.Validation([]api.FieldError{{Field: "currentPassword", Code: api.FieldNotAllowed, Message: "That isn't your current password."}})
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	err = a.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SetPassword(ctx, user.ID, hash); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, user.ID, p.SessionHash)
	})
	if err == nil {
		a.audit(ctx, p, "password_changed", user.Email, "Other sessions ended.")
	}
	return err
}

// audit appends an audit entry; a failure is logged, never returned (IAM-09).
func (a *Auth) audit(ctx context.Context, p Principal, action, target, detail string) {
	writeAudit(ctx, a.Store, a.Now(), a.Log, p, action, target, detail)
}

func writeAudit(ctx context.Context, st *store.Store, now time.Time, log *slog.Logger, p Principal, action, target, detail string) {
	actor := p.Actor()
	if p.UserID == "" && !p.System {
		actor = "anonymous"
	}
	if err := st.AddAudit(context.WithoutCancel(ctx), store.AuditEntry{At: now, Actor: actor, Action: action, Target: target, Detail: detail, ClientIP: p.ClientIP}); err != nil {
		log.Warn("write audit entry failed", "action", action, "error", err)
	}
}

// Sweep removes expired sessions, idempotency records and old audit entries (L3-005 §4).
func (a *Auth) Sweep(ctx context.Context) {
	now := a.Now()
	if n, err := a.Store.DeleteExpiredSessions(ctx, now); err != nil {
		a.Log.Warn("sweep sessions failed", "error", err)
	} else if n > 0 {
		a.Log.Info("expired sessions removed", "count", n)
	}
	if _, err := a.Store.SweepIdempotency(ctx, now.Add(-IdempotencyTTL)); err != nil {
		a.Log.Warn("sweep idempotency failed", "error", err)
	}
	if _, err := a.Store.SweepAudit(ctx, now.Add(-AuditTTL)); err != nil {
		a.Log.Warn("sweep audit failed", "error", err)
	}
}
