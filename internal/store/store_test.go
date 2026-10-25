package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "hc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.EnsureMainMachine(context.Background(), func() string { return "mch_testmain0000000" }, "test-computer", time.Now()); err != nil {
		t.Fatal(err)
	}
	return s
}

func res(kind api.Kind, name string, created time.Time) *domain.Resource {
	return &domain.Resource{ID: domain.NewResourceID(kind), Kind: kind, Name: name, Spec: json.RawMessage(`{}`), Generation: 1,
		Phase: api.PhasePending, LastTransitionAt: created, CreatedAt: created, UpdatedAt: created}
}

func TestMigrationsAreIdempotentAndRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hc.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	files, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(files) {
		t.Fatalf("migrations recorded %d times, want once per file (%d) (%v)", n, len(files), err)
	}
	var mode string
	_ = s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	var fk int
	_ = s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Errorf("journal_mode=%s foreign_keys=%d, want wal and 1 (ADR-005)", mode, fk)
	}
}

func TestCheckConstraintsMirrorTheContract(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	for _, k := range api.Kinds {
		if err := s.InsertResource(ctx, res(k, "ok-"+string(k), now)); err != nil {
			t.Errorf("kind %s rejected by schema: %v", k, err)
		}
	}
	bad := res("lambda", "nope", now)
	if err := s.InsertResource(ctx, bad); err == nil {
		t.Error("schema accepted an unknown kind")
	} else if errors.Is(err, ErrConflict) {
		t.Error("a CHECK violation must not look like a name conflict (it is a bug, not the caller's fault)")
	}
	for _, p := range api.Phases {
		r := res(api.KindApp, "phase-"+string(p), now)
		r.Phase = p
		if err := s.InsertResource(ctx, r); err != nil {
			t.Errorf("phase %s rejected by schema: %v", p, err)
		}
	}
}

func TestUniqueNamePerKind(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.InsertResource(ctx, res(api.KindApp, "web", now)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertResource(ctx, res(api.KindApp, "web", now)); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if err := s.InsertResource(ctx, res(api.KindCache, "web", now)); err != nil {
		t.Errorf("same name in another kind should be fine: %v", err)
	}
}

func TestListPaginatesWithCursor(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	base := time.Now()
	for i := 0; i < 5; i++ {
		if err := s.InsertResource(ctx, res(api.KindSecret, "s"+strings.Repeat("x", i+2), base.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	cursor := ""
	pages := 0
	for {
		page, next, err := s.ListResources(ctx, ListFilter{Kind: api.KindSecret, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page {
			names = append(names, r.Name)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if pages != 3 || len(names) != 5 || names[0] != "sxx" || names[4] != "sxxxxxx" {
		t.Errorf("pages=%d names=%v", pages, names)
	}
	if _, _, err := s.ListResources(ctx, ListFilter{Limit: 2, Cursor: "!!!"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestRefsRestrictDeletingAReferencedResource(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	sec, app := res(api.KindSecret, "key", now), res(api.KindApp, "web", now)
	_ = s.InsertResource(ctx, sec)
	_ = s.InsertResource(ctx, app)
	if err := s.SetRefs(ctx, app.ID, []Ref{{ToID: sec.ID, Field: "spec.secretEnv.K"}}); err != nil {
		t.Fatal(err)
	}
	deps, err := s.Dependants(ctx, sec.ID)
	if err != nil || len(deps) != 1 || deps[0].ID != app.ID {
		t.Fatalf("dependants %v %v", deps, err)
	}
	if err := s.DeleteResource(ctx, sec.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("deleting a referenced secret: %v", err)
	}
	if err := s.DeleteResource(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteResource(ctx, sec.ID); err != nil {
		t.Errorf("secret still blocked after its user was deleted: %v", err)
	}
}

func TestSaveStatusOnlyWhenGenerationMatches(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	r := res(api.KindApp, "web", time.Now())
	_ = s.InsertResource(ctx, r)
	r.Phase = api.PhaseReady
	if ok, err := s.SaveStatus(ctx, r, 1); err != nil || !ok {
		t.Fatalf("matching generation: %v %v", ok, err)
	}
	if ok, _ := s.SaveStatus(ctx, r, 2); ok {
		t.Error("stale generation overwrote status")
	}
}

// A status writer that read the resource before it was deleted can't bring it back: the deletion would be
// lost and the resource never removed.
func TestSaveStatusNeverUndoesADeletion(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	r := res(api.KindDomain, "web", time.Now())
	_ = s.InsertResource(ctx, r)
	stale := *r
	stale.Phase = api.PhaseReady
	r.Phase = api.PhaseDeleting
	if err := s.SaveResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	stale.Phase = api.PhaseProvisioning
	if ok, err := s.SaveStatus(ctx, &stale, stale.Generation); err != nil || ok {
		t.Fatalf("a stale writer moved a deleting resource: %v %v", ok, err)
	}
	if got, _ := s.GetResource(ctx, r.ID); got.Phase != api.PhaseDeleting {
		t.Errorf("phase: %s", got.Phase)
	}
	r.Message = "removing the route"
	if ok, err := s.SaveStatus(ctx, r, r.Generation); err != nil || !ok {
		t.Errorf("the deletion's own progress: %v %v", ok, err)
	}
}

func TestDueResourcesHonoursNextAttempt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	soon, later := res(api.KindApp, "soon", now), res(api.KindApp, "later", now)
	future := now.Add(time.Hour)
	later.NextAttemptAt = &future
	ready := res(api.KindApp, "done", now)
	ready.Phase = api.PhaseReady
	action := res(api.KindApp, "restart-me", now)
	action.Phase, action.RequestedAction = api.PhaseReady, api.ActionRestart
	for _, r := range []*domain.Resource{soon, later, ready, action} {
		if err := s.InsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueResources(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range due {
		got[d.Name] = true
	}
	if !got["soon"] || !got["restart-me"] || got["later"] || got["done"] {
		t.Errorf("due = %v", got)
	}
}

func TestEventsAreCappedAt500(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	r := res(api.KindApp, "web", time.Now())
	_ = s.InsertResource(ctx, r)
	for i := 0; i < 510; i++ {
		if err := s.AddEvent(ctx, r.ID, "tick", "x", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.ListEvents(ctx, r.ID, 1000)
	if err != nil || len(evs) != 500 {
		t.Fatalf("kept %d events (%v)", len(evs), err)
	}
	if evs[0].ID < evs[1].ID {
		t.Error("events not newest first")
	}
}

func TestIdempotencyRecordsExpire(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	_ = s.PutIdempotency(ctx, Idempotency{Principal: "p", Key: "k", RequestHash: "h", Status: 202, Body: []byte("{}"), CreatedAt: old})
	if _, err := s.GetIdempotency(ctx, "p", "k", time.Now().Add(-24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired record returned: %v", err)
	}
	if n, err := s.SweepIdempotency(ctx, time.Now().Add(-24*time.Hour)); err != nil || n != 1 {
		t.Errorf("sweep removed %d (%v)", n, err)
	}
}

func TestTransactionsRollBack(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(q *Queries) error {
		if err := q.InsertResource(ctx, res(api.KindApp, "ghost", time.Now())); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := s.GetResourceByName(ctx, api.DefaultProject, api.KindApp, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rolled-back row exists: %v", err)
	}
}
