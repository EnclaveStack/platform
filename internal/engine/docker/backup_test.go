//go:build docker

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Backups and restores on real PostgreSQL, Redis and the object store (ADR-018, L5-002 §6).

func objectStoreTarget(t *testing.T, d *Driver) backup.Target {
	t.Helper()
	ctx := context.Background()
	if c, _ := d.inspect(ctx, engine.ObjectStoreName); c != nil {
		t.Skip("an object store is already running on this machine; not touching it")
	}
	creds := engine.ObjectStore{AccessKey: "enclave-it", SecretKey: "it-secret-" + suffix() + suffix()}
	t.Cleanup(func() {
		_ = d.remove(context.Background(), engine.ObjectStoreName)
		_ = d.cli.VolumeRemove(context.Background(), objectStoreVolume, true)
	})
	b := resource(api.KindBucket, "it-backups-"+suffix())
	out := ensure(t, d, engine.EnsureInput{Resource: b, Spec: api.BucketSpec{}, ObjectStore: creds})
	return backup.Target{Endpoint: out["endpoint"].(string), Bucket: b.Name, AccessKey: creds.AccessKey, SecretKey: creds.SecretKey}
}

// dumpTo streams a dump into the store the way the reconciler does.
func dumpTo(t *testing.T, d *Driver, st backup.Store, in engine.EnsureInput, key string) backup.Object {
	t.Helper()
	ctx := context.Background()
	pr, pw := io.Pipe()
	go func() {
		_, err := d.Dump(ctx, in, pw)
		_ = pw.CloseWithError(err)
	}()
	obj, err := st.Put(ctx, key, pr, map[string]string{"enclave-run": "it"})
	if err != nil {
		t.Fatalf("dump and upload: %v", err)
	}
	return obj
}

func restoreFrom(t *testing.T, d *Driver, st backup.Store, in engine.EnsureInput, obj backup.Object) {
	t.Helper()
	ctx := context.Background()
	r, err := st.Get(ctx, obj.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pr, pw := io.Pipe()
	go func() {
		_, err := backup.VerifyingCopy(pw, r, obj.SHA256)
		_ = pw.CloseWithError(err)
	}()
	if err := d.Restore(ctx, in, pr); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

func TestPostgresBackupStreamsToTheObjectStoreAndRestoresIntoANewDatabase(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	st, err := backup.S3Opener{}.Open(ctx, objectStoreTarget(t, d))
	if err != nil {
		t.Fatal(err)
	}
	src := resource(api.KindDatabase, "it-src-"+suffix())
	cleanup(t, d, src, engine.ObjectStore{})
	srcIn := engine.EnsureInput{Resource: src, Spec: api.DatabaseSpec{Engine: "postgres", Version: "16"}, Password: "pw-" + suffix()}
	out := ensure(t, d, srcIn)
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", out["username"], srcIn.Password, out["port"], out["database"]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, note text); INSERT INTO orders SELECT g, 'order ' || g FROM generate_series(1, 1000) g`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)

	obj := dumpTo(t, d, st, srcIn, backup.Key("it", src.Name, time.Now(), "run_aaaaaaaaaaaaaaaa", "pgdump"))
	if obj.Size < 1000 || len(obj.SHA256) != 64 {
		t.Fatalf("object %+v", obj)
	}

	dst := resource(api.KindDatabase, "it-dst-"+suffix())
	cleanup(t, d, dst, engine.ObjectStore{})
	dstIn := engine.EnsureInput{Resource: dst, Spec: api.DatabaseSpec{Engine: "postgres", Version: "16"}, Password: "pw2-" + suffix()}
	restoreFrom(t, d, st, dstIn, obj)
	dout := ensure(t, d, dstIn)
	conn, err = pgx.Connect(ctx, fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", dout["username"], dstIn.Password, dout["port"], dout["database"]))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	var note string
	if err := conn.QueryRow(ctx, `SELECT count(*), max(note) FROM orders`).Scan(&n, &note); err != nil || n != 1000 || note != "order 999" {
		t.Fatalf("restored rows: %d %q %v", n, note, err)
	}
	// Restoring again over existing data replaces it (--clean --if-exists).
	if _, err := conn.Exec(ctx, `DELETE FROM orders WHERE id > 10`); err != nil {
		t.Fatal(err)
	}
	restoreFrom(t, d, st, dstIn, obj)
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if n != 1000 {
		t.Errorf("replace restore left %d rows", n)
	}
}

func TestRedisBackupRestoresThroughTheAOF(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	st, err := backup.S3Opener{}.Open(ctx, objectStoreTarget(t, d))
	if err != nil {
		t.Fatal(err)
	}
	r := resource(api.KindCache, "it-cache-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	in := engine.EnsureInput{Resource: r, Spec: api.CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 64}}
	ensure(t, d, in)
	name := engine.ContainerName(r)
	for i := 0; i < 50; i++ {
		if res, err := d.exec(ctx, name, []string{"redis-cli", "SET", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i)}, nil); err != nil || res.ExitCode != 0 {
			t.Fatal(err)
		}
	}
	obj := dumpTo(t, d, st, in, backup.Key("it", r.Name, time.Now(), "run_bbbbbbbbbbbbbbbb", "rdb"))
	if _, err := d.exec(ctx, name, []string{"redis-cli", "FLUSHALL"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.exec(ctx, name, []string{"redis-cli", "SET", "after", "backup"}, nil); err != nil {
		t.Fatal(err)
	}
	restoreFrom(t, d, st, in, obj)
	get := func(k string) string {
		res, _ := d.exec(ctx, name, []string{"redis-cli", "GET", k}, nil)
		return strings.TrimSpace(res.Output)
	}
	if get("k0") != "v0" || get("k49") != "v49" || get("after") != "" {
		t.Fatalf("after restore: k0=%q k49=%q after=%q", get("k0"), get("k49"), get("after"))
	}
	// The data survives a restart: it now lives in the AOF, not just in memory.
	if err := d.Restart(ctx, r); err != nil {
		t.Fatal(err)
	}
	ensure(t, d, in)
	if get("k49") != "v49" {
		t.Errorf("restored data lost on restart: %q", get("k49"))
	}
}

// failingReader fails after n bytes, like a dump that dies halfway.
type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("dump died")
	}
	m := min(len(p), f.n)
	for i := range p[:m] {
		p[i] = 'x'
	}
	f.n -= m
	return m, nil
}

func TestAFailedUploadLeavesNothingBehind(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	tg := objectStoreTarget(t, d)
	st, _ := backup.S3Opener{}.Open(ctx, tg)
	key := backup.Key("it", "broken", time.Now(), "run_cccccccccccccccc", "pgdump")
	if _, err := st.Put(ctx, key, &failingReader{n: 40 << 20}, nil); err == nil { // more than two 16 MiB parts
		t.Fatal("a failing reader produced an object")
	}
	if _, err := st.Get(ctx, key); !errors.Is(err, backup.ErrNotFound) {
		t.Errorf("partial object visible: %v", err)
	}
	mc, _ := minio.NewCore(strings.TrimPrefix(tg.Endpoint, "http://"), &minio.Options{Creds: credentials.NewStaticV4(tg.AccessKey, tg.SecretKey, "")})
	res, err := mc.ListMultipartUploads(ctx, tg.Bucket, "it/", "", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uploads) != 0 {
		t.Errorf("multipart upload left behind: %+v", res.Uploads)
	}
}
