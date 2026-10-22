package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Builds of source apps (L3-005, ADR-019).

// BuildKeepCount is how many builds of each app are kept.
const BuildKeepCount = 10

// Build statuses.
const (
	BuildRunning   = "running"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
	BuildSkipped   = "skipped"
)

// Build is one build of a source app.
type Build struct {
	ID, ResourceID, Status, Commit, InputsHash, Image, Message string
	StartedAt                                                  time.Time
	FinishedAt                                                 *time.Time
	Log                                                        []byte
}

const buildCols = `id, resource_id, status, commit_sha, inputs_hash, image, message, started_at, finished_at, log`

// InsertBuild records a build and trims the app's history to the newest BuildKeepCount.
func (q *Queries) InsertBuild(ctx context.Context, b *Build) error {
	if _, err := q.q.ExecContext(ctx, `INSERT INTO builds (`+buildCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ResourceID, b.Status, b.Commit, b.InputsHash, b.Image, b.Message, ts(b.StartedAt), tsPtr(b.FinishedAt), b.Log); err != nil {
		return err
	}
	_, err := q.q.ExecContext(ctx, `DELETE FROM builds WHERE resource_id = ? AND id NOT IN
		(SELECT id FROM builds WHERE resource_id = ? ORDER BY started_at DESC, id DESC LIMIT ?)`, b.ResourceID, b.ResourceID, BuildKeepCount)
	return err
}

// FinishBuild records a build's outcome.
func (q *Queries) FinishBuild(ctx context.Context, b *Build) error {
	_, err := q.q.ExecContext(ctx, `UPDATE builds SET status = ?, commit_sha = ?, inputs_hash = ?, image = ?, message = ?, finished_at = ?, log = ? WHERE id = ?`,
		b.Status, b.Commit, b.InputsHash, b.Image, b.Message, tsPtr(b.FinishedAt), b.Log, b.ID)
	return err
}

// LatestBuild returns an app's newest build that ran (skipped builds are not "the build log"), or ErrNotFound.
func (q *Queries) LatestBuild(ctx context.Context, resourceID string) (*Build, error) {
	row := q.q.QueryRowContext(ctx, `SELECT `+buildCols+` FROM builds WHERE resource_id = ? AND status != 'skipped'
		ORDER BY started_at DESC, id DESC LIMIT 1`, resourceID)
	var b Build
	var started string
	var finished sql.NullString
	err := row.Scan(&b.ID, &b.ResourceID, &b.Status, &b.Commit, &b.InputsHash, &b.Image, &b.Message, &started, &finished, &b.Log)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.StartedAt = parseTS(started)
	if finished.Valid {
		t := parseTS(finished.String)
		b.FinishedAt = &t
	}
	return &b, nil
}

// BuildCount returns how many builds of an app are stored (tests).
func (q *Queries) BuildCount(ctx context.Context, resourceID string) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM builds WHERE resource_id = ?`, resourceID).Scan(&n)
	return n, err
}
