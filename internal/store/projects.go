package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Project row (ADR-035).
type Project struct {
	Name        string
	Description string
	CreatedAt   time.Time
}

// CreateProject inserts a project; a duplicate name returns ErrConflict.
func (q *Queries) CreateProject(ctx context.Context, p Project) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO projects (name, description, created_at) VALUES (?, ?, ?)`, p.Name, p.Description, ts(p.CreatedAt))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// GetProject returns a project or ErrNotFound.
func (q *Queries) GetProject(ctx context.Context, name string) (Project, error) {
	var p Project
	var created string
	err := q.q.QueryRowContext(ctx, `SELECT name, description, created_at FROM projects WHERE name = ?`, name).Scan(&p.Name, &p.Description, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = parseTS(created)
	return p, err
}

// ListProjects returns every project by name.
func (q *Queries) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT name, description, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var created string
		if err := rows.Scan(&p.Name, &p.Description, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTS(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProjectDescription changes a project's description.
func (q *Queries) UpdateProjectDescription(ctx context.Context, name, description string) error {
	res, err := q.q.ExecContext(ctx, `UPDATE projects SET description = ? WHERE name = ?`, description, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProject removes an empty project (its members cascade); one with resources returns ErrConflict.
func (q *Queries) DeleteProject(ctx context.Context, name string) error {
	res, err := q.q.ExecContext(ctx, `DELETE FROM projects WHERE name = ?`, name)
	if isForeignKey(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProjectResourceCounts counts resources per project.
func (q *Queries) ProjectResourceCounts(ctx context.Context) (map[string]int, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT project, COUNT(*) FROM resources GROUP BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// ProjectRoles returns a user's role in each project they have one in.
func (q *Queries) ProjectRoles(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT project, role FROM project_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, r string
		if err := rows.Scan(&p, &r); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}

// AllProjectRoles returns every user's project roles, by user ID.
func (q *Queries) AllProjectRoles(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT user_id, project, role FROM project_members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var u, p, r string
		if err := rows.Scan(&u, &p, &r); err != nil {
			return nil, err
		}
		if out[u] == nil {
			out[u] = map[string]string{}
		}
		out[u][p] = r
	}
	return out, rows.Err()
}

// SetProjectRole sets or (with "") removes a user's role in a project.
func (q *Queries) SetProjectRole(ctx context.Context, userID, project, role string) error {
	if role == "" {
		_, err := q.q.ExecContext(ctx, `DELETE FROM project_members WHERE user_id = ? AND project = ?`, userID, project)
		return err
	}
	_, err := q.q.ExecContext(ctx, `INSERT INTO project_members (user_id, project, role) VALUES (?, ?, ?)
		ON CONFLICT(user_id, project) DO UPDATE SET role = excluded.role`, userID, project, role)
	return err
}

// Invite row: a one-time code, stored as a hash (ADR-036).
type Invite struct {
	UserID    string
	CodeHash  string
	Kind      string
	ExpiresAt time.Time
}

// PutInvite creates or replaces a user's invite.
func (q *Queries) PutInvite(ctx context.Context, in Invite) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO invites (user_id, code_hash, kind, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET code_hash = excluded.code_hash, kind = excluded.kind, expires_at = excluded.expires_at`,
		in.UserID, in.CodeHash, in.Kind, ts(in.ExpiresAt))
	return err
}

// InviteByHash returns an invite that hasn't expired at now.
func (q *Queries) InviteByHash(ctx context.Context, hash string, now time.Time) (Invite, error) {
	var in Invite
	var expires string
	err := q.q.QueryRowContext(ctx, `SELECT user_id, code_hash, kind, expires_at FROM invites WHERE code_hash = ? AND expires_at > ?`, hash, ts(now)).
		Scan(&in.UserID, &in.CodeHash, &in.Kind, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	in.ExpiresAt = parseTS(expires)
	return in, err
}

// DeleteInvite removes a user's invite.
func (q *Queries) DeleteInvite(ctx context.Context, userID string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM invites WHERE user_id = ?`, userID)
	return err
}

// AuditEntry row (IAM-09).
type AuditEntry struct {
	ID       int64
	At       time.Time
	Actor    string
	Action   string
	Target   string
	Detail   string
	ClientIP string
}

// AddAudit appends an audit entry.
func (q *Queries) AddAudit(ctx context.Context, e AuditEntry) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO audit_log (at, actor, action, target, detail, client_ip) VALUES (?, ?, ?, ?, ?, ?)`,
		ts(e.At), e.Actor, e.Action, e.Target, e.Detail, e.ClientIP)
	return err
}

// ListAudit returns up to limit entries older than the cursor (an entry ID; "" for the newest), newest
// first, and the cursor of the next page.
func (q *Queries) ListAudit(ctx context.Context, limit int, cursor string) ([]AuditEntry, string, error) {
	before := int64(1<<62 - 1)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", ErrBadCursor
		}
		before = n
	}
	rows, err := q.q.QueryContext(ctx, `SELECT id, at, actor, action, target, detail, client_ip FROM audit_log WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.ClientIP); err != nil {
			return nil, "", err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, next, rows.Err()
}

// SweepAudit deletes entries older than before.
func (q *Queries) SweepAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := q.q.ExecContext(ctx, `DELETE FROM audit_log WHERE at <= ?`, ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
