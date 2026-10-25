package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/auth"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Projects (ADR-035) and people (ADR-036).

func projectNotFound(name string) error {
	return api.NewError(api.CodeNotFound, "There's no project named "+name+".")
}

func (a *Auth) toAPIProject(p store.Project, counts map[string]int) api.Project {
	return api.Project{Name: p.Name, Description: p.Description, CreatedAt: p.CreatedAt, Resources: counts[p.Name]}
}

// ListProjects returns the projects the principal can see.
func (a *Auth) ListProjects(ctx context.Context, p Principal) (api.ProjectList, error) {
	rows, err := a.Store.ListProjects(ctx)
	if err != nil {
		return api.ProjectList{}, err
	}
	counts, err := a.Store.ProjectResourceCounts(ctx)
	if err != nil {
		return api.ProjectList{}, err
	}
	out := api.ProjectList{Items: []api.Project{}}
	for _, row := range rows {
		if p.RoleIn(row.Name) != "" {
			out.Items = append(out.Items, a.toAPIProject(row, counts))
		}
	}
	return out, nil
}

// ProjectNames lists every project's name.
func (a *Auth) ProjectNames(ctx context.Context) ([]string, error) {
	rows, err := a.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names, nil
}

// GetProject returns a project the principal can see.
func (a *Auth) GetProject(ctx context.Context, p Principal, name string) (api.Project, error) {
	if p.RoleIn(name) == "" {
		return api.Project{}, projectNotFound(name)
	}
	row, err := a.Store.GetProject(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.Project{}, projectNotFound(name)
	}
	if err != nil {
		return api.Project{}, err
	}
	counts, err := a.Store.ProjectResourceCounts(ctx)
	return a.toAPIProject(row, counts), err
}

// CreateProject makes a project (machine admins, PRJ-01).
func (a *Auth) CreateProject(ctx context.Context, p Principal, req api.CreateProject) (api.Project, error) {
	if !p.IsMachineAdmin() {
		return api.Project{}, api.NewError(api.CodeForbidden, "Only machine admins create projects.")
	}
	req.Name, req.Description = strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if errs := api.ValidateProject(req); len(errs) > 0 {
		return api.Project{}, api.Validation(errs)
	}
	row := store.Project{Name: req.Name, Description: req.Description, CreatedAt: a.Now()}
	err := a.Store.CreateProject(ctx, row)
	if errors.Is(err, store.ErrConflict) {
		return api.Project{}, &api.Error{Code: api.CodeNameTaken, Message: "A project named " + req.Name + " already exists.",
			Details: []api.FieldError{{Field: "name", Code: api.FieldNotAllowed, Message: "A project named " + req.Name + " already exists."}}}
	}
	if err != nil {
		return api.Project{}, err
	}
	a.audit(ctx, p, "project_created", req.Name, req.Description)
	return a.toAPIProject(row, nil), nil
}

// UpdateProject changes a project's description (its admins).
func (a *Auth) UpdateProject(ctx context.Context, p Principal, name string, req api.UpdateProject) (api.Project, error) {
	if err := p.Require(name, api.RoleAdmin); err != nil {
		if ae := (*api.Error)(nil); errors.As(err, &ae) && ae.Code == api.CodeNotFound {
			return api.Project{}, projectNotFound(name)
		}
		return api.Project{}, err
	}
	req.Description = strings.TrimSpace(req.Description)
	if errs := api.ValidateUpdateProject(req); len(errs) > 0 {
		return api.Project{}, api.Validation(errs)
	}
	err := a.Store.UpdateProjectDescription(ctx, name, req.Description)
	if errors.Is(err, store.ErrNotFound) {
		return api.Project{}, projectNotFound(name)
	}
	if err != nil {
		return api.Project{}, err
	}
	a.audit(ctx, p, "project_updated", name, req.Description)
	return a.GetProject(ctx, p, name)
}

