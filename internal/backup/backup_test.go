package backup

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestKeyAndRetention(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryOpener()
	tg := Target{Endpoint: "mem://", Bucket: "backups"}
	s, _ := m.Open(ctx, tg)
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	if k := Key("/enclave-backups/", "orders", now, "run_abcdefghijklmnop", "pgdump"); k != "enclave-backups/orders/20261021T030000.000Z-run_abcdefghijklmnop.pgdump" {
		t.Fatalf("key %q", k)
	}
	var keys []string
	for i := 0; i < 9; i++ {
		k := Key("hb", "orders", now.Add(-time.Duration(i)*24*time.Hour), "run_"+strings.Repeat(string(rune('a'+i)), 16), "pgdump")
		keys = append(keys, k)
		if _, err := s.Put(ctx, k, strings.NewReader("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	m.Put(tg, "hb/orders/someone-elses-file.sql", []byte("keep me"))
	m.Put(tg, "hb/orders-old/20200101T000000.000Z-run_aaaaaaaaaaaaaaaa.pgdump", []byte("another source"))

	deleted, err := Retain(ctx, s, "hb", "orders", 7, 0, now, nil)
	if err != nil || len(deleted) != 2 || deleted[0] != keys[7] || deleted[1] != keys[8] {
		t.Fatalf("count retention: %v %v", deleted, err)
	}
	// Two runs in the same second keep their order.
	a, b := Key("hb", "same", now.Add(10*time.Millisecond), "run_zzzzzzzzzzzzzzzz", "rdb"), Key("hb", "same", now.Add(20*time.Millisecond), "run_aaaaaaaaaaaaaaaa", "rdb")
	_, _ = s.Put(ctx, a, strings.NewReader("1"), nil)
	_, _ = s.Put(ctx, b, strings.NewReader("2"), nil)
	if d, _ := Retain(ctx, s, "hb", "same", 1, 0, now, nil); len(d) != 1 || d[0] != a {
		t.Errorf("same-second retention deleted %v, want the older %s", d, a)
	}
	deleted, _ = Retain(ctx, s, "hb", "orders", 7, 3, now, nil)
	if len(deleted) != 3 { // 3, 4, 5 and 6 days old minus... ages 4,5,6 are older than 3 days
		t.Errorf("age retention deleted %v", deleted)
	}
	// The newest is never deleted, even when everything is "too old".
	deleted, _ = Retain(ctx, s, "hb", "orders", 1, 1, now.Add(100*24*time.Hour), nil)
	left := m.Objects(tg)
	if len(deleted) != 3 || !contains(left, keys[0]) || !contains(left, "hb/orders/someone-elses-file.sql") || !contains(left, "hb/orders-old/20200101T000000.000Z-run_aaaaaaaaaaaaaaaa.pgdump") {
		t.Errorf("deleted %v, left %v", deleted, left)
	}
}

func TestRetainOnlyTouchesItsOwnRuns(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryOpener()
	tg := Target{Endpoint: "mem://", Bucket: "shared"}
	s, _ := m.Open(ctx, tg)
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	// Two backups of one database into one bucket and prefix: hourly (keep 2) and daily (keep 7).
	hourly, daily := map[string]bool{}, map[string]bool{}
	for i := 0; i < 4; i++ {
		h := "run_" + strings.Repeat(string(rune('a'+i)), 16)
		d := "run_" + strings.Repeat(string(rune('k'+i)), 16)
		hourly[h], daily[d] = true, true
		_, _ = s.Put(ctx, Key("hb", "orders", now.Add(-time.Duration(i)*time.Hour), h, "pgdump"), strings.NewReader("h"), nil)
		_, _ = s.Put(ctx, Key("hb", "orders", now.Add(-time.Duration(i)*24*time.Hour-time.Minute), d, "pgdump"), strings.NewReader("d"), nil)
	}
	deleted, err := Retain(ctx, s, "hb", "orders", 2, 0, now, func(id string) bool { return hourly[id] })
	if err != nil || len(deleted) != 2 {
		t.Fatalf("deleted %v %v", deleted, err)
	}
	for _, k := range deleted {
		if id := RunIDOf(k); !hourly[id] {
			t.Errorf("hourly retention deleted %s (run %q), which isn't its own", k, id)
		}
	}
	if left := m.Objects(tg); len(left) != 6 {
		t.Errorf("left %d objects, want the daily 4 and the newest 2 hourly: %v", len(left), left)
	}
	if RunIDOf("hb/orders/someone-elses-file.sql") != "" {
		t.Error("RunIDOf matched a foreign key")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestPutHashesAndVerifyingCopyCatchesDamage(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryOpener()
	tg := Target{Endpoint: "mem://", Bucket: "b"}
	s, _ := m.Open(ctx, tg)
	obj, err := s.Put(ctx, "k", strings.NewReader("hello"), nil)
	if err != nil || obj.Size != 5 || obj.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("put %+v %v", obj, err)
	}
	r, _ := s.Get(ctx, "k")
	var buf bytes.Buffer
	if _, err := VerifyingCopy(&buf, r, obj.SHA256); err != nil || buf.String() != "hello" {
		t.Errorf("verified copy: %v %q", err, buf.String())
	}
	m.Corrupt(tg, "k")
	r, _ = s.Get(ctx, "k")
	if _, err := VerifyingCopy(&bytes.Buffer{}, r, obj.SHA256); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("damage not detected: %v", err)
	}
	m.Refuse(tg, "the destination rejected the credentials (AccessDenied)")
	if _, err := s.Put(ctx, "k2", strings.NewReader("x"), nil); err == nil {
		t.Error("refused target accepted a put")
	}
	if _, err := s.Get(ctx, "missing"); err == nil {
		t.Error("missing object")
	}
}
