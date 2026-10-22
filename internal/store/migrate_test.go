package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// A table rebuild (0003) must keep every row, event and reference of an existing database, and leave
// foreign keys enforced (L3-005 §5).
func TestRebuildMigrationKeepsDataAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, f := range []string{"0001_init.sql", "0002_app_revisions.sql"} { // in order: a map would shuffle them
		v := i + 1
		body, err := migrationFS.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, ?)`, v, ts(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	now := ts(time.Now())
	for _, row := range [][]any{{"app_1", "app", "web", 7}, {"sec_1", "secret", "key", 0}} {
		if _, err := db.Exec(`INSERT INTO resources (id, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at, serving_revision)
			VALUES (?, ?, ?, '{}', 1, 'ready', ?, ?, ?, ?)`, row[0], row[1], row[2], now, now, now, row[3]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO resource_refs VALUES ('app_1', 'sec_1', 'spec.secretEnv.K')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_events (resource_id, type, message, created_at) VALUES ('app_1', 'created', 'Created.', ?)`, now); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer s.Close()
	var resources, refs, events int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resources`).Scan(&resources)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_refs`).Scan(&refs)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_events`).Scan(&events)
	if resources != 2 || refs != 1 || events != 1 {
		t.Fatalf("rows lost in the rebuild: resources=%d refs=%d events=%d", resources, refs, events)
	}
	app, err := s.GetResource(ctx, "app_1")
	if err != nil || app.ServingRevision != 7 {
		t.Errorf("serving_revision lost: %+v %v", app, err)
	}
	if err := s.DeleteResource(ctx, "sec_1"); err == nil {
		t.Error("foreign keys not enforced after the rebuild: a referenced secret was deleted")
	}
	if err := s.InsertResource(ctx, res(api.KindDomain, "web-local", time.Now())); err != nil {
		t.Errorf("domain kind still rejected: %v", err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d (%v)", fk, err)
	}
}

// Stored times must sort as text exactly as they sort as times (L3-005 §1).
func TestTimestampsSortAsTextLikeTimes(t *testing.T) {
	base := time.Date(2026, 10, 21, 3, 54, 40, 0, time.UTC)
	nanos := []int{0, 1, 100_000_000, 100_182_000, 200_284_000, 999_999_999, 120_000_000}
	for _, a := range nanos {
		for _, b := range nanos {
			ta, tb := base.Add(time.Duration(a)), base.Add(time.Duration(b))
			if (ts(ta) < ts(tb)) != ta.Before(tb) {
				t.Errorf("%s vs %s sorts wrong", ts(ta), ts(tb))
			}
			if !parseTS(ts(ta)).Equal(ta) {
				t.Errorf("round trip %s", ts(ta))
			}
		}
	}
	if len(ts(base)) != 30 {
		t.Errorf("width %d", len(ts(base)))
	}
}

// 0005 rewrites older variable-width values to the fixed width without changing the instant.
func TestMigrationNormalizesOldTimestamps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, f := range []string{"0001_init.sql", "0002_app_revisions.sql"} {
		body, _ := migrationFS.ReadFile("migrations/" + f)
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, '2026-10-20T10:00:00Z')`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	old := map[string]string{"a": "2026-10-21T03:54:40Z", "b": "2026-10-21T03:54:40.1Z", "c": "2026-10-21T03:54:40.100182Z"}
	for id, v := range old {
		if _, err := db.Exec(`INSERT INTO resources (id, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at)
			VALUES (?, 'app', ?, '{}', 1, 'ready', ?, ?, ?)`, id, "app-"+id, v, v, v); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.Query(`SELECT id, created_at FROM resources ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var order []string
	for rows.Next() {
		var id, v string
		_ = rows.Scan(&id, &v)
		order = append(order, id)
		want, _ := time.Parse(time.RFC3339Nano, old[id])
		if len(v) != 30 || !parseTS(v).Equal(want) {
			t.Errorf("%s: %q is not the same instant in the fixed width", id, v)
		}
	}
	// As text, the old values sorted [a c b] ("…40.1Z" after "…40.100182Z"); by time they are [a b c].
	if fmt.Sprint(order) != "[a b c]" {
		t.Errorf("order after migration %v, want [a b c]", order)
	}
}

// A later rebuild (0006) keeps runs, which reference resources by name across the rename.
func TestRebuildKeepsRunsAndTheirCascade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	db, err := sql.Open("sqlite", "file:"+path) // foreign keys off, as the rebuild runner applies them
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, f := range []string{"0001_init.sql", "0002_app_revisions.sql", "0003_domain_kind.sql", "0004_jobs.sql", "0005_fixed_width_timestamps.sql"} {
		body, _ := migrationFS.ReadFile("migrations/" + f)
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, ?)`, i+1, ts(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	now := ts(time.Now())
	if _, err := db.Exec(`INSERT INTO resources (id, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at, next_run_at)
		VALUES ('job_1', 'job', 'tick', '{}', 1, 'ready', ?, ?, ?, ?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs (id, resource_id, trigger, status, started_at) VALUES ('run_1', 'job_1', 'manual', 'succeeded', ?)`, now); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer s.Close()
	if _, err := s.GetRun(ctx, "job_1", "run_1"); err != nil {
		t.Fatalf("run lost in the rebuild: %v", err)
	}
	job, err := s.GetResource(ctx, "job_1")
	if err != nil || job.NextRunAt == nil {
		t.Fatalf("next_run_at lost: %+v %v", job, err)
	}
	if err := s.InsertResource(ctx, res(api.KindBackup, "nightly", time.Now())); err != nil {
		t.Errorf("backup kind rejected: %v", err)
	}
	if err := s.DeleteResource(ctx, "job_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, "job_1", "run_1"); err == nil {
		t.Error("runs no longer cascade with their resource after the rebuild")
	}
}

// 0012 moves every v0.6 resource into default with its references, events and runs, makes the admin a
// machine admin, and turns read tokens into viewers and write tokens into admins (ADR-035, ADR-036).
func TestOperateMigrationPutsEverythingInDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v06.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err != nil || v >= 12 {
			continue
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (?, ?)`, v, ts(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	now := ts(time.Now())
	for _, q := range []string{
		`INSERT INTO users (id, email, password_hash, created_at) VALUES ('usr_1', 'a@b.test', 'h', '` + now + `')`,
		`INSERT INTO api_tokens (id, user_id, name, prefix, secret_hash, scope, created_at) VALUES ('tok_r', 'usr_1', 'r', 'hc_pat_r', 'hr', 'read', '` + now + `')`,
		`INSERT INTO api_tokens (id, user_id, name, prefix, secret_hash, scope, created_at) VALUES ('tok_w', 'usr_1', 'w', 'hc_pat_w', 'hw', 'write', '` + now + `')`,
		`INSERT INTO resources (id, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at) VALUES ('app_1', 'app', 'web', '{}', 1, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO resources (id, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at) VALUES ('sec_1', 'secret', 'key', '{}', 1, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO resource_refs VALUES ('app_1', 'sec_1', 'spec.secretEnv.K')`,
		`INSERT INTO resource_events (resource_id, type, message, created_at) VALUES ('app_1', 'created', 'Created.', '` + now + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer s.Close()
	app, err := s.GetResourceByName(ctx, api.DefaultProject, api.KindApp, "web")
	if err != nil || app.Project != api.DefaultProject {
		t.Fatalf("app after migration: %+v %v", app, err)
	}
	var refs, events int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_refs`).Scan(&refs)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_events`).Scan(&events)
	if refs != 1 || events != 1 {
		t.Errorf("refs=%d events=%d", refs, events)
	}
	if u, err := s.UserByID(ctx, "usr_1"); err != nil || u.MachineRole != api.RoleAdmin {
		t.Errorf("admin after migration: %+v %v", u, err)
	}
	for hash, want := range map[string]string{"hr": api.RoleViewer, "hw": api.RoleAdmin} {
		if tok, err := s.TokenByHash(ctx, hash); err != nil || tok.Role != want || tok.Projects != nil {
			t.Errorf("token %s: %+v %v (want %s on all projects)", hash, tok, err, want)
		}
	}
	// The same name may now exist in another project, but not twice in one.
	if err := s.CreateProject(ctx, Project{Name: "shop", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := res(api.KindApp, "web", time.Now())
	r.Project = "shop"
	if err := s.InsertResource(ctx, r); err != nil {
		t.Errorf("web in shop: %v", err)
	}
	r2 := res(api.KindApp, "web", time.Now())
	if err := s.InsertResource(ctx, r2); err != ErrConflict {
		t.Errorf("a second web in default: %v", err)
	}
	if err := s.DeleteProject(ctx, "shop"); err != ErrConflict {
		t.Errorf("deleting a project with resources: %v", err)
	}
}
