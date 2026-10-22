package store

import (
	"context"
	"time"
)

// v0.6 bookkeeping (L3-005 §8): delivery counters per topic subscription (ADR-029) and the objects a
// transcoder has seen (ADR-034).

// DeadLetterTarget is the counter row of a queue (not a topic) that counts its messages past their
// maximum receives (L3-005 §8).
const DeadLetterTarget = "dead-letters"

// SubscriptionCount is one subscription's stored counters.
type SubscriptionCount struct {
	Target    string
	Delivered int64
	Failed    int64
	LastError string
}

// CountDelivery adds one delivery outcome for a subscription; an error message is kept as the last error
// (a success clears it).
func (q *Queries) CountDelivery(ctx context.Context, topicID, target string, ok bool, lastError string, at time.Time) error {
	delivered, failed := 1, 0
	if !ok {
		delivered, failed = 0, 1
	}
	_, err := q.q.ExecContext(ctx, `INSERT INTO subscription_stats (topic_id, target, delivered, failed, last_error, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (topic_id, target) DO UPDATE SET delivered = delivered + excluded.delivered, failed = failed + excluded.failed,
		last_error = excluded.last_error, updated_at = excluded.updated_at`, topicID, target, delivered, failed, lastError, ts(at))
	return err
}

// NoteDeliveryError records a failed attempt's reason without counting it (a retry is still to come).
func (q *Queries) NoteDeliveryError(ctx context.Context, topicID, target, lastError string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO subscription_stats (topic_id, target, last_error, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (topic_id, target) DO UPDATE SET last_error = excluded.last_error, updated_at = excluded.updated_at`, topicID, target, lastError, ts(at))
	return err
}

// SubscriptionCounts returns a topic's counters by target.
func (q *Queries) SubscriptionCounts(ctx context.Context, topicID string) (map[string]SubscriptionCount, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT target, delivered, failed, last_error FROM subscription_stats WHERE topic_id = ?`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SubscriptionCount{}
	for rows.Next() {
		var c SubscriptionCount
		if err := rows.Scan(&c.Target, &c.Delivered, &c.Failed, &c.LastError); err != nil {
			return nil, err
		}
		out[c.Target] = c
	}
	return out, rows.Err()
}

// PruneSubscriptionCounts deletes the counters of subscriptions a topic no longer has.
func (q *Queries) PruneSubscriptionCounts(ctx context.Context, topicID string, keep []string) error {
	current, err := q.SubscriptionCounts(ctx, topicID)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, t := range keep {
		wanted[t] = true
	}
	for t := range current {
		if !wanted[t] {
			if _, err := q.q.ExecContext(ctx, `DELETE FROM subscription_stats WHERE topic_id = ? AND target = ?`, topicID, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// SeenObjects returns the keys a transcoder has seen, with the ETag each had then.
func (q *Queries) SeenObjects(ctx context.Context, resourceID string) (map[string]string, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT key, etag FROM transcoder_objects WHERE resource_id = ?`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, e string
		if err := rows.Scan(&k, &e); err != nil {
			return nil, err
		}
		out[k] = e
	}
	return out, rows.Err()
}

// HasSeenObjects reports whether a transcoder has taken its first listing.
func (q *Queries) HasSeenObjects(ctx context.Context, resourceID string) (bool, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcoder_objects WHERE resource_id = ?`, resourceID).Scan(&n)
	return n > 0, err
}

// MarkSeen records an object as seen, with the run converting it ("" for none).
func (q *Queries) MarkSeen(ctx context.Context, resourceID, key, etag, runID string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO transcoder_objects (resource_id, key, etag, run_id, seen_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (resource_id, key) DO UPDATE SET etag = excluded.etag, run_id = excluded.run_id, seen_at = excluded.seen_at`,
		resourceID, key, etag, nullString(runID), ts(at))
	return err
}

// ForgetSeen drops a transcoder's seen objects, so its next listing starts over (its input changed).
func (q *Queries) ForgetSeen(ctx context.Context, resourceID string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM transcoder_objects WHERE resource_id = ?`, resourceID)
	return err
}

// CountRunning returns how many runs of a resource are in progress.
func (q *Queries) CountRunning(ctx context.Context, resourceID string) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE resource_id = ? AND status = 'running'`, resourceID).Scan(&n)
	return n, err
}