// DeleteProject removes an empty project (machine admins, PRJ-07).
func (a *Auth) DeleteProject(ctx context.Context, p Principal, name string) error {
	if !p.IsMachineAdmin() {
		if p.RoleIn(name) == "" {
			return projectNotFound(name)
		}
		return api.NewError(api.CodeForbidden, "Only machine admins delete projects.")
	}
	if name == api.DefaultProject {
		return api.NewError(api.CodeActionNotSupported, "The default project can't be deleted.")
	}
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		left, err := q.ResourcesInProject(ctx, name)
		if err != nil {
			return err
		}
		if len(left) > 0 {
			e := &api.Error{Code: api.CodeProjectNotEmpty, Message: fmt.Sprintf("%s still has %d resource(s). Delete them first.", name, len(left))}
			for _, r := range left {
				e.Details = append(e.Details, api.FieldError{Field: r.Ref(), Code: api.FieldNotAllowed, Message: fmt.Sprintf("The %s %s is still in %s.", r.Kind, r.Name, name)})
			}
			return e
		}
		return q.DeleteProject(ctx, name)
	})
	if errors.Is(err, store.ErrNotFound) {
		return projectNotFound(name)
	}
	if err == nil {
		a.audit(ctx, p, "project_deleted", name, "")
	}
	return err
}

// adminProjects lists the projects the principal administers; nil means every project (machine admin).
func (a *Auth) adminProjects(p Principal) []string {
	if p.IsMachineAdmin() {
		return nil
	}
	out := []string{} // empty, not nil: nil means every project
	for proj := range p.ProjectRoles {
		if p.RoleIn(proj) == api.RoleAdmin {
			out = append(out, proj)
		}
	}
	slices.Sort(out)
	return out
}

func userStatus(u store.User) string {
	if u.PasswordHash == "" {
		return api.UserInvited
	}
	return api.UserActive
}

func toUserSummary(u store.User, roles map[string]string) api.UserSummary {
	s := api.UserSummary{ID: u.ID, Email: u.Email, ProjectRoles: roles, Status: userStatus(u), CreatedAt: u.CreatedAt, LastSignInAt: u.LastSignInAt}
	if s.ProjectRoles == nil {
		s.ProjectRoles = map[string]string{}
	}
	if u.MachineRole != "" {
		role := u.MachineRole
		s.MachineRole = &role
	}
	return s
}

// ListUsers returns everyone to machine admins, and to a project admin the people with a role in the
// projects they administer, showing only those projects' roles (IAM-05).
func (a *Auth) ListUsers(ctx context.Context, p Principal) (api.UserList, error) {
	scope := a.adminProjects(p)
	if scope != nil && len(scope) == 0 {
		return api.UserList{}, api.NewError(api.CodeForbidden, "Only admins see the people with access.")
	}
	users, err := a.Store.ListUsers(ctx)
	if err != nil {
		return api.UserList{}, err
	}
	all, err := a.Store.AllProjectRoles(ctx)
	if err != nil {
		return api.UserList{}, err
	}
	out := api.UserList{Items: []api.UserSummary{}}
	for _, u := range users {
		roles := all[u.ID]
		if scope != nil {
			shown := map[string]string{}
			for _, proj := range scope {
				if r, ok := roles[proj]; ok {
					shown[proj] = r
				}
			}
			if len(shown) == 0 {
				continue
			}
			roles = shown
			u.MachineRole = "" // a project admin doesn't manage machine roles
		}
		out.Items = append(out.Items, toUserSummary(u, roles))
	}
	return out, nil
}

// GetUser returns one person, as ListUsers shows them.
func (a *Auth) GetUser(ctx context.Context, p Principal, id string) (api.UserSummary, error) {
	list, err := a.ListUsers(ctx, p)
	if err != nil {
		return api.UserSummary{}, err
	}
	for _, u := range list.Items {
		if u.ID == id {
			return u, nil
		}
	}
	return api.UserSummary{}, userNotFound()
}

func userNotFound() error { return api.NewError(api.CodeNotFound, "That person doesn't exist.") }

// checkGrant refuses roles the principal can't hand out: machine roles need a machine admin, project
// roles an admin of that project; projects must exist.
func (a *Auth) checkGrant(ctx context.Context, p Principal, machineRoleChange bool, projects []string) error {
	if machineRoleChange && !p.IsMachineAdmin() {
		return api.NewError(api.CodeForbidden, "Only machine admins give or change machine roles.")
	}
	for _, proj := range projects {
		if _, err := a.Store.GetProject(ctx, proj); errors.Is(err, store.ErrNotFound) {
			return api.Validation([]api.FieldError{{Field: "projectRoles." + proj, Code: api.FieldNotFound, Message: "There's no project named " + proj + "."}})
		} else if err != nil {
			return err
		}
		if p.RoleIn(proj) != api.RoleAdmin {
			return forbidden(api.RoleAdmin, proj)
		}
	}
	return nil
}

