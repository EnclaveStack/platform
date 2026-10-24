package cli_test

import (
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// Renditions, the HLS ladder and notify from the CLI (L2-002 CLI-54, L2-012).
func TestTranscoderRenditionsLadderAndNotifyFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("bucket", "create", "media", "--wait")
	h.ok("secret", "create", "hook-key", "--value", "s3cret")
	h.ok("topic", "create", "video-events", "--wait")
	h.ok("transcoder", "create", "videos", "--input", "media/incoming/", "--output", "media/processed/{meta:video-id}/",
		"--rendition", "selfie:mp4:720x1280:2500", "--rendition", "web:webm:1280x720:1500:96:2",
		"--hls-rung", "360p:360x640:600", "--hls-rung", "1080p:1080x1920:4000", "--hls-segment", "6",
		"--notify-webhook", "https://video.example.com/done", "--notify-secret", "hook-key", "--notify-topic", "video-events", "--wait")
	var tc api.TranscoderSpec
	h.spec("transcoder", "videos", &tc)
	if len(tc.Presets) != 0 || len(tc.Renditions) != 2 || tc.Renditions[1] != (api.Rendition{Name: "web", Container: "webm", MaxWidth: 1280, MaxHeight: 720, VideoKbps: 1500, AudioKbps: 96, GOPSeconds: 2}) ||
		tc.HLS == nil || len(tc.HLS.Rungs) != 2 || tc.HLS.Rungs[1] != (api.HLSRung{Name: "1080p", MaxWidth: 1080, MaxHeight: 1920, VideoKbps: 4000}) || tc.HLS.Name != "hls" ||
		tc.Notify == nil || tc.Notify.Webhook.URL != "https://video.example.com/done" || tc.Notify.Webhook.Secret != "hook-key" || tc.Notify.Topic != "video-events" ||
		tc.Output.Prefix != "processed/{meta:video-id}/" {
		t.Fatalf("spec %+v", tc)
	}
	if r := h.ok("get", "transcoder", "videos"); !strings.Contains(r.out, "1080p") || !strings.Contains(r.out, "video.example.com") {
		t.Errorf("get: %s", r.out)
	}

	// Update one setting: the rest of the ladder and of notify stay.
	h.ok("transcoder", "update", "videos", "--hls-gop", "3", "--hls-segment", "9", "--notify-topic", "", "--wait")
	tc = api.TranscoderSpec{}
	h.spec("transcoder", "videos", &tc)
	if tc.HLS.GOPSeconds != 3 || tc.HLS.SegmentSeconds != 9 || len(tc.HLS.Rungs) != 2 || tc.Notify.Topic != "" || tc.Notify.Webhook.Secret != "hook-key" || len(tc.Renditions) != 2 {
		t.Errorf("after update %+v %+v", tc.HLS, tc.Notify)
	}
	// Clear each, keeping one output.
	h.ok("transcoder", "update", "videos", "--no-hls", "--rendition", "", "--preset", "mp3", "--notify-webhook", "", "--wait")
	tc = api.TranscoderSpec{}
	h.spec("transcoder", "videos", &tc)
	if tc.HLS != nil || len(tc.Renditions) != 0 || tc.Notify != nil || len(tc.Presets) != 1 {
		t.Errorf("after clearing %+v", tc)
	}

	// Mistakes are caught before or by the server.
	for _, args := range [][]string{
		{"--rendition", "selfie:mp4:720:2500"},
		{"--rendition", "selfie:mp4:720x1280:fast"},
		{"--hls-rung", "720p:720x1280"},
		{"--no-hls", "--hls-gop", "2"},
		{"--hls-gop", "2"}, // a ladder needs rungs
	} {
		if r := h.run(append([]string{"transcoder", "update", "videos"}, args...)...); r.code != cli.ExitUsage {
			t.Errorf("%v: %d %s", args, r.code, r.err)
		}
	}
	if r := h.run("transcoder", "update", "videos", "--rendition", "a:mp4:721x1280:2500"); r.code != cli.ExitError || !strings.Contains(r.err, "maxWidth") {
		t.Errorf("an odd width: %d %s", r.code, r.err)
	}
	if r := h.run("transcoder", "create", "nothing", "--input", "media/in/", "--output", "media/out/"); r.code != cli.ExitError {
		t.Errorf("no outputs: %d %s", r.code, r.err)
	}
}
