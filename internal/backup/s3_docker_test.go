//go:build docker

package backup_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine/docker"
)

// The S3 store against a real MinIO of its own (a container named for this test on a random port, so an
// EnclaveStack already running on this Docker is never touched): tags and media types round-trip, and
// Browse lists one folder at a time in pages (L2-013 OBJ-01 – OBJ-03, L2-012 TRX-06).
func TestS3StoreHeadAndBrowseOnRealMinIO(t *testing.T) {
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "es-it-minio-" + hex.EncodeToString(b)
	user, pass := "it-user", "it-pass-"+hex.EncodeToString(b)
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::9000", "-e", "MINIO_ROOT_USER="+user, "-e", "MINIO_ROOT_PASSWORD="+pass,
		docker.DefaultObjectStoreImage, "server", "/data").CombinedOutput(); err != nil {
		t.Fatalf("start minio: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", name).Run() })
	port, err := exec.Command("docker", "port", name, "9000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	mc, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(user, pass, "")})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for err = mc.MakeBucket(ctx, "media", minio.MakeBucketOptions{}); err != nil; err = mc.MakeBucket(ctx, "media", minio.MakeBucketOptions{}) {
		if time.Now().After(deadline) {
			t.Fatalf("minio didn't start: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	st, err := backup.S3Opener{}.Open(ctx, backup.Target{Endpoint: "http://" + endpoint, Bucket: "media", AccessKey: user, SecretKey: pass})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"readme.txt", "incoming/a.mp4", "incoming/b.m3u8", "incoming/sub/c.jpg", "other/d.bin"} {
		if _, err := st.Put(ctx, k, strings.NewReader(k), map[string]string{"video-id": "v42"}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	// An uploader's own casing comes back lowercase; the media type comes from the key.
	if _, err := mc.PutObject(ctx, "media", "incoming/up.mov", strings.NewReader("x"), 1, minio.PutObjectOptions{UserMetadata: map[string]string{"Video-ID": "v7"}}); err != nil {
		t.Fatal(err)
	}
	h, err := st.Head(ctx, "incoming/up.mov")
	if err != nil || h.Meta["video-id"] != "v7" || h.Size != 1 {
		t.Errorf("head of an upload: %+v %v", h, err)
	}
	for k, want := range map[string]string{"incoming/a.mp4": "video/mp4", "incoming/b.m3u8": "application/vnd.apple.mpegurl", "incoming/sub/c.jpg": "image/jpeg"} {
		if h, err := st.Head(ctx, k); err != nil || h.ContentType != want || h.Meta["video-id"] != "v42" {
			t.Errorf("%s: %+v %v", k, h, err)
		}
	}
	if _, err := st.Head(ctx, "missing"); err != backup.ErrNotFound {
		t.Errorf("a missing object: %v", err)
	}
	// One folder: subfolders and objects, in key order; paging resumes after the last key.
	folders, objects, more, err := st.Browse(ctx, "", "", 10)
	if err != nil || fmt.Sprint(folders) != "[incoming/ other/]" || len(objects) != 1 || objects[0].Key != "readme.txt" || more {
		t.Errorf("top: %v %v %v %v", folders, objects, more, err)
	}
	var seen []string
	after := ""
	for range 10 {
		f, o, more, err := st.Browse(ctx, "incoming/", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, f...)
		for _, x := range o {
			seen = append(seen, x.Key)
		}
		if !more {
			break
		}
		after = seen[len(seen)-1]
	}
	if fmt.Sprint(seen) != "[incoming/a.mp4 incoming/b.m3u8 incoming/sub/ incoming/up.mov]" {
		t.Errorf("paged: %v", seen)
	}
	r, err := st.Get(ctx, "incoming/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "incoming/a.mp4" {
		t.Errorf("get: %q", got)
	}
}
