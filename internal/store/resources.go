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

const resourceCols = `id, kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message,
	steps_json, outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at, serving_revision, next_run_at, consecutive_failures,
	project`

// InsertResource stores a new resource; a duplicate (project, kind, name) returns ErrConflict.
func (q *Queries) InsertResource(ctx context.Context, r *domain.Resource) error {
	steps, outputs, err := encodeStatus(r)
	if err != nil {
		return err
	}
	_, err = q.q.ExecContext(ctx, `INSERT INTO resources (`+resourceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, string(r.Kind), r.Name, string(r.Spec), r.SecretCiphertext, r.Generation, r.ObservedGeneration, string(r.Phase), r.Message,
		steps, outputs, r.OwnerID, r.Attempts, r.RequestedAction, tsPtr(r.NextAttemptAt), ts(r.LastTransitionAt), ts(r.CreatedAt), ts(r.UpdatedAt), r.ServingRevision, tsPtr(r.NextRunAt), r.ConsecutiveFailures, r.ProjectName())
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// SaveResource writes every mutable column of r.
func (q *Queries) SaveResource(ctx context.Context, r *domain.Resource) error {
	steps, outputs, err := encodeStatus(r)
	if err != nil {
		return err
	}
	res, err := q.q.ExecContext(ctx, `UPDATE resources SET spec_json = ?, secret_ciphertext = ?, generation = ?, observed_generation = ?,
		phase = ?, message = ?, steps_json = ?, outputs_json = ?, attempts = ?, requested_action = ?, next_attempt_at = ?, last_transition_at = ?, updated_at = ?,
		serving_revision = ?, next_run_at = ?, consecutive_failures = ? WHERE id = ?`,
		string(r.Spec), r.SecretCiphertext, r.Generation, r.ObservedGeneration, string(r.Phase), r.Message, steps, outputs,
		r.Attempts, r.RequestedAction, tsPtr(r.NextAttemptAt), ts(r.LastTransitionAt), ts(r.UpdatedAt), r.ServingRevision, tsPtr(r.NextRunAt), r.ConsecutiveFailures, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SaveStatus writes only the observed-state columns, and only if the spec generation is still gen —
// so a reconcile that finishes after the user changed the spec never overwrites the newer intent.
// It reports whether the row was updated.
func (q *Queries) SaveStatus(ctx context.Context, r *domain.Resource, gen int64) (bool, error) {
	steps, outputs, err := encodeStatus(r)
	if err != nil {
		return false, err
	}
	res, err := q.q.ExecContext(ctx, `UPDATE resources SET observed_generation = ?, phase = ?, message = ?, steps_json = ?, outputs_json = ?,
		attempts = ?, requested_action = ?, next_attempt_at = ?, last_transition_at = ?, updated_at = ?, serving_revision = ?,
		next_run_at = ?, consecutive_failures = ? WHERE id = ? AND generation = ? AND (phase <> ? OR ? = ?)`,
		r.ObservedGeneration, string(r.Phase), r.Message, steps, outputs, r.Attempts, r.RequestedAction, tsPtr(r.NextAttemptAt),
		ts(r.LastTransitionAt), ts(r.UpdatedAt), r.ServingRevision, tsPtr(r.NextRunAt), r.ConsecutiveFailures, r.ID, gen,
		// Deleting is final (domain phase rules): a writer that read the row before the delete can't undo it.
		string(api.PhaseDeleting), string(r.Phase), string(api.PhaseDeleting))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteResource removes a resource row (events and outgoing refs cascade). A resource still
// referenced by another returns ErrConflict.
func (q *Queries) DeleteResource(ctx context.Context, id string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM resources WHERE id = ?`, id)
	if isForeignKey(err) {
		return ErrConflict
	}
	return err
}

// GetResource returns a resource by ID.
func (q *Queries) GetResource(ctx context.Context, id string) (*domain.Resource, error) {
	return q.one(ctx, `SELECT `+resourceCols+` FROM resources WHERE id = ?`, id)
}

// GetResourceByName returns a resource by project, kind and name (ADR-035).
func (q *Queries) GetResourceByName(ctx context.Context, project string, kind api.Kind, name string) (*domain.Resource, error) {
	if project == "" {
		project = api.DefaultProject
	}
	return q.one(ctx, `SELECT `+resourceCols+` FROM resources WHERE project = ? AND kind = ? AND name = ?`, project, string(kind), name)
}

// ResourcesNamed returns every resource of a kind with a name, across projects (machine-wide names:
// buckets, ADR-035).
func (q *Queries) ResourcesNamed(ctx context.Context, kind api.Kind, name string) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE kind = ? AND name = ? ORDER BY created_at, id`, string(kind), name)
}

func (q *Queries) one(ctx context.Context, query string, args ...any) (*domain.Resource, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	return scanResource(rows)
}

// ListFilter selects resources for ListResources.
type ListFilter struct {
	Kind api.Kind
	Name string
	// Projects limits the list to these projects; nil means every project.
	Projects []string
	Limit    int
	Cursor   string
}

// ListResources pages through resources ordered by (created_at, id). The cursor is opaque to clients.
func (q *Queries) ListResources(ctx context.Context, f ListFilter) ([]*domain.Resource, string, error) {
	var where []string
	var args []any
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, string(f.Kind))
	}
	if f.Name != "" {
		where, args = append(where, "name = ?"), append(args, f.Name)
	}
	if f.Projects != nil {
		if len(f.Projects) == 0 {
			return nil, "", nil
		}
		where = append(where, "project IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(f.Projects)), ", ")+")")
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	if f.Cursor != "" {
		created, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where, args = append(where, "(created_at, id) > (?, ?)"), append(args, created, id)
	}
	query := `SELECT ` + resourceCols + ` FROM resources`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at, id LIMIT ?"
	args = append(args, f.Limit+1)
	out, err := q.many(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(ts(last.CreatedAt), last.ID)
	}
	return out, next, nil
}

// ErrBadCursor is returned for a cursor the server did not issue.
var ErrBadCursor = errors.New("bad cursor")

func encodeCursor(created, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(created + "|" + id))
}

func decodeCursor(c string) (string, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", ErrBadCursor
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return "", "", ErrBadCursor
	}
	return parts[0], parts[1], nil
}

