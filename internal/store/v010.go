package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.10 tables (L3-005 §12): the cluster's epoch, the database CA, event alerts' cursors and standby copies.

// Epoch is the control plane's epoch: 0 until a standby is first promoted (ADR-057).
func (q *Queries) Epoch(ctx context.Context) (int64, error) {
	var n int64
	err := q.q.QueryRowContext(ctx, `SELECT epoch FROM cluster WHERE id = 1`).Scan(&n)
	return n, err
}

// Promote makes machineID the main machine, the old main offline with no address, and raises the epoch by
// one, in the caller's transaction (ADR-057). It returns the new epoch.
func (q *Queries) Promote(ctx context.Context, machineID string, now time.Time) (int64, error) {
	m, err := q.GetMachine(ctx, machineID)
	if err != nil {
		return 0, err
	}
	if m.Main {
		return q.Epoch(ctx)
	}
	if _, err := q.q.ExecContext(ctx, `UPDATE machines SET is_main = 0, state = 'offline', state_since = ?, address = NULL, schedulable = 0 WHERE is_main = 1`, ts(now)); err != nil {
		return 0, err
	}
	if _, err := q.q.ExecContext(ctx, `UPDATE machines SET is_main = 1, role = 'worker', state = 'online', state_since = ?, schedulable = 1 WHERE id = ?`, ts(now), machineID); err != nil {
		return 0, err
	}
	if _, err := q.q.ExecContext(ctx, `DELETE FROM standby_copies`); err != nil {
		return 0, err
	}
	if _, err := q.q.ExecContext(ctx, `UPDATE cluster SET epoch = epoch + 1 WHERE id = 1`); err != nil {
		return 0, err
	}
	return q.Epoch(ctx)
}

// DatabaseCA is the CA that signs databases' TLS certificates (ADR-058).
type DatabaseCA struct {
	CertPEM       string
	KeyCiphertext []byte
	CreatedAt     time.Time
}

// GetDatabaseCA returns the database CA, or ErrNotFound before the first TLS database.
func (q *Queries) GetDatabaseCA(ctx context.Context) (DatabaseCA, error) {
	var ca DatabaseCA
	var created string
	err := q.q.QueryRowContext(ctx, `SELECT cert_pem, key_ciphertext, created_at FROM database_ca WHERE id = 1`).Scan(&ca.CertPEM, &ca.KeyCiphertext, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ca, ErrNotFound
	}
	ca.CreatedAt = parseTS(created)
	return ca, err
}

