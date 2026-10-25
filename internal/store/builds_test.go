package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

func TestBuildsKeepTheNewestTenAndLatestSkipsSkipped(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	app := res(api.KindApp, "api", now)
	if err := s.InsertResource(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestBuild(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no builds yet: %v", err)
	}
	for i := 0; i < 12; i++ {
		b := &Build{ID: fmt.Sprintf("bld_%02d", i), ResourceID: app.ID, Status: BuildRunning, StartedAt: now.Add(time.Duration(i) * time.Second)}
		if err := s.InsertBuild(ctx, b); err != nil {
			t.Fatal(err)
		}
		fin := b.StartedAt.Add(time.Second)
		b.Status, b.Commit, b.Image, b.FinishedAt, b.Log = BuildSucceeded, fmt.Sprintf("%040d", i), "enclave.local/api:r1", &fin, []byte(fmt.Sprintf("log %d\n", i))
		if err := s.FinishBuild(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.BuildCount(ctx, app.ID); n != BuildKeepCount {
		t.Errorf("kept %d builds, want %d", n, BuildKeepCount)
	}
	skipped := &Build{ID: "bld_99", ResourceID: app.ID, Status: BuildSkipped, StartedAt: now.Add(time.Minute), Message: "unchanged"}
	if err := s.InsertBuild(ctx, skipped); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestBuild(ctx, app.ID)
	if err != nil || latest.ID != "bld_11" || string(latest.Log) != "log 11\n" || latest.FinishedAt == nil || latest.Commit != fmt.Sprintf("%040d", 11) {
		t.Fatalf("latest %+v %v", latest, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id = ?`, app.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.BuildCount(ctx, app.ID); n != 0 {
		t.Errorf("builds outlived their app: %d", n)
	}
}