// DueResources returns up to limit resources the reconciler should work on at now: in-progress
// phases whose next attempt is due, oldest first.
func (q *Queries) DueResources(ctx context.Context, now time.Time, limit int) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources
		WHERE (phase IN ('pending', 'provisioning', 'updating', 'deleting') OR requested_action != '') AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY COALESCE(next_attempt_at, created_at) LIMIT ?`, ts(now), limit)
}

// ResourcesInPhase returns all resources in phase.
func (q *Queries) ResourcesInPhase(ctx context.Context, phase api.Phase) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE phase = ? ORDER BY created_at, id`, string(phase))
}

// AllResources returns every resource (machine totals, metrics).
func (q *Queries) AllResources(ctx context.Context) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources ORDER BY created_at, id`)
}

// ResourcesInProject returns every resource of a project, oldest first.
func (q *Queries) ResourcesInProject(ctx context.Context, project string) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE project = ? ORDER BY created_at, id`, project)
}

// ProjectResourcesOfKind returns every resource of one kind in a project, oldest first.
func (q *Queries) ProjectResourcesOfKind(ctx context.Context, project string, kind api.Kind) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE project = ? AND kind = ? ORDER BY created_at, id`, project, string(kind))
}

// ResourcesOfKind returns every resource of one kind, oldest first.
func (q *Queries) ResourcesOfKind(ctx context.Context, kind api.Kind) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE kind = ? ORDER BY created_at, id`, string(kind))
}

// OwnedBy returns resources whose owner is ownerID.
func (q *Queries) OwnedBy(ctx context.Context, ownerID string) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+resourceCols+` FROM resources WHERE owner_id = ? ORDER BY created_at, id`, ownerID)
}

