package server

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cfg(t *testing.T) Config {
	return Config{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Driver: "fake", ShutdownGrace: 2 * time.Second}
}

func TestStartRejectsBadConfiguration(t *testing.T) {
	ctx := context.Background()
	c := cfg(t)
	c.Driver = "kubernetes"
	if _, err := Start(ctx, c, io.Discard); err == nil || !strings.Contains(err.Error(), "unknown driver") {
		t.Errorf("unknown driver: %v", err)
	}
	c = cfg(t)
	c.FakeFaults = "nonsense"
	if _, err := Start(ctx, c, io.Discard); err == nil {
		t.Error("bad fault spec accepted")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	c = cfg(t)
	c.Listen = ln.Addr().String()
	if _, err := Start(ctx, c, io.Discard); err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Errorf("port in use: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	c = cfg(t)
	c.DataDir = filepath.Join(blocker, "data")
	if _, err := Start(ctx, c, io.Discard); err == nil {
		t.Error("unusable data directory accepted")
	}
}

func TestStartCreatesPrivateDataDirAndStopsCleanly(t *testing.T) {
	c := cfg(t)
	c.DataDir = filepath.Join(c.DataDir, "nested", "hc")
	r, err := Start(context.Background(), c, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if r.SetupCode == "" || !strings.HasPrefix(r.URL, "http://127.0.0.1:") {
		t.Errorf("running %+v", r)
	}
	for name, mode := range map[string]os.FileMode{"": 0o700, "master.key": 0o600} {
		st, err := os.Stat(filepath.Join(c.DataDir, name))
		if err != nil || st.Mode().Perm() != mode {
			t.Errorf("%q mode %v (%v), want %v", name, st.Mode().Perm(), err, mode)
		}
	}
	if err := r.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
	select {
	case <-r.Done():
	default:
		t.Error("Done not closed after Stop")
	}
	if err := r.Stop(); err != nil {
		t.Errorf("second stop: %v", err)
	}
	if DefaultDataDir() == "" {
		t.Error("no default data dir")
	}
}
