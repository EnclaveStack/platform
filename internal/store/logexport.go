package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Log export progress (ADR-040): the time of the last line sent per container, and counters.

// LogCursor is a container's checkpoint: the time of the last line sent, and how many lines with exactly
// that time were sent (lines can share a timestamp).
type LogCursor struct {
	At   time.Time
	Skip int
}

// LogCursors returns an export's checkpoints by container name.
func (q *Queries) LogCursors(ctx context.Context, exportID string) (map[string]LogCursor, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT container, ts, skip FROM logexport_cursors WHERE export_id = ?`, exportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LogCursor{}
	for rows.Next() {
		var c, at string
		var skip int
		if err := rows.Scan(&c, &at, &skip); err != nil {
			return nil, err
		}
		out[c] = LogCursor{At: parseTS(at), Skip: skip}
	}
	return out, rows.Err()
}

// SaveLogCursor moves a container's checkpoint.
func (q *Queries) SaveLogCursor(ctx context.Context, exportID, container string, c LogCursor) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO logexport_cursors (export_id, container, ts, skip) VALUES (?, ?, ?, ?)
		ON CONFLICT(export_id, container) DO UPDATE SET ts = excluded.ts, skip = excluded.skip`, exportID, container, ts(c.At), c.Skip)
	return err
}

// LogExportStats are an export's counters.
type LogExportStats struct {
	LinesSent  int64
	BytesSent  int64
	LastSentAt *time.Time
	LastError  string
	LagSeconds float64
}

// GetLogExportStats returns an export's counters (zero before its first batch).
func (q *Queries) GetLogExportStats(ctx context.Context, exportID string) (LogExportStats, error) {
	var s LogExportStats
	var last sql.NullString
	err := q.q.QueryRowContext(ctx, `SELECT lines_sent, bytes_sent, last_sent_at, last_error, lag_seconds FROM logexport_stats WHERE export_id = ?`, exportID).
		Scan(&s.LinesSent, &s.BytesSent, &last, &s.LastError, &s.LagSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	s.LastSentAt = parseNullTS(last)
	return s, err
}

// SaveLogExportStats writes an export's counters.
func (q *Queries) SaveLogExportStats(ctx context.Context, exportID string, s LogExportStats) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO logexport_stats (export_id, lines_sent, bytes_sent, last_sent_at, last_error, lag_seconds) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(export_id) DO UPDATE SET lines_sent = excluded.lines_sent, bytes_sent = excluded.bytes_sent, last_sent_at = excluded.last_sent_at,
			last_error = excluded.last_error, lag_seconds = excluded.lag_seconds`, exportID, s.LinesSent, s.BytesSent, tsPtr(s.LastSentAt), s.LastError, s.LagSeconds)
	return err
}