func (q *Queries) many(ctx context.Context, query string, args ...any) ([]*domain.Resource, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Resource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanResource(r scanner) (*domain.Resource, error) {
	var res domain.Resource
	var kind, phase, spec, steps, outputs, transition, created, updated string
	var owner, next, nextRun sql.NullString
	err := r.Scan(&res.ID, &kind, &res.Name, &spec, &res.SecretCiphertext, &res.Generation, &res.ObservedGeneration, &phase, &res.Message,
		&steps, &outputs, &owner, &res.Attempts, &res.RequestedAction, &next, &transition, &created, &updated, &res.ServingRevision, &nextRun, &res.ConsecutiveFailures,
		&res.Project)
	if err != nil {
		return nil, err
	}
	res.Kind, res.Phase, res.Spec = api.Kind(kind), api.Phase(phase), json.RawMessage(spec)
	if err := json.Unmarshal([]byte(steps), &res.Steps); err != nil {
		return nil, fmt.Errorf("resource %s steps: %w", res.ID, err)
	}
	if err := json.Unmarshal([]byte(outputs), &res.Outputs); err != nil {
		return nil, fmt.Errorf("resource %s outputs: %w", res.ID, err)
	}
	if owner.Valid {
		res.OwnerID = &owner.String
	}
	res.NextAttemptAt, res.NextRunAt = parseNullTS(next), parseNullTS(nextRun)
	res.LastTransitionAt, res.CreatedAt, res.UpdatedAt = parseTS(transition), parseTS(created), parseTS(updated)
	return &res, nil
}

func encodeStatus(r *domain.Resource) (string, string, error) {
	steps := r.Steps
	if steps == nil {
		steps = []api.Step{}
	}
	outputs := r.Outputs
	if outputs == nil {
		outputs = map[string]any{}
	}
	sb, err := json.Marshal(steps)
	if err != nil {
		return "", "", err
	}
	ob, err := json.Marshal(outputs)
	if err != nil {
		return "", "", err
	}
	return string(sb), string(ob), nil
}

// Ref is a dependency from one resource to another (app → secret).
type Ref struct {
	ToID  string
	Field string
}

// SetRefs replaces fromID's outgoing references.
// DropRef removes one of a resource's references, by field: a VM that no longer needs its snapshot
// (L2-007 SNP-06).
func (q *Queries) DropRef(ctx context.Context, fromID, field string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM resource_refs WHERE from_id = ? AND field = ?`, fromID, field)
	return err
}

func (q *Queries) SetRefs(ctx context.Context, fromID string, refs []Ref) error {
	if _, err := q.q.ExecContext(ctx, `DELETE FROM resource_refs WHERE from_id = ?`, fromID); err != nil {
		return err
	}
	for _, r := range refs {
		if _, err := q.q.ExecContext(ctx, `INSERT OR IGNORE INTO resource_refs (from_id, to_id, field) VALUES (?, ?, ?)`, fromID, r.ToID, r.Field); err != nil {
			return err
		}
	}
	return nil
}

// Dependants returns the resources that reference toID.
func (q *Queries) Dependants(ctx context.Context, toID string) ([]*domain.Resource, error) {
	return q.many(ctx, `SELECT `+prefixed("r", resourceCols)+` FROM resources r WHERE r.id IN (SELECT from_id FROM resource_refs WHERE to_id = ?) ORDER BY r.created_at`, toID)
}

func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// AddEvent appends an event and keeps at most 500 per resource (L3-005 §4).
func (q *Queries) AddEvent(ctx context.Context, resourceID, typ, message string, at time.Time) error {
	if _, err := q.q.ExecContext(ctx, `INSERT INTO resource_events (resource_id, type, message, created_at) VALUES (?, ?, ?, ?)`, resourceID, typ, message, ts(at)); err != nil {
		return err
	}
	_, err := q.q.ExecContext(ctx, `DELETE FROM resource_events WHERE resource_id = ? AND id NOT IN
		(SELECT id FROM resource_events WHERE resource_id = ? ORDER BY id DESC LIMIT 500)`, resourceID, resourceID)
	return err
}

// ListEvents returns a resource's newest events first.
func (q *Queries) ListEvents(ctx context.Context, resourceID string, limit int) ([]api.Event, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT id, type, message, created_at FROM resource_events WHERE resource_id = ? ORDER BY id DESC LIMIT ?`, resourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Event{}
	for rows.Next() {
		var e api.Event
		var created string
		if err := rows.Scan(&e.ID, &e.Type, &e.Message, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Idempotency record.
type Idempotency struct {
	Principal   string
	Key         string
	RequestHash string
	Status      int
	Body        []byte
	CreatedAt   time.Time
}

// GetIdempotency returns a stored response newer than since, or ErrNotFound.
func (q *Queries) GetIdempotency(ctx context.Context, principal, key string, since time.Time) (Idempotency, error) {
	var r Idempotency
	var created string
	err := q.q.QueryRowContext(ctx, `SELECT principal, key, request_hash, status, body, created_at FROM idempotency_keys
		WHERE principal = ? AND key = ? AND created_at > ?`, principal, key, ts(since)).Scan(&r.Principal, &r.Key, &r.RequestHash, &r.Status, &r.Body, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CreatedAt = parseTS(created)
	return r, err
}

// PutIdempotency stores (or replaces an expired) response for a key.
func (q *Queries) PutIdempotency(ctx context.Context, r Idempotency) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO idempotency_keys (principal, key, request_hash, status, body, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(principal, key) DO UPDATE SET request_hash = excluded.request_hash, status = excluded.status, body = excluded.body, created_at = excluded.created_at`,
		r.Principal, r.Key, r.RequestHash, r.Status, r.Body, ts(r.CreatedAt))
	return err
}

// SweepIdempotency deletes records older than before.
func (q *Queries) SweepIdempotency(ctx context.Context, before time.Time) (int64, error) {
	res, err := q.q.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at <= ?`, ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetSecretCiphertext replaces a secret's encrypted value (a synced secret's new value, ADR-041).
func (q *Queries) SetSecretCiphertext(ctx context.Context, id string, ct []byte) error {
	_, err := q.q.ExecContext(ctx, `UPDATE resources SET secret_ciphertext = ? WHERE id = ?`, ct, id)
	return err
}
