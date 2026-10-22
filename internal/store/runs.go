package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// Run history for jobs and backups (L3-005 §5, ADR-017).

const runCols = `id, resource_id, trigger, status, scheduled_for, started_at, finished_at, exit_code, message, skipped_count, artifact_json, subject_key, outputs_json`

// RunRetention keeps the newest RunKeepCount runs of each resource, plus every run younger than RunKeepAge.
const (
	RunKeepCount = 100
	RunKeepAge   = 30 * 24 * time.Hour
)

// InsertRun records a new run.
func (q *Queries) InsertRun(ctx context.Context, r *domain.Run) error {
	art, err := encodeArtifact(r.Artifact)
	if err != nil {
		return err
	}
	_, err = q.q.ExecContext(ctx, `INSERT INTO runs (`+runCols+`, output_tail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ResourceID, r.Trigger, r.Status, tsPtr(r.ScheduledFor), ts(r.StartedAt), tsPtr(r.FinishedAt), r.ExitCode, r.Message,
		r.SkippedCount, art, nullString(r.Key), encodeKeys(r.Outputs), r.OutputTail)
	return err
}

// FinishRun records a run's outcome. It only changes a run that is still running, and reports whether it did.
func (q *Queries) FinishRun(ctx context.Context, r *domain.Run) (bool, error) {
	art, err := encodeArtifact(r.Artifact)
	if err != nil {
		return false, err
	}
	res, err := q.q.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, exit_code = ?, message = ?, output_tail = ?, artifact_json = ?,
		outputs_json = ? WHERE id = ? AND status = 'running'`, r.Status, tsPtr(r.FinishedAt), r.ExitCode, r.Message, r.OutputTail, art, encodeKeys(r.Outputs), r.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetRun returns one run of a resource, with its output tail.
func (q *Queries) GetRun(ctx context.Context, resourceID, runID string) (*domain.Run, error) {
	row := q.q.QueryRowContext(ctx, `SELECT `+runCols+`, output_tail FROM runs WHERE resource_id = ? AND id = ?`, resourceID, runID)
	r, err := scanRun(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRuns pages through a resource's runs, newest first (without output tails).
func (q *Queries) ListRuns(ctx context.Context, resourceID string, limit int, cursor string) ([]*domain.Run, string, error) {
	args := []any{resourceID}
	where := `resource_id = ?`
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		started, id, ok := strings.Cut(string(raw), "|")
		if err != nil || !ok {
			return nil, "", ErrBadCursor
		}
		where += ` AND (started_at < ? OR (started_at = ? AND id < ?))`
		args = append(args, started, started, id)
	}
	args = append(args, limit+1)
	rows, err := q.q.QueryContext(ctx, `SELECT `+runCols+` FROM runs WHERE `+where+` ORDER BY started_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		r, err := scanRun(rows, false)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(ts(last.StartedAt) + "|" + last.ID))
		out = out[:limit]
	}
	return out, next, nil
}

// RunningRuns returns every run still marked running (the scheduler adopts them).
func (q *Queries) RunningRuns(ctx context.Context) ([]*domain.Run, error) {
	return q.runs(ctx, `SELECT `+runCols+` FROM runs WHERE status = 'running' ORDER BY started_at, id`)
}

// HasRunningRun reports whether a resource has a run in progress (overlap policy).
func (q *Queries) HasRunningRun(ctx context.Context, resourceID string) (bool, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE resource_id = ? AND status = 'running'`, resourceID).Scan(&n)
	return n > 0, err
}

// DueSchedules returns ready schedulable resources whose next run is due.
func (q *Queries) DueSchedules(ctx context.Context, now time.Time) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE next_run_at IS NOT NULL AND next_run_at <= ? AND phase = 'ready'
		ORDER BY next_run_at, id`, ts(now))
}

// SetNextRun moves a resource's next run without touching anything else.
func (q *Queries) SetNextRun(ctx context.Context, id string, next *time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE resources SET next_run_at = ? WHERE id = ?`, tsPtr(next), id)
	return err
}

// ArtifactRunIDs returns the IDs of a resource's runs that still record a stored backup.
func (q *Queries) ArtifactRunIDs(ctx context.Context, resourceID string) (map[string]bool, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT id FROM runs WHERE resource_id = ? AND artifact_json IS NOT NULL`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// RestoringRuns returns the runs of a backup that a database or cache is waiting to be restored from
// (the pendingRestore output), so retention leaves their objects alone until the restore has read them.
func (q *Queries) RestoringRuns(ctx context.Context, backupID string) (map[string]bool, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT json_extract(outputs_json, '$.pendingRestore.runId') FROM resources
		WHERE json_extract(outputs_json, '$.pendingRestore.backupId') = ?`, backupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.Valid {
			ids[id.String] = true
		}
	}
	return ids, rows.Err()
}

