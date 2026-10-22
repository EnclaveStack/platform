package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Alerts, incidents and the notification outbox (ADR-038, ADR-039).

// AlertState is one alert's state for one subject (a resource it watches).
type AlertState struct {
	AlertID        string
	SubjectID      string
	State          string
	Since          time.Time
	LastNotifiedAt *time.Time
	IncidentID     string
	LastCheckedAt  time.Time
}

// AlertStates returns an alert's states by subject ID.
func (q *Queries) AlertStates(ctx context.Context, alertID string) (map[string]AlertState, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT alert_id, subject_id, state, since, last_notified_at, COALESCE(incident_id, ''), last_checked_at
		FROM alert_states WHERE alert_id = ?`, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AlertState{}
	for rows.Next() {
		var s AlertState
		var since, checked string
		var notified sql.NullString
		if err := rows.Scan(&s.AlertID, &s.SubjectID, &s.State, &since, &notified, &s.IncidentID, &checked); err != nil {
			return nil, err
		}
		s.Since, s.LastCheckedAt, s.LastNotifiedAt = parseTS(since), parseTS(checked), parseNullTS(notified)
		out[s.SubjectID] = s
	}
	return out, rows.Err()
}

// SaveAlertState writes one state.
func (q *Queries) SaveAlertState(ctx context.Context, s AlertState) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO alert_states (alert_id, subject_id, state, since, last_notified_at, incident_id, last_checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(alert_id, subject_id) DO UPDATE SET state = excluded.state, since = excluded.since, last_notified_at = excluded.last_notified_at,
			incident_id = excluded.incident_id, last_checked_at = excluded.last_checked_at`,
		s.AlertID, s.SubjectID, s.State, ts(s.Since), tsPtr(s.LastNotifiedAt), nullString(s.IncidentID), ts(s.LastCheckedAt))
	return err
}

// DeleteAlertStatesExcept drops states of subjects an alert no longer watches.
func (q *Queries) DeleteAlertStatesExcept(ctx context.Context, alertID string, keep []string) error {
	query := `DELETE FROM alert_states WHERE alert_id = ?`
	args := []any{alertID}
	if len(keep) > 0 {
		query += ` AND subject_id NOT IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(keep)), ", ") + `)`
		for _, k := range keep {
			args = append(args, k)
		}
	}
	_, err := q.q.ExecContext(ctx, query, args...)
	return err
}

// Incident is one firing of an alert for one subject.
type Incident struct {
	ID         string
	AlertID    string
	Subject    string
	StartedAt  time.Time
	ResolvedAt *time.Time
	Value      *float64
	Message    string
}

// CreateIncident opens an incident.
func (q *Queries) CreateIncident(ctx context.Context, in Incident) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO incidents (id, alert_id, subject, started_at, resolved_at, value, message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.AlertID, in.Subject, ts(in.StartedAt), tsPtr(in.ResolvedAt), in.Value, in.Message)
	return err
}

// ResolveIncident closes an incident.
func (q *Queries) ResolveIncident(ctx context.Context, id string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE incidents SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`, ts(at), id)
	return err
}

// GetIncident returns one incident.
func (q *Queries) GetIncident(ctx context.Context, id string) (Incident, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT id, alert_id, subject, started_at, resolved_at, value, message FROM incidents WHERE id = ?`, id)
	if err != nil {
		return Incident{}, err
	}
	list, err := scanIncidents(rows)
	if err != nil {
		return Incident{}, err
	}
	if len(list) == 0 {
		return Incident{}, ErrNotFound
	}
	return list[0], nil
}

// ListIncidents returns an alert's newest incidents first.
func (q *Queries) ListIncidents(ctx context.Context, alertID string, limit int) ([]Incident, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT id, alert_id, subject, started_at, resolved_at, value, message FROM incidents
		WHERE alert_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, alertID, limit)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

func scanIncidents(rows *sql.Rows) ([]Incident, error) {
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var in Incident
		var started string
		var resolved sql.NullString
		var value sql.NullFloat64
		if err := rows.Scan(&in.ID, &in.AlertID, &in.Subject, &started, &resolved, &value, &in.Message); err != nil {
			return nil, err
		}
		in.StartedAt, in.ResolvedAt = parseTS(started), parseNullTS(resolved)
		if value.Valid {
			v := value.Float64
			in.Value = &v
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SweepIncidents deletes resolved incidents older than before (ALR-05).
func (q *Queries) SweepIncidents(ctx context.Context, before time.Time) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM incidents WHERE resolved_at IS NOT NULL AND resolved_at < ?`, ts(before))
	return err
}

