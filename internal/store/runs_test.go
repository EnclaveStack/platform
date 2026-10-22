package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

func TestRunsFinishOncePageAndAreSwept(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	job := res(api.KindJob, "tick", now)
	if err := s.InsertResource(ctx, job); err != nil {
		t.Fatal(err)
	}
	// 130 old finished runs, 3 recent ones, 1 running.
	for i := 0; i < 133; i++ {
		age := 40 * 24 * time.Hour
		if i >= 130 {
			age = time.Hour
		}
		started := now.Add(-age).Add(time.Duration(i) * time.Second)
		r := &domain.Run{ID: fmt.Sprintf("run_%03d", i), ResourceID: job.ID, Trigger: api.TriggerSchedule, Status: api.RunSucceeded, StartedAt: started, FinishedAt: &started}
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	running := &domain.Run{ID: "run_live", ResourceID: job.ID, Trigger: api.TriggerManual, Status: api.RunRunning, StartedAt: now}
	if err := s.InsertRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	if busy, _ := s.HasRunningRun(ctx, job.ID); !busy {
		t.Error("running run not seen")
	}

	exit := 0
	running.Status, running.FinishedAt, running.ExitCode, running.OutputTail = api.RunSucceeded, &now, &exit, []byte("done\n")
	if ok, err := s.FinishRun(ctx, running); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	running.Status = api.RunFailed
	if ok, _ := s.FinishRun(ctx, running); ok {
		t.Error("a finished run was finished again")
	}
	got, err := s.GetRun(ctx, job.ID, "run_live")
	if err != nil || got.Status != api.RunSucceeded || string(got.OutputTail) != "done\n" || *got.ExitCode != 0 {
		t.Fatalf("run %+v %v", got, err)
	}

	page, next, err := s.ListRuns(ctx, job.ID, 50, "")
	if err != nil || len(page) != 50 || next == "" || page[0].ID != "run_live" {
		t.Fatalf("first page: %d %q %v", len(page), next, err)
	}
	seen := map[string]bool{}
	for cur := ""; ; {
		p, n, err := s.ListRuns(ctx, job.ID, 40, cur)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p {
			if seen[r.ID] {
				t.Fatalf("run %s returned twice", r.ID)
			}
			seen[r.ID] = true
		}
		if n == "" {
			break
		}
		cur = n
	}
	if len(seen) != 134 {
		t.Errorf("paged %d runs, want 134", len(seen))
	}

	if fails, err := s.RecordRunOutcome(ctx, job.ID, true, map[string]any{"lastRunStatus": "failed"}); err != nil || fails != 1 {
		t.Errorf("outcome: %d %v", fails, err)
	}
	if fails, _ := s.RecordRunOutcome(ctx, job.ID, false, map[string]any{"lastRunStatus": "succeeded"}); fails != 0 {
		t.Errorf("success should reset failures: %d", fails)
	}

	n, err := s.SweepRuns(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// Kept: newest 100 by start time (run_live + 3 recent + 96 old); the other 34 old ones go.
	if n != 34 {
		t.Errorf("swept %d, want 34", n)
	}
	if _, err := s.GetRun(ctx, job.ID, "run_live"); err != nil {
		t.Error("newest run swept")
	}
}
