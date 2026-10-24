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

// An app's own object store user reaches its buckets at their levels and nothing else, and removing it
// revokes its keys (ADR-049, L2-011 BKA-02, BKA-06), on real MinIO.
func TestStoreUserReachesItsGrantsAtTheirLevels(t *testing.T) {
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
	media, invoices := resource(api.KindBucket, "it-media-"+suffix()), resource(api.KindBucket, "it-invoices-"+suffix())
	out := ensure(t, d, engine.EnsureInput{Resource: media, Spec: api.BucketSpec{}, ObjectStore: root})
	ensure(t, d, engine.EnsureInput{Resource: invoices, Spec: api.BucketSpec{}, ObjectStore: root})
	endpoint := strings.TrimPrefix(out["endpoint"].(string), "http://")
	client := func(u engine.ObjectStore) *minio.Client {
		mc, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(u.AccessKey, u.SecretKey, "")})
		if err != nil {
			t.Fatal(err)
		}
		return mc
	}
	body := []byte("photo")
	put := func(mc *minio.Client, bucket, key string) error {
		_, err := mc.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{})
		return err
	}
	if err := put(client(root), media.Name, "seed.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := put(client(root), invoices.Name, "seed.pdf"); err != nil {
		t.Fatal(err)
	}
	// The user names production uses: "esr-" and the resource ID's 16 random characters (20 in all).
	reader := engine.ObjectStore{AccessKey: "esr-" + suffix() + suffix() + suffix() + "ab", SecretKey: "reader-secret-" + suffix() + suffix()}
	writer := engine.ObjectStore{AccessKey: "esr-" + suffix() + suffix() + suffix() + "cd", SecretKey: "writer-secret-" + suffix() + suffix()}
	if err := d.SetStoreUserAccess(ctx, reader, []api.BucketGrant{{Bucket: media.Name, Access: api.BucketRead}}, root); err != nil {
		t.Fatalf("set reader: %v", err)
	}
	if err := d.SetStoreUserAccess(ctx, writer, []api.BucketGrant{{Bucket: media.Name, Access: api.BucketReadWrite}}, root); err != nil {
		t.Fatalf("set writer: %v", err)
	}
	rc, wc := client(reader), client(writer)
	var readErr error
	for range 20 { // a new user may take a moment to be usable
		if _, readErr = rc.StatObject(ctx, media.Name, "seed.jpg", minio.StatObjectOptions{}); readErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if readErr != nil {
		t.Fatalf("the reader reads its bucket: %v", readErr)
	}
	listed := 0
	for o := range rc.ListObjects(ctx, media.Name, minio.ListObjectsOptions{}) {
		if o.Err != nil {
			t.Fatalf("the reader lists its bucket: %v", o.Err)
		}
		listed++
	}
	if listed != 1 {
		t.Errorf("the reader listed %d objects", listed)
	}
	if err := put(rc, media.Name, "new.jpg"); err == nil {
		t.Error("a read-only user wrote")
	}
	if err := rc.RemoveObject(ctx, media.Name, "seed.jpg", minio.RemoveObjectOptions{}); err == nil {
		if _, err := client(root).StatObject(ctx, media.Name, "seed.jpg", minio.StatObjectOptions{}); err != nil {
			t.Error("a read-only user deleted")
		}
	}
	if err := put(wc, media.Name, "new.jpg"); err != nil {
		t.Errorf("the writer writes its bucket: %v", err)
	}
	if err := wc.RemoveObject(ctx, media.Name, "new.jpg", minio.RemoveObjectOptions{}); err != nil {
		t.Errorf("the writer deletes in its bucket: %v", err)
	}
	for name, mc := range map[string]*minio.Client{"reader": rc, "writer": wc} {
		if _, err := mc.StatObject(ctx, invoices.Name, "seed.pdf", minio.StatObjectOptions{}); err == nil {
			t.Errorf("the %s read a bucket it wasn't given", name)
		}
		if err := mc.MakeBucket(ctx, "it-made-"+suffix(), minio.MakeBucketOptions{}); err == nil {
			t.Errorf("the %s made a bucket", name)
		}
		if err := mc.RemoveBucket(ctx, invoices.Name); err == nil {
			t.Errorf("the %s removed a bucket", name)
		}
	}
	// Read to read and write: the same keys now write.
	if err := d.SetStoreUserAccess(ctx, reader, []api.BucketGrant{{Bucket: media.Name, Access: api.BucketReadWrite}}, root); err != nil {
		t.Fatal(err)
	}
	if err := put(rc, media.Name, "later.jpg"); err != nil {
		t.Errorf("after widening to read and write: %v", err)
	}
	// No grants: the user is gone and its keys stop working.
	if err := d.SetStoreUserAccess(ctx, reader, nil, root); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.StatObject(ctx, media.Name, "seed.jpg", minio.StatObjectOptions{}); err == nil {
		t.Error("a removed user still reads")
	}
	if _, err := wc.StatObject(ctx, media.Name, "seed.jpg", minio.StatObjectOptions{}); err != nil {
		t.Errorf("removing one user touched another: %v", err)
	}
}
