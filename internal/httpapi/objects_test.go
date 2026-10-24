package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

func apiErr(err error) *api.Error {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	return &api.Error{}
}

// Bucket objects through the API on the simulated engine (L2-013 OBJ-01 – OBJ-06; the real object store
// is in tests/e2e).
func TestBucketObjectsThroughTheAPI(t *testing.T) {
	env := testenv.Start(t)
	c, tok := env.Token(t, api.ScopeWrite)
	reader, _ := env.Token(t, api.ScopeRead)
	ctx := context.Background()
	raw := func(method, path string, body io.Reader, length int64) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, env.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	b, err := c.CreateResource(ctx, api.KindBucket, "media", api.BucketSpec{})
	if err != nil {
		t.Fatal(err)
	}
	b = testenv.WaitReady(t, c, b.ID)

	// Upload with tags; the media type comes from the extension.
	info, err := c.PutObject(ctx, b.ID, "incoming/clip one.mov", strings.NewReader("video bytes"), 11, map[string]string{"video-id": "v42"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 11 || info.ContentType != "video/quicktime" || info.Tags["video-id"] != "v42" {
		t.Errorf("upload: %+v", info)
	}
	for _, k := range []string{"readme.txt", "incoming/a.mp4", "incoming/sub/b.jpg"} {
		if _, err := c.PutObject(ctx, b.ID, k, strings.NewReader(k), -1, nil); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	// One folder at a time: folders, then objects in key order.
	top, err := c.ListObjects(ctx, b.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(top.Folders) != "[incoming/]" || len(top.Items) != 1 || top.Items[0].Key != "readme.txt" || top.NextCursor != nil {
		t.Errorf("top: %+v", top)
	}
	in, _ := c.ListObjects(ctx, b.ID, "incoming/", "")
	keys := []string{}
	for _, o := range in.Items {
		keys = append(keys, o.Key)
	}
	if fmt.Sprint(in.Folders) != "[incoming/sub/]" || fmt.Sprint(keys) != "[incoming/a.mp4 incoming/clip one.mov]" {
		t.Errorf("incoming: %+v", in)
	}
	// Paging: one entry a page, following the cursor, reaches every entry once.
	var seen []string
	cursor := ""
	for range 10 {
		page := raw(http.MethodGet, "/api/v1/resources/"+b.ID+"/objects?prefix=incoming/&limit=1&cursor="+url.QueryEscape(cursor), nil, 0)
		var l api.ObjectList
		pb, _ := io.ReadAll(page.Body)
		page.Body.Close()
		if err := json.Unmarshal(pb, &l); err != nil {
			t.Fatalf("%d %s: %v", page.StatusCode, pb, err)
		}
		seen = append(seen, l.Folders...)
		for _, o := range l.Items {
			seen = append(seen, o.Key)
		}
		if l.NextCursor == nil {
			break
		}
		cursor = *l.NextCursor
	}
	if len(seen) != 3 || !slices.IsSorted(seen) {
		t.Errorf("pages: %v", seen)
	}

	// Download: bytes, media type, tags, as an attachment.
	got, body, err := c.GetObject(ctx, b.ID, "incoming/clip one.mov")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "video bytes" || got.Tags["video-id"] != "v42" || got.Size != 11 {
		t.Errorf("download: %q %+v", data, got)
	}
	dl := raw(http.MethodGet, "/api/v1/resources/"+b.ID+"/objects/readme.txt", nil, 0)
	if dl.Header.Get("Content-Disposition") != `attachment; filename=readme.txt` || dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("download headers: %v", dl.Header)
	}
	dl.Body.Close()

	// Delete, twice: the second is already done.
	for range 2 {
		if err := c.DeleteObject(ctx, b.ID, "readme.txt"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.GetObject(ctx, b.ID, "readme.txt"); apiErr(err).Code != api.CodeNotFound {
		t.Errorf("deleted object: %v", err)
	}
	ev, _ := c.Events(ctx, b.ID, 50)
	if !slices.ContainsFunc(ev, func(e api.Event) bool {
		return e.Type == "object_uploaded" && strings.HasPrefix(e.Message, "Uploaded incoming/clip one.mov (11 B)")
	}) ||
		!slices.ContainsFunc(ev, func(e api.Event) bool {
			return e.Type == "object_deleted" && strings.HasPrefix(e.Message, "Deleted readme.txt")
		}) {
		t.Errorf("events: %+v", ev)
	}

	// Refused: a read-only token, bad keys and tags, something that isn't a bucket.
	if _, err := reader.ListObjects(ctx, b.ID, "", ""); apiErr(err).Code != api.CodeForbidden {
		t.Errorf("a viewer listing: %v", err)
	}
	for _, k := range []string{"a/../b", "a//b", "./a", "dir/"} {
		if _, err := c.PutObject(ctx, b.ID, k, strings.NewReader("x"), 1, nil); apiErr(err).Code != api.CodeValidationFailed {
			t.Errorf("key %q: %v", k, err)
		}
	}
	if _, err := c.PutObject(ctx, b.ID, "x.txt", strings.NewReader("x"), 1, map[string]string{"Bad_Name": "1"}); apiErr(err).Code != api.CodeValidationFailed {
		t.Errorf("a bad tag: %v", err)
	}
	if _, err := c.ListObjects(ctx, b.ID, "noslash", ""); apiErr(err).Code != api.CodeValidationFailed {
		t.Errorf("a bad prefix: %v", err)
	}
	q, _ := c.CreateResource(ctx, api.KindQueue, "jobs", api.QueueSpec{})
	if _, err := c.ListObjects(ctx, q.ID, "", ""); apiErr(err).Code != api.CodeActionNotSupported {
		t.Errorf("a queue's objects: %v", err)
	}
	// An upload larger than the limit says so before any byte is read.
	resp := raw(http.MethodPut, "/api/v1/resources/"+b.ID+"/objects/big.bin", io.LimitReader(zeros{}, api.ObjectUploadMax+1), api.ObjectUploadMax+1)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// An upload through the API reaches a transcoder watching the folder, with its tag (L2-012 TRX-06).
func TestAnUploadReachesATranscoderWithItsTags(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	b, _ := c.CreateResource(ctx, api.KindBucket, "media", api.BucketSpec{})
	testenv.WaitReady(t, c, b.ID)
	tc, err := c.CreateResource(ctx, api.KindTranscoder, "videos", api.TranscoderSpec{Input: api.BucketFolder{Bucket: "media", Prefix: "incoming/"},
		Output: api.BucketFolder{Bucket: "media", Prefix: "processed/{meta:video-id}/"}, Presets: []string{api.PresetMP3}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, c, tc.ID)
	time.Sleep(300 * time.Millisecond) // the baseline listing
	if _, err := c.PutObject(ctx, b.ID, "incoming/talk.mov", bytes.NewReader([]byte("talk")), 4, map[string]string{"video-id": "v7"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		l, _ := c.ListObjects(ctx, b.ID, "processed/v7/", "")
		if len(l.Items) == 1 && l.Items[0].Key == "processed/v7/mp3.mp3" {
			got, body, err := c.GetObject(ctx, b.ID, "processed/v7/mp3.mp3")
			if err != nil {
				t.Fatal(err)
			}
			body.Close()
			if got.Tags["video-id"] != "v7" || got.Tags["enclave-transcoder"] != "videos" || got.ContentType != "audio/mpeg" {
				t.Errorf("output: %+v", got)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the transcoder didn't convert the upload")
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }
