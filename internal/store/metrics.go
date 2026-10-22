package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Metrics (ADR-037): a series per (resource, metric), 30-second points for a day and 15-minute rollups for
// 30 days. Times are unix seconds.

// MachineSeries is the resource ID of the machine's own series.
const MachineSeries = "machine"

// Point is one sample of a series.
type Point struct {
	ResourceID string
	Metric     string
	TS         int64
	Value      float64
}

// SeriesPoint is a point read back; Max is set for rollups.
type SeriesPoint struct {
	TS    int64
	Value float64
	Max   *float64
}

// seriesID returns the ID of a series, creating it when missing.
func (q *Queries) seriesID(ctx context.Context, resourceID, metric string) (int64, error) {
	var id int64
	err := q.q.QueryRowContext(ctx, `SELECT id FROM metric_series WHERE resource_id = ? AND metric = ?`, resourceID, metric).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.q.ExecContext(ctx, `INSERT INTO metric_series (resource_id, metric) VALUES (?, ?)`, resourceID, metric)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertPoints stores a sample's points; a point at an existing time replaces it.
func (q *Queries) InsertPoints(ctx context.Context, points []Point) error {
	ids := map[[2]string]int64{}
	for _, p := range points {
		key := [2]string{p.ResourceID, p.Metric}
		id, ok := ids[key]
		if !ok {
			var err error
			if id, err = q.seriesID(ctx, p.ResourceID, p.Metric); err != nil {
				return err
			}
			ids[key] = id
		}
		if _, err := q.q.ExecContext(ctx, `INSERT INTO metric_points (series_id, ts, value) VALUES (?, ?, ?)
			ON CONFLICT(series_id, ts) DO UPDATE SET value = excluded.value`, id, p.TS, p.Value); err != nil {
			return err
		}
	}
	return nil
}

// QueryPoints returns a series' raw points in [from, to], averaged into step-second buckets (step ≥ 30).
func (q *Queries) QueryPoints(ctx context.Context, resourceID, metric string, from, to, step int64) ([]SeriesPoint, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT (p.ts / ?) * ?, AVG(p.value), MAX(p.value) FROM metric_points p
		JOIN metric_series s ON s.id = p.series_id
		WHERE s.resource_id = ? AND s.metric = ? AND p.ts >= ? AND p.ts <= ?
		GROUP BY p.ts / ? ORDER BY 1`, step, step, resourceID, metric, from, to, step)
	if err != nil {
		return nil, err
	}
	return scanSeries(rows, step > 30)
}

// QueryRollups returns a series' 15-minute rollups in [from, to], merged into step-second buckets.
func (q *Queries) QueryRollups(ctx context.Context, resourceID, metric string, from, to, step int64) ([]SeriesPoint, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT (r.ts / ?) * ?, SUM(r.avg * r.count) / SUM(r.count), MAX(r.max) FROM metric_rollups r
		JOIN metric_series s ON s.id = r.series_id
		WHERE s.resource_id = ? AND s.metric = ? AND r.ts >= ? AND r.ts <= ?
		GROUP BY r.ts / ? ORDER BY 1`, step, step, resourceID, metric, from, to, step)
	if err != nil {
		return nil, err
	}
	return scanSeries(rows, true)
}

func scanSeries(rows *sql.Rows, withMax bool) ([]SeriesPoint, error) {
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		var mx float64
		if err := rows.Scan(&p.TS, &p.Value, &mx); err != nil {
			return nil, err
		}
		if withMax {
			p.Max = &mx
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecentPoints returns a series' raw points since a time, oldest first (alert evaluation).
func (q *Queries) RecentPoints(ctx context.Context, resourceID, metric string, since int64) ([]SeriesPoint, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT p.ts, p.value, p.value FROM metric_points p JOIN metric_series s ON s.id = p.series_id
		WHERE s.resource_id = ? AND s.metric = ? AND p.ts >= ? ORDER BY p.ts`, resourceID, metric, since)
	if err != nil {
		return nil, err
	}
	return scanSeries(rows, false)
}

// LatestValues returns each series' newest point no older than since, for /metrics (MET-07).
func (q *Queries) LatestValues(ctx context.Context, since int64) ([]Point, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT s.resource_id, s.metric, p.ts, p.value FROM metric_series s
		JOIN metric_points p ON p.series_id = s.id
		WHERE p.ts = (SELECT MAX(ts) FROM metric_points WHERE series_id = s.id) AND p.ts >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.ResourceID, &p.Metric, &p.TS, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RollUp folds raw points of finished buckets (bucket start < before) into rollups; buckets are bucket
// seconds wide. Re-running is safe: a bucket is rewritten from its points.
func (q *Queries) RollUp(ctx context.Context, bucket, after, before int64) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO metric_rollups (series_id, ts, avg, max, count)
		SELECT series_id, (ts / ?) * ?, AVG(value), MAX(value), COUNT(*) FROM metric_points
		WHERE ts >= ? AND ts < ? GROUP BY series_id, ts / ?
		ON CONFLICT(series_id, ts) DO UPDATE SET avg = excluded.avg, max = excluded.max, count = excluded.count`,
		bucket, bucket, after, before, bucket)
	return err
}

// SweepMetrics deletes points older than pointsBefore and rollups older than rollupsBefore, at most limit
// rows of each per call; it reports whether more remain.
func (q *Queries) SweepMetrics(ctx context.Context, pointsBefore, rollupsBefore int64, limit int) (bool, error) {
	more := false
	for _, t := range []struct {
		table  string
		before int64
	}{{"metric_points", pointsBefore}, {"metric_rollups", rollupsBefore}} {
		res, err := q.q.ExecContext(ctx, `DELETE FROM `+t.table+` WHERE (series_id, ts) IN (SELECT series_id, ts FROM `+t.table+` WHERE ts < ? LIMIT ?)`, t.before, limit)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n >= int64(limit) {
			more = true
		}
	}
	return more, nil
}

// DeleteSeriesOfGone removes the series, points and rollups of resources that no longer exist.
func (q *Queries) DeleteSeriesOfGone(ctx context.Context) (int64, error) {
	gone := `SELECT id FROM metric_series WHERE resource_id != '` + MachineSeries + `' AND resource_id NOT IN (SELECT id FROM resources)`
	for _, table := range []string{"metric_points", "metric_rollups"} {
		if _, err := q.q.ExecContext(ctx, `DELETE FROM `+table+` WHERE series_id IN (`+gone+`)`); err != nil {
			return 0, err
		}
	}
	res, err := q.q.ExecContext(ctx, `DELETE FROM metric_series WHERE id IN (`+gone+`)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Unix is a time as stored in metric tables.
func Unix(t time.Time) int64 { return t.Unix() }
