//go:build docker

package docker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Real-engine tests (L5-002 §1): go test -tags docker ./internal/engine/docker/...
// They use unique names and remove everything they create.

// TestMain removes the shared probe helper afterwards when these tests created it, so a run leaves no
// container behind; one that was already there (a real EnclaveStack on this machine) is left alone.
func TestMain(m *testing.M) {
	d, err := New(Options{})
	existed := true
	if err == nil {
		c, _ := d.inspect(context.Background(), engine.ProbeName)
		existed = c != nil
	}
	code := m.Run()
	if err == nil && !existed {
		_ = d.remove(context.Background(), engine.ProbeName)
	}
	os.Exit(code)
}

type steps struct{ seen []string }

func (s *steps) Step(name string, st api.StepState) { s.seen = append(s.seen, name+":"+string(st)) }

func suffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newDriver(t *testing.T) *Driver {
	t.Helper()
	d, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ping(context.Background()); err != nil {
		t.Skipf("Docker is not running: %v", err)
	}
	return d
}

func resource(kind api.Kind, name string) *domain.Resource {
	return &domain.Resource{ID: domain.NewResourceID(kind), Kind: kind, Name: name, Generation: 1}
}

func ensure(t *testing.T, d *Driver, in engine.EnsureInput) engine.Outputs {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s := &steps{}
	out, err := d.Ensure(ctx, in, s)
	if err != nil {
		t.Fatalf("ensure %s: %v (steps %v)", in.Resource.Name, err, s.seen)
	}
	return out
}

func cleanup(t *testing.T, d *Driver, r *domain.Resource, store engine.ObjectStore) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := d.Delete(ctx, r, store); err != nil {
			t.Errorf("cleanup %s: %v", r.Name, err)
		}
	})
}

func containerID(t *testing.T, d *Driver, r *domain.Resource) string {
	t.Helper()
	c, err := d.inspect(context.Background(), engine.ContainerName(r))
	if err != nil || c == nil {
		t.Fatalf("inspect %s: %v", r.Name, err)
	}
	return c.ID
}