// InviteUser creates a person with roles and an invite link; base is the console's URL (IAM-04).
func (a *Auth) InviteUser(ctx context.Context, p Principal, req api.InviteUser, base string) (api.Invitation, error) {
	req.Email = strings.TrimSpace(req.Email)
	if errs := api.ValidateInvite(req); len(errs) > 0 {
		return api.Invitation{}, api.Validation(errs)
	}
	if err := a.checkGrant(ctx, p, req.MachineRole != nil, sortedKeys(req.ProjectRoles)); err != nil {
		return api.Invitation{}, err
	}
	now := a.Now()
	user := store.User{ID: domain.NewID("usr"), Email: req.Email, CreatedAt: now}
	if req.MachineRole != nil {
		user.MachineRole = *req.MachineRole
	}
	code := auth.RandomSecret(32)
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.CreateUser(ctx, user); errors.Is(err, store.ErrConflict) {
			return &api.Error{Code: api.CodeEmailTaken, Message: req.Email + " already has access to this machine.",
				Details: []api.FieldError{{Field: "email", Code: api.FieldNotAllowed, Message: req.Email + " already has access to this machine."}}}
		} else if err != nil {
			return err
		}
		for proj, role := range req.ProjectRoles {
			if err := q.SetProjectRole(ctx, user.ID, proj, role); err != nil {
				return err
			}
		}
		return q.PutInvite(ctx, store.Invite{UserID: user.ID, CodeHash: auth.Hash(code), Kind: api.InviteKindInvite, ExpiresAt: now.Add(api.InviteTTL)})
	})
	if err != nil {
		return api.Invitation{}, err
	}
	a.audit(ctx, p, "user_invited", user.Email, describeRoles(user.MachineRole, req.ProjectRoles))
	a.Log.Info("user invited", "user_id", user.ID, "by", p.Key())
	inv := api.Invitation{User: toUserSummary(user, req.ProjectRoles), URL: inviteURL(base, code), ExpiresAt: now.Add(api.InviteTTL)}
	a.emailLink(ctx, p, &inv, api.InviteKindInvite)
	return inv, nil
}

// InviteEmailSetting reads how invite links are emailed; nil when they aren't (IAM-04).
func InviteEmailSetting(ctx context.Context, st *store.Store) (*api.InviteEmail, error) {
	raw, err := st.GetSetting(ctx, store.SettingInviteEmail)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ie api.InviteEmail
	if err := json.Unmarshal([]byte(raw), &ie); err != nil {
		return nil, fmt.Errorf("the invite email setting is unreadable: %w", err)
	}
	return &ie, nil
}

// inviteMailTimeout bounds sending a link inside the request that made it.
const inviteMailTimeout = 10 * time.Second

// emailLink also sends an invite or reset link by email when the machine is set up for it (IAM-04).
// It never fails the request: the link is in the answer either way, and emailError says why it didn't go.
func (a *Auth) emailLink(ctx context.Context, p Principal, inv *api.Invitation, kind string) {
	ie, err := InviteEmailSetting(ctx, a.Store)
	if err != nil || ie == nil || a.SendMail == nil {
		if err != nil {
			inv.EmailError = err.Error()
		}
		return
	}
	machine := a.MachineName(ctx)
	sender := p.Email
	if sender == "" {
		sender = "an admin"
	}
	until := inv.ExpiresAt.UTC().Format("Monday 2 January 2006, 15:04 UTC")
	subject := fmt.Sprintf("You're invited to %s", machine)
	text := fmt.Sprintf("%s invited you to the EnclaveStack on %s.\n\nOpen this link to choose your password and sign in:\n\n%s\n\nThe link works once, until %s. If you weren't expecting it, ignore this email.\n",
		sender, machine, inv.URL, until)
	if kind == api.InviteKindReset {
		subject = fmt.Sprintf("Reset your password on %s", machine)
		text = fmt.Sprintf("%s issued a link to set a new password for your account on %s.\n\n%s\n\nThe link works once, until %s. Your other sessions were signed out.\n",
			sender, machine, inv.URL, until)
	}
	sctx, cancel := context.WithTimeout(ctx, inviteMailTimeout)
	defer cancel()
	if err := a.SendMail(sctx, ie.Project, ie.Service, api.SendEmail{From: ie.From, To: []string{inv.User.Email}, Subject: subject, Text: text}); err != nil {
		inv.EmailError = "The link wasn't emailed: " + err.Error() + ". Send it yourself."
		a.Log.Warn("invite email failed", "user_id", inv.User.ID, "error", err)
		return
	}
	inv.Emailed = true
}

func inviteURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/invite?code=" + url.QueryEscape(code)
}

