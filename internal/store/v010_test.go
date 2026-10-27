package store

import (
	"context"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// A function run keeps its event and receipt (L3-005 §12).
func TestFunctionRunKeepsItsEvent(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	fn := res(api.KindFunction, "resize", now)
	if err := s.InsertResource(ctx, fn); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "run_fn1", ResourceID: fn.ID, Trigger: api.TriggerQueue, Status: api.RunRunning, StartedAt: now,
		Event: []byte(`{"source":"queue","queue":"jobs"}`), Receipt: "rcpt-1"}
	if err := s.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, fn.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Event) != string(run.Event) || got.Receipt != "rcpt-1" || got.ToAPI().Event == nil {
		t.Errorf("%s %q", got.Event, got.Receipt)
	}
}

// Promotion swaps the main machine, raises the epoch and forgets standby copies (ADR-057).
func TestPromoteRaisesTheEpoch(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sb := Machine{ID: "mch_standby", Name: "studio", State: api.MachineOnline, StateSince: now, CreatedAt: now, Role: api.MachineStandby, Labels: map[string]string{}}
	if err := s.InsertMachine(ctx, sb); err != nil {
		t.Fatal(err)
	}
	second := Machine{ID: "mch_standby2", Name: "studio2", State: api.MachineWaiting, StateSince: now, CreatedAt: now, Role: api.MachineStandby, Labels: map[string]string{}}
	if err := s.InsertMachine(ctx, second); !isUnique(err) && err != ErrConflict {
		t.Errorf("a second standby: %v", err)
	}
	if e, _ := s.Epoch(ctx); e != 0 {
		t.Fatalf("epoch %d", e)
	}
	if err := s.RecordStandbyCopy(ctx, sb.ID, 0, "refused", now); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetStandbyCopy(ctx, sb.ID); c.LastCopyAt != nil || c.LastError != "refused" || !c.Info(now).Stale {
		t.Errorf("failed copy %+v", c)
	}
	if err := s.RecordStandbyCopy(ctx, sb.ID, 4096, "", now); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetStandbyCopy(ctx, sb.ID); c.LastCopyAt == nil || c.Bytes != 4096 || c.LastError != "" || c.Info(now).Stale {
		t.Errorf("copy %+v", c)
	}
	var epoch int64
	err := s.WithTx(ctx, func(q *Queries) (err error) {
		epoch, err = q.Promote(ctx, sb.ID, now)
		return err
	})
	if err != nil || epoch != 1 {
		t.Fatalf("promote %d %v", epoch, err)
	}
	m, _ := s.GetMachine(ctx, sb.ID)
	old, _ := s.GetMachine(ctx, "mch_testmain0000000")
	if !m.Main || m.Role != api.MachineWorker || old.Main || old.State != api.MachineOffline {
		t.Errorf("after promotion: new %+v old %+v", m, old)
	}
	if _, err := s.GetStandbyCopy(ctx, sb.ID); err != ErrNotFound {
		t.Errorf("copy kept: %v", err)
	}
}

// An event alert reads events after its cursor, of its resources and types only (ADR-060).
func TestEventCursor(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	app, other, alert := res(api.KindApp, "web", now), res(api.KindApp, "api", now), res(api.KindAlert, "deploys", now)
	for _, r := range []*domain.Resource{app, other, alert} {
		if err := s.InsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	start, _ := s.LastEventID(ctx)
	for _, e := range []struct{ id, typ string }{{app.ID, "ready"}, {other.ID, "ready"}, {app.ID, "updated"}, {app.ID, "failed"}} {
		if err := s.AddEvent(ctx, e.id, e.typ, "x", now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.EventsAfter(ctx, start, []string{app.ID}, []string{"ready", "failed"}, 10)
	if err != nil || len(got) != 2 || got[0].Type != "ready" || got[1].Type != "failed" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, ok, _ := s.AlertCursor(ctx, alert.ID); ok {
		t.Error("cursor before one is set")
	}
	if err := s.SetAlertCursor(ctx, alert.ID, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlertCursor(ctx, alert.ID, got[1].ID); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := s.AlertCursor(ctx, alert.ID); !ok || c != got[1].ID {
		t.Errorf("cursor %d", c)
	}
	if rest, _ := s.EventsAfter(ctx, got[1].ID, []string{app.ID}, []string{"ready", "failed"}, 10); len(rest) != 0 {
		t.Errorf("%+v", rest)
	}
}

func TestDatabaseCAIsStoredOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.GetDatabaseCA(ctx); err != ErrNotFound {
		t.Fatal(err)
	}
	ca := DatabaseCA{CertPEM: "pem", KeyCiphertext: []byte{1}, CreatedAt: time.Now()}
	if err := s.PutDatabaseCA(ctx, ca); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDatabaseCA(ctx, ca); err != ErrConflict {
		t.Errorf("second CA: %v", err)
	}
	if got, err := s.GetDatabaseCA(ctx); err != nil || got.CertPEM != "pem" {
		t.Errorf("%+v %v", got, err)
	}
}