func TestDatabaseServesSQLSurvivesRecreateAndDeletesItsData(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	r := resource(api.KindDatabase, "it-db-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	in := engine.EnsureInput{Resource: r, Spec: api.DatabaseSpec{Engine: "postgres", Version: "16"}, Password: "pw-" + suffix()}
	out := ensure(t, d, in)
	port := out["port"].(int)
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", out["username"], in.Password, port, out["database"])
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE kept (v text); INSERT INTO kept VALUES ('survives')`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)

	firstID := containerID(t, d, r)
	ensure(t, d, in) // idempotent: nothing recreated
	if containerID(t, d, r) != firstID {
		t.Error("ensure with the same generation recreated the container")
	}
	r.Generation = 2 // a spec change recreates the container but keeps the volume and the port
	out2 := ensure(t, d, in)
	if containerID(t, d, r) == firstID || out2["port"].(int) != port {
		t.Errorf("recreate: same container=%v port %d→%v", containerID(t, d, r) == firstID, port, out2["port"])
	}
	conn, err = pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	var v string
	if err := conn.QueryRow(ctx, `SELECT v FROM kept`).Scan(&v); err != nil || v != "survives" {
		t.Errorf("data after recreate: %q %v", v, err)
	}
	conn.Close(ctx)
	if h, err := d.Health(ctx, r, engine.ObjectStore{}); err != nil || h != engine.Healthy {
		t.Errorf("health %v %v", h, err)
	}
	logs, err := d.Logs(ctx, r, 20)
	if err != nil || len(logs) == 0 {
		t.Errorf("logs: %d lines, %v", len(logs), err)
	}
	if err := d.Delete(ctx, r, engine.ObjectStore{}); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.Health(ctx, r, engine.ObjectStore{}); h != engine.Missing {
		t.Errorf("after delete health %v", h)
	}
	if _, err := d.cli.VolumeInspect(ctx, engine.VolumeName(r)); err == nil {
		t.Error("volume survived delete")
	}
}

func TestCacheAnswersPingAndDriftIsVisible(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	r := resource(api.KindCache, "it-cache-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	out := ensure(t, d, engine.EnsureInput{Resource: r, Spec: api.CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 64}})
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", out["port"].(int)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("PING\r\n"))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if strings.TrimSpace(line) != "+PONG" {
		t.Errorf("PING answered %q", line)
	}
	timeout := 1
	if err := d.cli.ContainerStop(ctx, engine.ContainerName(r), container.StopOptions{Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.Health(ctx, r, engine.ObjectStore{}); h != engine.Stopped {
		t.Errorf("stopped container reported %v", h)
	}
	ensure(t, d, engine.EnsureInput{Resource: r, Spec: api.CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 64}})
	if h, _ := d.Health(ctx, r, engine.ObjectStore{}); h != engine.Healthy {
		t.Errorf("ensure did not restart a stopped container: %v", h)
	}
}

func TestAppServesHTTPWithItsEnvironmentAndRestarts(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	r := resource(api.KindApp, "it-app-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	spec := api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Publish: true, MemoryMiB: 64}
	out := ensure(t, d, engine.EnsureInput{Resource: r, Spec: spec, Env: map[string]string{"ES_SECRET": "from-secret"}})
	url := fmt.Sprintf("http://127.0.0.1:%d/", out["port"].(int))
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET %s: %v %v", url, err, resp)
	}
	resp.Body.Close()
	names, err := d.containersFor(ctx, r)
	name := ""
	if len(names) == 1 {
		name = names[0]
	}
	if err != nil || name != engine.RevisionName(r, 1) {
		t.Fatalf("serving container %q (%v), want revision 1", name, err)
	}
	c, _ := d.inspect(ctx, name)
	if !containsEnv(c.Config.Env, "ES_SECRET=from-secret") || c.HostConfig.Privileged || c.HostConfig.NetworkMode == "host" {
		t.Errorf("container config: env=%v privileged=%v net=%s", c.Config.Env, c.HostConfig.Privileged, c.HostConfig.NetworkMode)
	}
	if c.Config.Labels[LabelManaged] != "true" || c.Config.Labels[LabelResourceID] != r.ID {
		t.Errorf("labels %v", c.Config.Labels)
	}
	if err := d.Restart(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := d.waitHealthy(ctx, name, false); err != nil {
		t.Fatal(err)
	}
}

func containsEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestCrashingAppReportsItsOutput(t *testing.T) {
	d := newDriver(t)
	r := resource(api.KindApp, "it-crash-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err := d.Ensure(ctx, engine.EnsureInput{Resource: r, Spec: api.AppSpec{Image: "alpine:3.20", Command: []string{"sh", "-c", "echo boom-marker; exit 3"}, MemoryMiB: 64}}, &steps{})
	if err == nil || !strings.Contains(err.Error(), "boom-marker") {
		t.Errorf("crash error should carry the output: %v", err)
	}
}

func TestMissingImageIsPermanent(t *testing.T) {
	d := newDriver(t)
	r := resource(api.KindApp, "it-missing-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	_, err := d.Ensure(context.Background(), engine.EnsureInput{Resource: r, Spec: api.AppSpec{Image: "enclave-test/does-not-exist:nope", MemoryMiB: 64}}, &steps{})
	if err == nil || !engine.IsPermanent(err) {
		t.Errorf("missing image: permanent=%v err=%v", engine.IsPermanent(err), err)
	}
}

func TestBucketStoresObjectsAndDeleteEmptiesIt(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	store := engine.ObjectStore{AccessKey: "enclave-it", SecretKey: "it-secret-" + suffix() + suffix()}
	if c, _ := d.inspect(ctx, engine.ObjectStoreName); c != nil {
		t.Skip("an object store is already running on this machine; not touching it")
	}
	t.Cleanup(func() {
		_ = d.remove(context.Background(), engine.ObjectStoreName)
		_ = d.cli.VolumeRemove(context.Background(), objectStoreVolume, true)
	})
	r := resource(api.KindBucket, "it-bucket-"+suffix())
	out := ensure(t, d, engine.EnsureInput{Resource: r, Spec: api.BucketSpec{Versioning: true}, ObjectStore: store})
	endpoint := strings.TrimPrefix(out["endpoint"].(string), "http://")
	mc, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(store.AccessKey, store.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("hello from enclave")
	if _, err := mc.PutObject(ctx, r.Name, "greeting.txt", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	obj, err := mc.GetObject(ctx, r.Name, "greeting.txt", minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(obj)
	if !bytes.Equal(got, body) {
		t.Errorf("round trip %q", got)
	}
	if v, err := mc.GetBucketVersioning(ctx, r.Name); err != nil || v.Status != "Enabled" {
		t.Errorf("versioning %+v %v", v, err)
	}
	if h, _ := d.Health(ctx, r, store); h != engine.Healthy {
		t.Errorf("bucket health %v", h)
	}
	if err := d.Delete(ctx, r, store); err != nil {
		t.Fatalf("delete non-empty bucket: %v", err)
	}
	if ok, _ := mc.BucketExists(ctx, r.Name); ok {
		t.Error("bucket survived delete")
	}
}