// Notification is one row of the outbox.
type Notification struct {
	ID            string
	ChannelID     string
	IncidentID    string
	Event         string
	Alert         string
	Subject       string
	BodyJSON      string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        *time.Time
}

const notificationCols = `id, channel_id, COALESCE(incident_id, ''), event, alert, subject, body_json, status, attempts, next_attempt_at, last_error, created_at, sent_at`

// InsertNotification queues a notification.
func (q *Queries) InsertNotification(ctx context.Context, n Notification) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO notifications (id, channel_id, incident_id, event, alert, subject, body_json, status, attempts, next_attempt_at, last_error, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, n.ID, n.ChannelID, nullString(n.IncidentID), n.Event, n.Alert, n.Subject, n.BodyJSON, n.Status, n.Attempts,
		ts(n.NextAttemptAt), n.LastError, ts(n.CreatedAt), tsPtr(n.SentAt))
	return err
}

// DueNotifications returns pending notifications due at now, oldest first.
func (q *Queries) DueNotifications(ctx context.Context, now time.Time, limit int) ([]Notification, error) {
	return q.notifications(ctx, `WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`, ts(now), limit)
}

// ChannelNotifications returns a channel's newest notifications first.
func (q *Queries) ChannelNotifications(ctx context.Context, channelID string, limit int) ([]Notification, error) {
	return q.notifications(ctx, `WHERE channel_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, channelID, limit)
}

// IncidentNotifications returns the notifications of incidents, oldest first.
func (q *Queries) IncidentNotifications(ctx context.Context, incidentIDs []string) ([]Notification, error) {
	if len(incidentIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(incidentIDs))
	for i, id := range incidentIDs {
		args[i] = id
	}
	return q.notifications(ctx, `WHERE incident_id IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(incidentIDs)), ", ")+`) ORDER BY created_at, id`, args...)
}

// GetNotification returns one notification.
func (q *Queries) GetNotification(ctx context.Context, id string) (Notification, error) {
	list, err := q.notifications(ctx, `WHERE id = ?`, id)
	if err != nil {
		return Notification{}, err
	}
	if len(list) == 0 {
		return Notification{}, ErrNotFound
	}
	return list[0], nil
}

func (q *Queries) notifications(ctx context.Context, where string, args ...any) ([]Notification, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT `+notificationCols+` FROM notifications `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var next, created string
		var sent sql.NullString
		if err := rows.Scan(&n.ID, &n.ChannelID, &n.IncidentID, &n.Event, &n.Alert, &n.Subject, &n.BodyJSON, &n.Status, &n.Attempts, &next, &n.LastError, &created, &sent); err != nil {
			return nil, err
		}
		n.NextAttemptAt, n.CreatedAt, n.SentAt = parseTS(next), parseTS(created), parseNullTS(sent)
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNotification records an attempt's outcome.
func (q *Queries) UpdateNotification(ctx context.Context, n Notification) error {
	_, err := q.q.ExecContext(ctx, `UPDATE notifications SET status = ?, attempts = ?, next_attempt_at = ?, last_error = ?, sent_at = ? WHERE id = ?`,
		n.Status, n.Attempts, ts(n.NextAttemptAt), n.LastError, tsPtr(n.SentAt), n.ID)
	return err
}

// PruneNotifications keeps a channel's newest keep finished notifications (NCH-04).
func (q *Queries) PruneNotifications(ctx context.Context, channelID string, keep int) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM notifications WHERE channel_id = ? AND status != 'pending' AND id NOT IN
		(SELECT id FROM notifications WHERE channel_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`, channelID, channelID, keep)
	return err
}

// FailedRunsSince returns runs of a resource that ended failed or timed out after since (ALR-02).
func (q *Queries) FailedRunsSince(ctx context.Context, resourceID string, since time.Time) (int, *time.Time, error) {
	var n int
	var last sql.NullString
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*), MAX(finished_at) FROM runs WHERE resource_id = ? AND status IN ('failed', 'timed_out') AND finished_at > ?`,
		resourceID, ts(since)).Scan(&n, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	return n, parseNullTS(last), err
}
