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

// 0013 places every v0.7 resource on the main machine, keeping its references and events (ADR-044, PLC-08).
func TestMachinesMigrationPlacesEverythingOnMain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v07.db")
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
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err != nil || v >= 13 {
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
		`INSERT INTO resources (id, project, kind, name, spec_json, generation, phase, last_transition_at, created_at, updated_at) VALUES ('db_1', 'default', 'database', 'orders', '{}', 1, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO resources (id, project, kind, name, spec_json, generation, phase, owner_id, last_transition_at, created_at, updated_at) VALUES ('sec_1', 'default', 'secret', 'orders-password', '{}', 1, 'ready', 'db_1', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO resource_refs VALUES ('db_1', 'sec_1', 'spec.x')`,
		`INSERT INTO resource_events (resource_id, type, message, created_at) VALUES ('db_1', 'created', 'Created.', '` + now + `')`,
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
	got, err := s.GetResource(ctx, "db_1")
	if err != nil || got.Machine != api.MainMachine || got.Stack != "" {
		t.Fatalf("database after migration: %+v %v", got, err)
	}
	main, err := s.GetMachine(ctx, api.MainMachine)
	if err != nil || main.Name != api.MainMachine || main.State != api.MachineOnline || !main.Schedulable {
		t.Fatalf("main machine: %+v %v", main, err)
	}
	var refs, events int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_refs`).Scan(&refs)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM resource_events`).Scan(&events)
	if refs != 1 || events != 1 {
		t.Errorf("refs=%d events=%d", refs, events)
	}
	on, err := s.ResourcesOnMachine(ctx, api.MainMachine)
	if err != nil || len(on) != 2 {
		t.Errorf("resources on main: %d %v", len(on), err)
	}
	if err := s.DeleteMachine(ctx, api.MainMachine); err != ErrConflict {
		t.Errorf("deleting a machine with resources: %v", err)
	}
}

func TestMachinesRoundTripAndJoinCodesAreUnique(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	exp := now.Add(time.Hour)
	m := Machine{ID: "mch_1", Name: "rack-1", State: api.MachineWaiting, StateSince: now, Labels: map[string]string{"disk": "ssd"},
		Schedulable: true, JoinCodeHash: "h1", JoinExpiresAt: &exp, CreatedAt: now}
	if err := s.InsertMachine(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertMachine(ctx, Machine{ID: "mch_2", Name: "rack-1", State: api.MachineWaiting, StateSince: now, CreatedAt: now}); err != ErrConflict {
		t.Errorf("duplicate name: %v", err)
	}
	if err := s.InsertMachine(ctx, Machine{ID: "mch_3", Name: "rack-3", State: api.MachineWaiting, StateSince: now, JoinCodeHash: "h1", CreatedAt: now}); err != ErrConflict {
		t.Errorf("duplicate join code: %v", err)
	}
	got, err := s.GetMachineByJoinCode(ctx, "h1")
	if err != nil || got.Name != "rack-1" || got.Labels["disk"] != "ssd" || got.JoinExpiresAt == nil || !got.JoinExpiresAt.Equal(exp) {
		t.Fatalf("by join code: %+v %v", got, err)
	}
	got.State, got.JoinCodeHash, got.JoinExpiresAt = api.MachineOnline, "", nil
	got.Address, got.AgentPort, got.TunnelPort, got.TunnelPublicKey = "192.168.1.20", 7071, 51820, "pub"
	got.Info = MachineInfo{OS: "linux", Arch: "arm64", CPUs: 4, MemoryTotalBytes: 8 << 30}
	if err := s.SaveMachine(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMachineByJoinCode(ctx, "h1"); err != ErrNotFound {
		t.Errorf("a spent code still finds the machine: %v", err)
	}
	again, _ := s.GetMachineByName(ctx, "rack-1")
	if again.Address != "192.168.1.20" || again.Info.CPUs != 4 || again.State != api.MachineOnline {
		t.Errorf("after save: %+v", again)
	}
	list, _ := s.ListMachines(ctx)
	if len(list) != 2 || list[0].ID != api.MainMachine || list[1].Name != "rack-1" {
		t.Errorf("list: %+v", list)
	}
	if err := s.AddMachineEvent(ctx, "mch_1", "joined", "Joined.", now); err != nil {
		t.Fatal(err)
	}
	if ev, _ := s.ListMachineEvents(ctx, "mch_1", 10); len(ev) != 1 || ev[0].Type != "joined" {
		t.Errorf("events: %+v", ev)
	}
	if err := s.DeleteMachine(ctx, "mch_1"); err != nil {
		t.Fatal(err)
	}
	if ev, _ := s.ListMachineEvents(ctx, "mch_1", 10); len(ev) != 0 {
		t.Errorf("events outlived their machine: %+v", ev)
	}
}

// An address belongs to one machine's pool at a time (ADR-045, XNET-02).
func TestAddressPoolsNeverShareAnAddress(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()
	if err := s.InsertMachine(ctx, Machine{ID: "mch_1", Name: "rack-1", State: api.MachineOnline, StateSince: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PoolAddressTo(ctx, "es-net-shop", "10.0.0.5", api.MainMachine); err != nil {
		t.Fatal(err)
	}
	if err := s.PoolAddressTo(ctx, "es-net-shop", "10.0.0.5", api.MainMachine); err != nil {
		t.Errorf("giving the same machine its own address again: %v", err)
	}
	if err := s.PoolAddressTo(ctx, "es-net-shop", "10.0.0.5", "mch_1"); err != ErrConflict {
		t.Errorf("an address of main given to rack-1: %v", err)
	}
	if err := s.MovePoolAddress(ctx, "es-net-shop", "10.0.0.5", "mch_1"); err != nil {
		t.Fatal(err)
	}
	pool, _ := s.AddressPool(ctx, "es-net-shop")
	if len(pool) != 1 || pool[0].MachineID != "mch_1" {
		t.Errorf("after the move: %+v", pool)
	}
	if err := s.DeleteMachine(ctx, "mch_1"); err != nil {
		t.Fatal(err)
	}
	if pool, _ := s.AllAddressPools(ctx); len(pool) != 0 {
		t.Errorf("a removed machine's pool stayed: %+v", pool)
	}
}

func TestMachineCAIsStoredOnce(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.GetMachineCA(ctx); err != ErrNotFound {
		t.Fatalf("before: %v", err)
	}
	ca := MachineCA{CertPEM: "cert", KeyCiphertext: []byte{1, 2}, CreatedAt: time.Now()}
	if err := s.PutMachineCA(ctx, ca); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMachineCA(ctx, ca); err != ErrConflict {
		t.Errorf("a second CA: %v", err)
	}
	if got, err := s.GetMachineCA(ctx); err != nil || got.CertPEM != "cert" {
		t.Errorf("CA: %+v %v", got, err)
	}
}
