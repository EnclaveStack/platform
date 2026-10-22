package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

func TestSubscriptionCountsAddUpAndPrune(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	topic := res(api.KindTopic, "orders", now)
	if err := s.InsertResource(ctx, topic); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []bool{true, true, false} {
		msg := ""
		if !ok {
			msg = "500 Internal Server Error"
		}
		if err := s.CountDelivery(ctx, topic.ID, "https://hooks.example.com/a", ok, msg, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CountDelivery(ctx, topic.ID, "queue/emails", true, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteDeliveryError(ctx, topic.ID, "queue/emails", "broker busy", now); err != nil {
		t.Fatal(err)
	}
	c, err := s.SubscriptionCounts(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h := c["https://hooks.example.com/a"]; h.Delivered != 2 || h.Failed != 1 || h.LastError != "500 Internal Server Error" {
		t.Errorf("webhook counts: %+v", h)
	}
	if q := c["queue/emails"]; q.Delivered != 1 || q.Failed != 0 || q.LastError != "broker busy" {
		t.Errorf("queue counts: %+v", q)
	}
	if err := s.PruneSubscriptionCounts(ctx, topic.ID, []string{"queue/emails"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.SubscriptionCounts(ctx, topic.ID); len(c) != 1 {
		t.Errorf("prune left %v", c)
	}
	if err := s.DeleteResource(ctx, topic.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.SubscriptionCounts(ctx, topic.ID); len(c) != 0 {
		t.Errorf("counts outlived the topic: %v", c)
	}
}

func TestTranscoderSeenObjectsAndRunKeys(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tc := res(api.KindTranscoder, "videos", now)
	if err := s.InsertResource(ctx, tc); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.HasSeenObjects(ctx, tc.ID); seen {
		t.Error("nothing seen yet")
	}
	run := &domain.Run{ID: "run_t1", ResourceID: tc.ID, Trigger: api.TriggerWatch, Status: api.RunRunning, StartedAt: now, Key: "videos/a.mov"}
	if err := s.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, tc.ID, "videos/old.mov", "e0", "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, tc.ID, "videos/a.mov", "e1", run.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, tc.ID, "videos/a.mov", "e2", run.ID, now); err != nil {
		t.Fatal(err)
	}
	seen, err := s.SeenObjects(ctx, tc.ID)
	if err != nil || len(seen) != 2 || seen["videos/a.mov"] != "e2" {
		t.Errorf("seen: %v %v", seen, err)
	}
	if n, _ := s.CountRunning(ctx, tc.ID); n != 1 {
		t.Errorf("running: %d", n)
	}
	run.Status, run.FinishedAt, run.Outputs = api.RunSucceeded, &now, []string{"media/a/mp3.mp3", "media/a/thumbnail.jpg"}
	if ok, err := s.FinishRun(ctx, run); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	got, err := s.GetRun(ctx, tc.ID, run.ID)
	if err != nil || got.Key != "videos/a.mov" || !slices.Equal(got.Outputs, run.Outputs) || got.Trigger != api.TriggerWatch {
		t.Errorf("run read back: %+v %v", got, err)
	}
	if err := s.ForgetSeen(ctx, tc.ID); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.HasSeenObjects(ctx, tc.ID); seen {
		t.Error("forget kept objects")
	}
}
