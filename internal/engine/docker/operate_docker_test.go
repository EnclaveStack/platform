//go:build docker

package docker

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// A project's object store user reaches its own buckets and no others (ADR-035, PRJ-06), on real MinIO.
func TestProjectStoreUserReachesOnlyItsBuckets(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	root := engine.ObjectStore{AccessKey: "enclave-it", SecretKey: "it-secret-" + suffix() + suffix()}
	if c, _ := d.inspect(ctx, engine.ObjectStoreName); c != nil {
		t.Skip("an object store is already running on this machine; not touching it")
	}
	t.Cleanup(func() {
		_ = d.remove(context.Background(), engine.ObjectStoreName)
		_ = d.cli.VolumeRemove(context.Background(), objectStoreVolume, true)
	})
	mine, other := resource(api.KindBucket, "it-mine-"+suffix()), resource(api.KindBucket, "it-other-"+suffix())
	out := ensure(t, d, engine.EnsureInput{Resource: mine, Spec: api.BucketSpec{}, ObjectStore: root})
	ensure(t, d, engine.EnsureInput{Resource: other, Spec: api.BucketSpec{}, ObjectStore: root})
	user := engine.ObjectStore{AccessKey: "es-p-shop", SecretKey: "shop-secret-" + suffix() + suffix()}
	if err := d.SetProjectStoreAccess(ctx, user, []string{mine.Name}, root); err != nil {
		t.Fatalf("set access: %v", err)
	}
	endpoint := strings.TrimPrefix(out["endpoint"].(string), "http://")
	mc, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(user.AccessKey, user.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("project data")
	var putErr error
	for range 20 { // a new user may take a moment to be usable
		if _, putErr = mc.PutObject(ctx, mine.Name, "a.txt", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{}); putErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if putErr != nil {
		t.Fatalf("the project user writes its bucket: %v", putErr)
	}
	if _, err := mc.PutObject(ctx, other.Name, "a.txt", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{}); err == nil {
		t.Error("the project user wrote another project's bucket")
	}
	// Adding a bucket widens the policy; none left removes the user.
	if err := d.SetProjectStoreAccess(ctx, user, []string{mine.Name, other.Name}, root); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.PutObject(ctx, other.Name, "b.txt", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{}); err != nil {
		t.Errorf("after widening: %v", err)
	}
	if err := d.SetProjectStoreAccess(ctx, user, nil, root); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.StatObject(ctx, mine.Name, "a.txt", minio.StatObjectOptions{}); err == nil {
		t.Error("a removed user still reads")
	}
}

// Stats and logs of a real container: CPU time and memory are read, and LogsSince returns only newer
// lines, per container (ADR-037, ADR-040).
func TestStatsAndLogsSinceOfARunningApp(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	r := resource(api.KindApp, "it-stats-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	ensure(t, d, engine.EnsureInput{Resource: r, Revision: 1, Spec: api.AppSpec{Image: "busybox:1.36", MemoryMiB: 64, Replicas: 1,
		Command: []string{"sh", "-c", "i=0; while true; do i=$((i+1)); echo line $i; sleep 0.2; done"}}})
	r.ServingRevision = 1
	time.Sleep(1500 * time.Millisecond)
	stats, err := d.Stats(ctx, r)
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	if s := stats[0]; s.MemoryBytes <= 0 || s.MemoryLimit != 64<<20 || s.CPUSeconds < 0 || s.ID == "" {
		t.Errorf("stats %+v", s)
	}
	lines, err := d.LogsSince(ctx, r, nil, time.Now().Add(-time.Minute), 1000)
	if err != nil || len(lines) < 3 {
		t.Fatalf("logs %d %v", len(lines), err)
	}
	cut := lines[len(lines)-1]
	time.Sleep(600 * time.Millisecond)
	newer, err := d.LogsSince(ctx, r, map[string]time.Time{cut.Container: cut.Time}, time.Now().Add(-time.Minute), 1000)
	if err != nil || len(newer) == 0 {
		t.Fatalf("newer %d %v", len(newer), err)
	}
	// The checkpoint's own line comes back (the shipper skips it and so sees it is still kept), nothing older.
	if newer[0].Time != cut.Time || newer[0].Message != cut.Message {
		t.Errorf("the checkpoint's line didn't come first: %+v (checkpoint %+v)", newer[0], cut)
	}
	for _, l := range newer[1:] {
		if !l.Time.After(cut.Time) {
			t.Errorf("a line at or before the checkpoint came back: %+v (checkpoint %v)", l, cut.Time)
		}
	}
}