// PutDatabaseCA stores the database CA once; a second one returns ErrConflict.
func (q *Queries) PutDatabaseCA(ctx context.Context, ca DatabaseCA) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO database_ca (id, cert_pem, key_ciphertext, created_at) VALUES (1, ?, ?, ?)`, ca.CertPEM, ca.KeyCiphertext, ts(ca.CreatedAt))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// ResourceEvent is one row of the event log with what an event alert needs.
type ResourceEvent struct {
	ID         int64
	ResourceID string
	Type       string
	Message    string
	CreatedAt  time.Time
}

// LastEventID is the newest event's ID (0 with none).
func (q *Queries) LastEventID(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := q.q.QueryRowContext(ctx, `SELECT MAX(id) FROM resource_events`).Scan(&n)
	return n.Int64, err
}

// EventsAfter returns up to limit events after the ID after, of the resources and types given, oldest first.
func (q *Queries) EventsAfter(ctx context.Context, after int64, resourceIDs, types []string, limit int) ([]ResourceEvent, error) {
	if len(resourceIDs) == 0 || len(types) == 0 {
		return nil, nil
	}
	args := []any{after}
	for _, id := range resourceIDs {
		args = append(args, id)
	}
	for _, t := range types {
		args = append(args, t)
	}
	args = append(args, limit)
	rows, err := q.q.QueryContext(ctx, `SELECT id, resource_id, type, message, created_at FROM resource_events WHERE id > ?
		AND resource_id IN (`+placeholders(len(resourceIDs))+`) AND type IN (`+placeholders(len(types))+`) ORDER BY id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResourceEvent
	for rows.Next() {
		var e ResourceEvent
		var created string
		if err := rows.Scan(&e.ID, &e.ResourceID, &e.Type, &e.Message, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AlertCursor returns the last event an event alert handled, and false when it has no cursor yet.
func (q *Queries) AlertCursor(ctx context.Context, alertID string) (int64, bool, error) {
	var n int64
	err := q.q.QueryRowContext(ctx, `SELECT event_id FROM alert_cursors WHERE alert_id = ?`, alertID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return n, err == nil, err
}

// SetAlertCursor moves an event alert's cursor.
func (q *Queries) SetAlertCursor(ctx context.Context, alertID string, eventID int64) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO alert_cursors (alert_id, event_id) VALUES (?, ?)
		ON CONFLICT (alert_id) DO UPDATE SET event_id = excluded.event_id`, alertID, eventID)
	return err
}

// DropAlertCursor forgets an alert's cursor, so it starts from the newest event when next evaluated (EVN-03).
func (q *Queries) DropAlertCursor(ctx context.Context, alertID string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM alert_cursors WHERE alert_id = ?`, alertID)
	return err
}

// StandbyCopy is what the main machine last sent its standby (ADR-057).
type StandbyCopy struct {
	MachineID   string
	LastCopyAt  *time.Time
	Bytes       int64
	LastError   string
	AttemptedAt time.Time
}

// Info is the copy as the API shows it, stale when the last copy is older than api.StandbyStaleAfter.
func (c StandbyCopy) Info(now time.Time) *api.StandbyInfo {
	return &api.StandbyInfo{LastCopyAt: c.LastCopyAt, LastCopyBytes: c.Bytes, LastCopyError: c.LastError,
		Stale: c.LastCopyAt == nil || now.Sub(*c.LastCopyAt) > api.StandbyStaleAfter}
}

// GetStandbyCopy returns a standby's last copy, or ErrNotFound before the first attempt.
func (q *Queries) GetStandbyCopy(ctx context.Context, machineID string) (StandbyCopy, error) {
	c := StandbyCopy{MachineID: machineID}
	var last sql.NullString
	var attempted string
	err := q.q.QueryRowContext(ctx, `SELECT last_copy_at, bytes, last_error, attempted_at FROM standby_copies WHERE machine_id = ?`, machineID).
		Scan(&last, &c.Bytes, &c.LastError, &attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.LastCopyAt, c.AttemptedAt = parseNullTS(last), parseTS(attempted)
	return c, err
}

// RecordStandbyCopy records an attempt: a success (err "") moves the last copy, a failure keeps it.
func (q *Queries) RecordStandbyCopy(ctx context.Context, machineID string, bytes int64, failure string, now time.Time) error {
	if failure == "" {
		_, err := q.q.ExecContext(ctx, `INSERT INTO standby_copies (machine_id, last_copy_at, bytes, last_error, attempted_at) VALUES (?, ?, ?, '', ?)
			ON CONFLICT (machine_id) DO UPDATE SET last_copy_at = excluded.last_copy_at, bytes = excluded.bytes, last_error = '', attempted_at = excluded.attempted_at`,
			machineID, ts(now), bytes, ts(now))
		return err
	}
	_, err := q.q.ExecContext(ctx, `INSERT INTO standby_copies (machine_id, last_error, attempted_at) VALUES (?, ?, ?)
		ON CONFLICT (machine_id) DO UPDATE SET last_error = excluded.last_error, attempted_at = excluded.attempted_at`, machineID, failure, ts(now))
	return err
}

// DatabaseCert is a TLS database's certificate (ADR-058). Names are its SANs, joined, so a change of
// address issues a new one.
type DatabaseCert struct {
	ResourceID    string
	CertPEM       string
	KeyCiphertext []byte
	Names         string
	Serial        string
	NotAfter      time.Time
}

// GetDatabaseCert returns a database's certificate, or ErrNotFound.
func (q *Queries) GetDatabaseCert(ctx context.Context, resourceID string) (DatabaseCert, error) {
	c := DatabaseCert{ResourceID: resourceID}
	var notAfter string
	err := q.q.QueryRowContext(ctx, `SELECT cert_pem, key_ciphertext, names, serial, not_after FROM database_certs WHERE resource_id = ?`, resourceID).
		Scan(&c.CertPEM, &c.KeyCiphertext, &c.Names, &c.Serial, &notAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.NotAfter = parseTS(notAfter)
	return c, err
}

// PutDatabaseCert stores (or replaces) a database's certificate.
func (q *Queries) PutDatabaseCert(ctx context.Context, c DatabaseCert) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO database_certs (resource_id, cert_pem, key_ciphertext, names, serial, not_after) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (resource_id) DO UPDATE SET cert_pem = excluded.cert_pem, key_ciphertext = excluded.key_ciphertext, names = excluded.names,
		serial = excluded.serial, not_after = excluded.not_after`, c.ResourceID, c.CertPEM, c.KeyCiphertext, c.Names, c.Serial, ts(c.NotAfter))
	return err
}

// VacuumInto writes a consistent copy of the database to path while writes go on (ADR-057).
func (s *Store) VacuumInto(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