// ForgetArtifacts records that the backups of these runs were removed by retention: the runs keep their
// history but can no longer be restored (L2-005 BAK-04).
func (q *Queries) ForgetArtifacts(ctx context.Context, resourceID string, runIDs []string) error {
	for _, id := range runIDs {
		if _, err := q.q.ExecContext(ctx, `UPDATE runs SET artifact_json = NULL, message = message || ' Removed by retention.'
			WHERE resource_id = ? AND id = ? AND artifact_json IS NOT NULL`, resourceID, id); err != nil {
			return err
		}
	}
	return nil
}

// MergeOutputs sets some output keys of a resource without touching anything else.
func (q *Queries) MergeOutputs(ctx context.Context, id string, outputs map[string]any) error {
	var raw string
	if err := q.q.QueryRowContext(ctx, `SELECT outputs_json FROM resources WHERE id = ?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	merged := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &merged)
	for k, v := range outputs {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	_, err = q.q.ExecContext(ctx, `UPDATE resources SET outputs_json = ? WHERE id = ?`, string(b), id)
	return err
}

// RecordRunOutcome updates a resource's consecutive failures and merges outputs (last run facts) without
// touching its spec or phase.
func (q *Queries) RecordRunOutcome(ctx context.Context, id string, failed bool, outputs map[string]any) (int, error) {
	var raw string
	var fails int
	if err := q.q.QueryRowContext(ctx, `SELECT outputs_json, consecutive_failures FROM resources WHERE id = ?`, id).Scan(&raw, &fails); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	merged := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &merged)
	for k, v := range outputs {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return 0, err
	}
	if failed {
		fails++
	} else {
		fails = 0
	}
	_, err = q.q.ExecContext(ctx, `UPDATE resources SET outputs_json = ?, consecutive_failures = ? WHERE id = ?`, string(b), fails, id)
	return fails, err
}

// SweepRuns deletes finished runs older than RunKeepAge beyond each resource's newest RunKeepCount.
func (q *Queries) SweepRuns(ctx context.Context, now time.Time) (int64, error) {
	res, err := q.q.ExecContext(ctx, `DELETE FROM runs WHERE status != 'running' AND started_at < ? AND id NOT IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY resource_id ORDER BY started_at DESC, id DESC) AS n FROM runs) WHERE n <= ?)`,
		ts(now.Add(-RunKeepAge)), RunKeepCount)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (q *Queries) runs(ctx context.Context, query string, args ...any) ([]*domain.Run, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		r, err := scanRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRun(s scanner, withOutput bool) (*domain.Run, error) {
	var r domain.Run
	var scheduled, finished, art, key sql.NullString
	var exit sql.NullInt64
	var started, outputs string
	dest := []any{&r.ID, &r.ResourceID, &r.Trigger, &r.Status, &scheduled, &started, &finished, &exit, &r.Message, &r.SkippedCount, &art, &key, &outputs}
	if withOutput {
		dest = append(dest, &r.OutputTail)
	}
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	r.ScheduledFor, r.FinishedAt, r.StartedAt = parseNullTS(scheduled), parseNullTS(finished), parseTS(started)
	if exit.Valid {
		n := int(exit.Int64)
		r.ExitCode = &n
	}
	r.Key = key.String
	if err := json.Unmarshal([]byte(outputs), &r.Outputs); err != nil {
		return nil, fmt.Errorf("run %s outputs: %w", r.ID, err)
	}
	if art.Valid && art.String != "" {
		r.Artifact = &api.Artifact{}
		if err := json.Unmarshal([]byte(art.String), r.Artifact); err != nil {
			return nil, fmt.Errorf("run %s artifact: %w", r.ID, err)
		}
	}
	return &r, nil
}

func encodeArtifact(a *api.Artifact) (any, error) {
	if a == nil {
		return nil, nil
	}
	b, err := json.Marshal(a)
	return string(b), err
}

func encodeKeys(keys []string) string {
	if len(keys) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(keys)
	return string(b)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
