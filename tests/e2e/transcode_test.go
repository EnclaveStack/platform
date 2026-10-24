//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/pkg/api"
)

// L2-012 for real (§5): the real server, Docker, object store and FFmpeg. A tagged portrait upload becomes
// an HLS ladder and a rendition in the folder its tag names, and a signed webhook says so. The CLI makes
// the transcoder, as a user would.
func TestTranscoderLadderAndWebhookForReal(t *testing.T) {
	requireTools(t, "docker")
	if exec.Command("docker", "inspect", "es-system-objectstore").Run() == nil {
		t.Skip("an object store is already running on this Docker; this test would replace its keys")
	}
	var mu sync.Mutex
	var bodies [][]byte
	var sigs []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies, sigs = append(bodies, b), append(sigs, r.Header.Get("X-Enclave-Signature"))
		mu.Unlock()
	}))
	defer hook.Close()

	s := startServer(t, t.TempDir(), freePort(t))
	token, _ := setupAndToken(t, s)
	ctx := context.Background()
	id := suffix()
	media, tc := "e2e-video-"+id, "e2e-tc-"+id
	t.Cleanup(func() {
		for _, kr := range [][2]string{{"transcoder", tc}, {"bucket", media}, {"secret", "e2e-hook-" + id}} {
			cli(t, s, token, "", "delete", kr[0], kr[1], "--yes", "--wait", "--timeout", "3m")
		}
		_ = exec.Command("docker", "rm", "-f", "es-system-objectstore").Run()
		_ = exec.Command("docker", "volume", "rm", "-f", "es-system-objectstore-data").Run()
	})
	mustCLI(t, s, token, "", "bucket", "create", media, "--wait", "--timeout", "5m")
	mustCLI(t, s, token, "", "secret", "create", "e2e-hook-"+id, "--value", "s3cret")
	mustCLI(t, s, token, "", "transcoder", "create", tc, "--input", media+"/incoming/", "--output", media+"/processed/{meta:video-id}/",
		"--hls-rung", "360p:360x640:600", "--hls-rung", "720p:720x1280:2000", "--rendition", "selfie:mp4:720x1280:2500",
		"--notify-webhook", hook.URL+"/done", "--notify-secret", "e2e-hook-"+id, "--wait", "--timeout", "5m")
	time.Sleep(15 * time.Second) // the first listing is the baseline (TRN-03)

	// A 9:16 clip with sound, uploaded with a tag as an app would (x-amz-meta-video-id).
	img := "linuxserver/ffmpeg:8.0.1@sha256:c72eaf699cb104e84c5292ee9b2a65bf3110738dec113d7cfcef02d7bc21b056"
	clip, err := exec.Command("docker", "run", "--rm", "--entrypoint", "ffmpeg", img, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=8:size=1080x1920:rate=30", "-f", "lavfi", "-i", "sine=duration=8", "-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-f", "matroska", "-").Output()
	if err != nil {
		t.Fatalf("make a clip: %v", err)
	}
	var root struct{ AccessKey, SecretKey string }
	_ = json.Unmarshal([]byte(strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", "objectstore-credentials", "--reveal"))), &root)
	endpoint := strings.TrimPrefix(fmt.Sprint(getJSON(t, s, token, "bucket", media).Outputs["endpoint"]), "http://")
	store, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(root.AccessKey, root.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(ctx, media, "incoming/clip.mkv", bytes.NewReader(clip), int64(len(clip)), minio.PutObjectOptions{UserMetadata: map[string]string{"video-id": "v42"}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Minute)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no webhook; runs:\n%s", mustCLI(t, s, token, "", "transcoder", "runs", tc))
		}
		time.Sleep(2 * time.Second)
	}
	mu.Lock()
	body, sig := bodies[0], sigs[0]
	mu.Unlock()
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("signature %q", sig)
	}
	var msg api.TranscodeMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Status != api.RunSucceeded || msg.HLSMaster == nil || *msg.HLSMaster != "processed/v42/hls/master.m3u8" || msg.Metadata["video-id"] != "v42" {
		t.Fatalf("message: %s", body)
	}
	keys := make([]string, len(msg.Outputs))
	for i, o := range msg.Outputs {
		keys[i] = o.Key
	}
	for _, want := range []string{"processed/v42/selfie.mp4", "processed/v42/hls/360p/index.m3u8", "processed/v42/hls/360p/segment-001.ts", "processed/v42/hls/720p/index.m3u8"} {
		if !slices.Contains(keys, want) {
			t.Errorf("missing %s in %v", want, keys)
		}
	}
	// What is in the bucket: the master playlist lists both rungs at their real sizes, served as HLS,
	// and the outputs carry the tag.
	obj, err := store.GetObject(ctx, media, "processed/v42/hls/master.m3u8", minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	master, _ := io.ReadAll(obj)
	if !strings.Contains(string(master), "RESOLUTION=360x640\n360p/index.m3u8") || !strings.Contains(string(master), "RESOLUTION=720x1280\n720p/index.m3u8") {
		t.Errorf("master playlist:\n%s", master)
	}
	info, err := store.StatObject(ctx, media, "processed/v42/selfie.mp4", minio.StatObjectOptions{})
	if err != nil || info.UserMetadata["Video-Id"] != "v42" || info.ContentType != "video/mp4" {
		t.Errorf("rendition object: %v %v %s", err, info.UserMetadata, info.ContentType)
	}
	if st, err := store.StatObject(ctx, media, "processed/v42/hls/master.m3u8", minio.StatObjectOptions{}); err != nil || st.ContentType != "application/vnd.apple.mpegurl" {
		t.Errorf("master content type: %v %s", err, st.ContentType)
	}
	// The secret it signs with can't be deleted while it does.
	if _, _, code := cli(t, s, token, "", "secret", "delete", "e2e-hook-"+id, "--yes"); code == 0 {
		t.Error("deleted the transcoder's signing secret")
	}
}
