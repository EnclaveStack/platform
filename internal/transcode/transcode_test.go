package transcode

import (
	"strings"
	"testing"

	"github.com/enclavestack/platform/pkg/api"
)

func TestEveryPresetHasACommand(t *testing.T) {
	for _, p := range api.Presets {
		if File(p) == "" {
			t.Errorf("%s has no command", p)
		}
	}
}

func TestScriptUsesOnlyTheFixedInputName(t *testing.T) {
	s := Script(InputName("videos/$(rm -rf).MOV"), Outputs{Presets: []string{api.PresetMP4720p, api.PresetHLS720p}})
	if strings.Contains(s, "rm -rf") || !strings.Contains(s, "/in/input.mov ") {
		t.Errorf("script:\n%s", s)
	}
	if !strings.Contains(s, "mkdir -p /out/hls-720p") || !strings.HasPrefix(s, "set -e\nmkdir -p /out\n") {
		t.Errorf("script:\n%s", s)
	}
	if InputName("weird.$x") != "input" || InputName("noext") != "input" {
		t.Error("odd extensions are dropped")
	}
}

func TestOutputKeys(t *testing.T) {
	if k := OutputKey("media/", "videos/", "videos/2026/cat.mov", "hls-720p/index.m3u8"); k != "media/2026/cat/hls-720p/index.m3u8" {
		t.Error(k)
	}
}

func TestRenditionsAndALadderWriteTheirFilesAndAMasterPlaylist(t *testing.T) {
	h := &api.HLSLadder{Name: "hls", SegmentSeconds: 6, GOPSeconds: 2, AudioKbps: 128, Rungs: []api.HLSRung{
		{Name: "360p", MaxWidth: 360, MaxHeight: 640, VideoKbps: 600}, {Name: "720p", MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2000}}}
	s := Script("input.mov", Outputs{Renditions: []api.Rendition{{Name: "selfie", Container: api.ContainerMP4, MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2500, AudioKbps: 128, GOPSeconds: 2},
		{Name: "web", Container: api.ContainerWebM, MaxWidth: 1280, MaxHeight: 720, VideoKbps: 1500, AudioKbps: 96, GOPSeconds: 2}}, HLS: h})
	for _, want := range []string{
		"scale=w='min(720,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease", // never scaled up
		"-maxrate 2500k -bufsize 5000k", "/out/selfie.mp4", "-movflags +faststart",
		"libvpx-vp9 -crf 33 -b:v 1500k", "-c:a libopus -b:a 96k", "/out/web.webm",
		"expr:gte(t,n_forced*2)", "-sc_threshold 0 -flags +cgop", // aligned keyframes
		"-hls_time 6", "/out/hls/360p/segment-%03d.ts /out/hls/360p/index.m3u8",
		"BANDWIDTH=728000,RESOLUTION=%s\\n360p/index.m3u8", "BANDWIDTH=2128000", "mv /out/hls/master.tmp /out/hls/master.m3u8",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %q:\n%s", want, s)
		}
	}
	if MasterPlaylist(h) != "hls/master.m3u8" {
		t.Error(MasterPlaylist(h))
	}
}

func TestRunFolders(t *testing.T) {
	tags := map[string]string{"video-id": "v42"}
	for _, c := range []struct{ out, key, want, err string }{
		{"media/", "videos/2026/cat.mov", "media/2026/cat/", ""},
		{"processed/{meta:video-id}/", "videos/2026/cat.mov", "processed/v42/", ""},
		{"processed/{name}-{meta:video-id}/", "videos/2026/cat.mov", "processed/cat-v42/", ""},
		{"processed/{meta:user}/", "videos/cat.mov", "", "has no tag user"},
	} {
		got, err := RunFolder(c.out, "videos/", c.key, tags)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v", c.out, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v", c.out, got, err)
		}
	}
	if _, err := RunFolder("p/{meta:id}/", "", "a.mov", map[string]string{"id": "../etc"}); err == nil {
		t.Error("a tag that leaves the folder is refused")
	}
}