func describeRoles(machine string, projects map[string]string) string {
	var parts []string
	if machine != "" {
		parts = append(parts, "machine: "+machine)
	}
	for _, proj := range sortedKeys(projects) {
		parts = append(parts, proj+": "+projects[proj])
	}
	if len(parts) == 0 {
		return "no roles"
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// UpdateUser changes a person's roles (IAM-05, IAM-06). The last machine admin keeps the role.
func (a *Auth) UpdateUser(ctx context.Context, p Principal, id string, req api.UpdateUser) (api.UserSummary, error) {
	if errs := api.ValidateUpdateUser(req); len(errs) > 0 {
		return api.UserSummary{}, api.Validation(errs)
	}
	if err := a.checkGrant(ctx, p, req.MachineRoleSet, sortedKeys(req.ProjectRoles)); err != nil {
		return api.UserSummary{}, err
	}
	var detail string
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		u, err := q.UserByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return userNotFound()
		}
		if err != nil {
			return err
		}
		if !p.IsMachineAdmin() {
			if _, err := a.visibleTo(ctx, q, p, id); err != nil {
				return err
			}
		}
		if req.MachineRoleSet {
			next := ""
			if req.MachineRole != nil {
				next = *req.MachineRole
			}
			if u.MachineRole == api.RoleAdmin && next != api.RoleAdmin && u.PasswordHash != "" {
				if err := lastAdminGuard(ctx, q); err != nil {
					return err
				}
			}
			if err := q.SetMachineRole(ctx, id, next); err != nil {
				return err
			}
			detail = "machine: " + orNone(next)
		}
		for _, proj := range sortedKeys(req.ProjectRoles) {
			role := ""
			if r := req.ProjectRoles[proj]; r != nil {
				role = *r
			}
			if err := q.SetProjectRole(ctx, id, proj, role); err != nil {
				return err
			}
			detail = strings.TrimPrefix(detail+", "+proj+": "+orNone(role), ", ")
		}
		return nil
	})
	if err != nil {
		return api.UserSummary{}, err
	}
	u, _ := a.Store.UserByID(ctx, id)
	a.audit(ctx, p, "user_roles_changed", u.Email, detail)
	return a.GetUser(ctx, p, id)
}

// visibleTo refuses a person outside the project admin's projects.
func (a *Auth) visibleTo(ctx context.Context, q *store.Queries, p Principal, id string) (map[string]string, error) {
	roles, err := q.ProjectRoles(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, proj := range a.adminProjects(p) {
		if _, ok := roles[proj]; ok {
			return roles, nil
		}
	}
	return nil, userNotFound()
}

func lastAdminGuard(ctx context.Context, q *store.Queries) error {
	n, err := q.CountMachineAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return api.NewError(api.CodeLastAdmin, "This is the last machine admin. Make someone else an admin first.")
	}
	return nil
}

// DeleteUser removes a person, ending their sessions and tokens at once (machine admins, IAM-06).
func (a *Auth) DeleteUser(ctx context.Context, p Principal, id string) error {
	if !p.IsMachineAdmin() {
		return api.NewError(api.CodeForbidden, "Only machine admins remove people.")
	}
	var email string
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		u, err := q.UserByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return userNotFound()
		}
		if err != nil {
			return err
		}
		email = u.Email
		if u.MachineRole == api.RoleAdmin && u.PasswordHash != "" {
			if err := lastAdminGuard(ctx, q); err != nil {
				return err
			}
		}
		return q.DeleteUser(ctx, id)
	})
	if err == nil {
		a.audit(ctx, p, "user_removed", email, "Sessions and tokens ended.")
		a.Log.Info("user removed", "user_id", id, "by", p.Key())
	}
	return err
}

