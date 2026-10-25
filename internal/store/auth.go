package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// User row. PasswordHash is "" while an invite is open; MachineRole is "" for none (ADR-036).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	MachineRole  string
	CreatedAt    time.Time
	LastSignInAt *time.Time
}

// Session row (the ID is stored only as a hash).
type Session struct {
	IDHash     string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// Token row. Projects nil means every project (ADR-036).
type Token struct {
	ID         string
	UserID     string
	Name       string
	Prefix     string
	SecretHash string
	Role       string
	Projects   []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// GetSetting returns a setting's value or ErrNotFound.
func (q *Queries) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := q.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting upserts a setting.
func (q *Queries) SetSetting(ctx context.Context, key, value string) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes a setting.
func (q *Queries) DeleteSetting(ctx context.Context, key string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// Setting keys (L3-005 §5).
const (
	SettingPublicAddress = "public_address"
	SettingACMEEmail     = "acme_email"
	// SettingInviteEmail holds the JSON api.InviteEmail that sends invite links (IAM-04).
	SettingInviteEmail = "invite_email"
	// SettingMetricsRolledUpTo is the end of the last 15-minute bucket rolled up (ADR-037).
	SettingMetricsRolledUpTo = "metrics_rolled_up_to"
)

// FirstUserEmail returns the email of the earliest user (the admin), or "" when there is none.
func (q *Queries) FirstUserEmail(ctx context.Context) (string, error) {
	var email string
	err := q.q.QueryRowContext(ctx, `SELECT email FROM users ORDER BY created_at, id LIMIT 1`).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// CountUsers returns how many users exist.
func (q *Queries) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user; a duplicate email returns ErrConflict.
func (q *Queries) CreateUser(ctx context.Context, u User) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, machine_role, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.PasswordHash, nullString(u.MachineRole), ts(u.CreatedAt))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

const userCols = `id, email, password_hash, COALESCE(machine_role, ''), created_at, last_sign_in_at`

// UserByEmail looks a user up case-insensitively.
func (q *Queries) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(q.q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// UserByID looks a user up by ID.
func (q *Queries) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(q.q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ListUsers returns every user, oldest first.
func (q *Queries) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanUser(row scanner) (User, error) {
	var u User
	var created string
	var seen sql.NullString
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.MachineRole, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.CreatedAt, u.LastSignInAt = parseTS(created), parseNullTS(seen)
	return u, err
}

// SetMachineRole sets or (with "") removes a user's machine role.
func (q *Queries) SetMachineRole(ctx context.Context, userID, role string) error {
	_, err := q.q.ExecContext(ctx, `UPDATE users SET machine_role = ? WHERE id = ?`, nullString(role), userID)
	return err
}

// SetPassword replaces a user's password hash.
func (q *Queries) SetPassword(ctx context.Context, userID, hash string) error {
	_, err := q.q.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

// TouchSignIn records a sign-in.
func (q *Queries) TouchSignIn(ctx context.Context, userID string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE users SET last_sign_in_at = ? WHERE id = ?`, ts(at), userID)
	return err
}

// DeleteUser removes a user; their sessions, tokens, roles and invite go with them (cascade).
func (q *Queries) DeleteUser(ctx context.Context, id string) error {
	res, err := q.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountMachineAdmins counts users with the machine admin role who can sign in or have an open invite.
func (q *Queries) CountMachineAdmins(ctx context.Context) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE machine_role = 'admin' AND password_hash != ''`).Scan(&n)
	return n, err
}

// DeleteUserSessions ends every session of a user except keepHash ("" ends all).
func (q *Queries) DeleteUserSessions(ctx context.Context, userID, keepHash string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, userID, keepHash)
	return err
}

// CreateSession inserts a session.
func (q *Queries) CreateSession(ctx context.Context, s Session) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		s.IDHash, s.UserID, ts(s.CreatedAt), ts(s.ExpiresAt), ts(s.LastSeenAt))
	return err
}

// SessionByHash returns a session that has not expired at now.
func (q *Queries) SessionByHash(ctx context.Context, hash string, now time.Time) (Session, error) {
	var s Session
	var created, expires, seen string
	err := q.q.QueryRowContext(ctx, `SELECT id_hash, user_id, created_at, expires_at, last_seen_at FROM sessions WHERE id_hash = ? AND expires_at > ?`, hash, ts(now)).
		Scan(&s.IDHash, &s.UserID, &created, &expires, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = parseTS(created), parseTS(expires), parseTS(seen)
	return s, err
}

// TouchSession slides a session's expiry.
func (q *Queries) TouchSession(ctx context.Context, hash string, seen, expires time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`, ts(seen), ts(expires), hash)
	return err
}

// DeleteSession removes one session.
func (q *Queries) DeleteSession(ctx context.Context, hash string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, hash)
	return err
}

// DeleteExpiredSessions removes sessions past expiry and returns how many.
func (q *Queries) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := q.q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, ts(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const tokenCols = `id, user_id, name, prefix, secret_hash, role, projects_json, created_at, last_used_at, expires_at, revoked_at`

// CreateToken inserts an API token.
func (q *Queries) CreateToken(ctx context.Context, t Token) error {
	var projects any
	if t.Projects != nil {
		b, err := json.Marshal(t.Projects)
		if err != nil {
			return err
		}
		projects = string(b)
	}
	_, err := q.q.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.Prefix, t.SecretHash, t.Role, projects, ts(t.CreatedAt), tsPtr(t.LastUsedAt), tsPtr(t.ExpiresAt), tsPtr(t.RevokedAt))
	return err
}

// ListAllTokens returns every user's tokens that are not revoked, newest first (TOK-04).
func (q *Queries) ListAllTokens(ctx context.Context) ([]Token, error) {
	return q.listTokens(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE revoked_at IS NULL ORDER BY created_at DESC, id DESC`)
}

// ListTokens returns a user's tokens that are not revoked, newest first.
func (q *Queries) ListTokens(ctx context.Context, userID string) ([]Token, error) {
	return q.listTokens(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, userID)
}

func (q *Queries) listTokens(ctx context.Context, query string, args ...any) ([]Token, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TokenByHash returns a token by the hash of its secret, including revoked or expired ones.
func (q *Queries) TokenByHash(ctx context.Context, hash string) (Token, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE secret_hash = ?`, hash)
	if err != nil {
		return Token{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Token{}, ErrNotFound
	}
	return scanToken(rows)
}

// TouchToken records a token's last use.
func (q *Queries) TouchToken(ctx context.Context, id string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts(at), id)
	return err
}

// RevokeToken revokes a user's token ("" as userID: anyone's); ErrNotFound if it is not theirs or already
// revoked.
func (q *Queries) RevokeToken(ctx context.Context, userID, id string, at time.Time) error {
	res, err := q.q.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND (user_id = ? OR ? = '') AND revoked_at IS NULL`, ts(at), id, userID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanToken(r scanner) (Token, error) {
	var t Token
	var created string
	var projects, used, expires, revoked sql.NullString
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.SecretHash, &t.Role, &projects, &created, &used, &expires, &revoked); err != nil {
		return t, err
	}
	if projects.Valid {
		if err := json.Unmarshal([]byte(projects.String), &t.Projects); err != nil {
			return t, fmt.Errorf("token %s projects: %w", t.ID, err)
		}
	}
	t.CreatedAt = parseTS(created)
	t.LastUsedAt, t.ExpiresAt, t.RevokedAt = parseNullTS(used), parseNullTS(expires), parseNullTS(revoked)
	return t, nil
}

// ExpireTokenForTest sets a token's expiry in the past. Used only by tests (testenv).
func (q *Queries) ExpireTokenForTest(ctx context.Context, id string) error {
	_, err := q.q.ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, ts(time.Now().Add(-time.Minute)), id)
	return err
}
