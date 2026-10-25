package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Public access storage (L3-005 §11).

func TestTunnelPortsAreStableAndDistinct(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()
	link, relay, link2 := res(api.KindTunnel, "demo", now), res(api.KindTunnel, "edge", now), res(api.KindTunnel, "other", now)
	if err := s.InsertResource(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertResource(ctx, relay); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertResource(ctx, link2); err != nil {
		t.Fatal(err)
	}
	a, err := s.EnsureTunnelPorts(ctx, link.ID, false)
	if err != nil || a.HTTPPort != TunnelPortBase || a.HTTPSPort != 0 {
		t.Fatalf("first link ports %+v %v", a, err)
	}
	r, err := s.EnsureTunnelPorts(ctx, relay.ID, true)
	if err != nil || r.HTTPPort != RelayPortBase || r.HTTPSPort != RelayPortBase+1 {
		t.Fatalf("relay ports %+v %v", r, err)
	}
	b, err := s.EnsureTunnelPorts(ctx, link2.ID, false)
	if err != nil || b.HTTPPort != TunnelPortBase+1 {
		t.Fatalf("second link ports %+v %v", b, err)
	}
	if again, err := s.EnsureTunnelPorts(ctx, link.ID, false); err != nil || again != a {
		t.Fatalf("ports must stay for the tunnel's life: %+v %v", again, err)
	}
	// Deleting the tunnel frees its ports for the next one.
	if err := s.DeleteResource(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTunnelPorts(ctx, link.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ports kept after delete: %v", err)
	}
	next := res(api.KindTunnel, "next", now)
	if err := s.InsertResource(ctx, next); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.EnsureTunnelPorts(ctx, next.ID, false); p.HTTPPort != TunnelPortBase {
		t.Fatalf("freed port not reused: %+v", p)
	}
}

func TestGateSessions(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateUser(ctx, User{ID: "usr_gate", Email: "dev@example.com", PasswordHash: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	g := GateSession{TokenHash: "hash-1", UserID: "usr_gate", Hostname: "stage.example.com", CreatedAt: now, ExpiresAt: now.Add(12 * time.Hour), CheckedAt: now}
	if err := s.InsertGateSession(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGateSession(ctx, "hash-1")
	if err != nil || got.UserID != g.UserID || got.Hostname != g.Hostname || !got.ExpiresAt.Equal(g.ExpiresAt) {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := s.TouchGateSession(ctx, "hash-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetGateSession(ctx, "hash-1"); !got.CheckedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("checked_at not updated: %+v", got)
	}
	if n, err := s.DeleteExpiredGateSessions(ctx, now.Add(13*time.Hour)); err != nil || n != 1 {
		t.Fatalf("expired sessions removed %d %v", n, err)
	}
	// A user deleted ends every gate session of theirs (GATE-03).
	if err := s.InsertGateSession(ctx, GateSession{TokenHash: "hash-2", UserID: "usr_gate", Hostname: "dev.example.com", CreatedAt: now, ExpiresAt: now.Add(time.Hour), CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, "usr_gate"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGateSession(ctx, "hash-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session outlived its user: %v", err)
	}
}

func TestMachineRoleAndJoinKey(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()
	m := Machine{ID: "mch_edge0000000000", Name: "vps1", State: api.MachineWaiting, StateSince: now, Address: "203.0.113.9", CreatedAt: now,
		Role: api.MachineEdge, JoinKey: []byte("sealed")}
	if err := s.InsertMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMachine(ctx, m.ID)
	if err != nil || !got.IsEdge() || string(got.JoinKey) != "sealed" {
		t.Fatalf("edge machine read back as %+v %v", got, err)
	}
	got.JoinKey = nil
	if err := s.SaveMachine(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetMachine(ctx, m.ID); again.JoinKey != nil || !again.IsEdge() {
		t.Fatalf("join key not cleared or role lost: %+v", again)
	}
	if main, _ := s.GetMachine(ctx, s.MainID()); main.Role != api.MachineWorker {
		t.Fatalf("the main machine is a worker, got %q", main.Role)
	}
}