// ResetUser issues a new link: a fresh invite for an invited person, a password reset for an active one
// (IAM-07). A reset ends the person's sessions.
func (a *Auth) ResetUser(ctx context.Context, p Principal, id, base string) (api.Invitation, error) {
	if !p.IsMachineAdmin() {
		return api.Invitation{}, api.NewError(api.CodeForbidden, "Only machine admins issue reset links.")
	}
	now := a.Now()
	code := auth.RandomSecret(32)
	var user store.User
	err := a.Store.WithTx(ctx, func(q *store.Queries) error {
		var err error
		user, err = q.UserByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return userNotFound()
		}
		if err != nil {
			return err
		}
		kind := api.InviteKindInvite
		if user.PasswordHash != "" {
			kind = api.InviteKindReset
			if err := q.DeleteUserSessions(ctx, id, ""); err != nil {
				return err
			}
		}
		return q.PutInvite(ctx, store.Invite{UserID: id, CodeHash: auth.Hash(code), Kind: kind, ExpiresAt: now.Add(api.InviteTTL)})
	})
	if err != nil {
		return api.Invitation{}, err
	}
	roles, _ := a.Store.ProjectRoles(ctx, id)
	a.audit(ctx, p, "user_link_issued", user.Email, "")
	inv := api.Invitation{User: toUserSummary(user, roles), URL: inviteURL(base, code), ExpiresAt: now.Add(api.InviteTTL)}
	kind := api.InviteKindInvite
	if user.PasswordHash != "" {
		kind = api.InviteKindReset
	}
	a.emailLink(ctx, p, &inv, kind)
	return inv, nil
}

func inviteInvalid() error {
	return api.NewError(api.CodeInviteInvalid, "This link was used already or has expired. Ask an admin for a new one.")
}

// InviteInfo describes an open invite or reset link.
func (a *Auth) InviteInfo(ctx context.Context, code string) (api.InviteInfo, error) {
	in, err := a.Store.InviteByHash(ctx, auth.Hash(strings.TrimSpace(code)), a.Now())
	if errors.Is(err, store.ErrNotFound) {
		return api.InviteInfo{}, inviteInvalid()
	}
	if err != nil {
		return api.InviteInfo{}, err
	}
	u, err := a.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return api.InviteInfo{}, inviteInvalid()
	}
	return api.InviteInfo{Email: u.Email, Kind: in.Kind}, nil
}

// AcceptInvite sets the person's password from a link and signs them in.
func (a *Auth) AcceptInvite(ctx context.Context, req api.AcceptInvite, clientIP string) (api.Session, string, error) {
	key := "invite|" + clientIP
	if blocked, wait := a.Throttle.Blocked(key); blocked {
		return api.Session{}, "", &api.Error{Code: api.CodeTooManyAttempts, RetryAfterSeconds: int(wait.Seconds()) + 1,
			Message: fmt.Sprintf("Too many attempts. Try again in %s.", humanDuration(wait))}
	}
	if errs := api.ValidatePassword(req.Password); len(errs) > 0 {
		return api.Session{}, "", api.Validation(errs)
	}
	in, err := a.Store.InviteByHash(ctx, auth.Hash(strings.TrimSpace(req.Code)), a.Now())
	if errors.Is(err, store.ErrNotFound) {
		a.Throttle.Fail(key)
		return api.Session{}, "", inviteInvalid()
	}
	if err != nil {
		return api.Session{}, "", err
	}
	hash, err := auth.HashPassword(req.Password)
	auth.ReleaseMemory()
	if err != nil {
		return api.Session{}, "", err
	}
	err = a.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SetPassword(ctx, in.UserID, hash); err != nil {
			return err
		}
		if err := q.DeleteUserSessions(ctx, in.UserID, ""); err != nil {
			return err
		}
		return q.DeleteInvite(ctx, in.UserID)
	})
	if err != nil {
		return api.Session{}, "", err
	}
	a.Throttle.Succeed(key)
	user, err := a.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return api.Session{}, "", err
	}
	a.audit(ctx, Principal{UserID: user.ID, Email: user.Email, ClientIP: clientIP}, in.Kind+"_accepted", user.Email, "")
	return a.startSession(ctx, user, clientIP)
}

// Audit lists audit entries (machine admins, IAM-09).
func (a *Auth) Audit(ctx context.Context, p Principal, limit int, cursor string) (api.AuditList, error) {
	if !p.IsMachineAdmin() {
		return api.AuditList{}, api.NewError(api.CodeForbidden, "Only machine admins read the audit list.")
	}
	rows, next, err := a.Store.ListAudit(ctx, limit, cursor)
	if errors.Is(err, store.ErrBadCursor) {
		return api.AuditList{}, api.NewError(api.CodeMalformedRequest, "That cursor isn't valid. Start from the first page.")
	}
	if err != nil {
		return api.AuditList{}, err
	}
	out := api.AuditList{Items: make([]api.AuditEntry, 0, len(rows))}
	for _, e := range rows {
		out.Items = append(out.Items, api.AuditEntry{ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail, ClientIP: e.ClientIP})
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// Audit records an entry on behalf of another service (secret reveals).
func (a *Auth) Record(ctx context.Context, p Principal, action, target, detail string) {
	a.audit(ctx, p, action, target, detail)
}
