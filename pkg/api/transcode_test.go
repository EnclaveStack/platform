package api

import (
	"slices"
	"strings"
	"testing"
)

// L2-012 TRX-01 – TRX-07: renditions, the ladder, notify and output folders from tags.
func TestRenditionsLadderAndNotifyValidation(t *testing.T) {
	s, errs := normalize(t, KindTranscoder, `{"input":{"bucket":"media","prefix":"incoming/"},"output":{"bucket":"media","prefix":"processed/{meta:video-id}/"},
		"renditions":[{"name":"selfie","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":2500}],
		"hls":{"rungs":[{"name":"360p","maxWidth":360,"maxHeight":640,"videoKbps":600},{"name":"1080p","maxWidth":1080,"maxHeight":1920,"videoKbps":4000}]},
		"notify":{"webhook":{"url":"https://video.example.com/hooks/transcode","secret":"hook-key"},"topic":"video-events"}}`)
	tc := s.(TranscoderSpec)
	if len(errs) != 0 {
		t.Fatalf("valid spec refused: %v", errs)
	}
	if r := tc.Renditions[0]; r.AudioKbps != 128 || r.GOPSeconds != 2 {
		t.Errorf("rendition defaults: %+v", r)
	}
	if h := tc.HLS; h.Name != "hls" || h.SegmentSeconds != 6 || h.GOPSeconds != 2 || h.AudioKbps != 128 {
		t.Errorf("ladder defaults: %+v", h)
	}
	if len(tc.Presets) != 0 {
		t.Error("presets are optional")
	}
	base := `"input":{"bucket":"upl"},"output":{"bucket":"oth"}`
	rung := `{"name":"720p","maxWidth":720,"maxHeight":1280,"videoKbps":2000}`
	cases := map[string]string{
		`{` + base + `}`: "spec.presets:REQUIRED",
		`{` + base + `,"renditions":[{"name":"Big One","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":2000}]}`:                                                                        "spec.renditions[0].name:INVALID_FORMAT",
		`{` + base + `,"renditions":[{"name":"a","container":"mkv","maxWidth":720,"maxHeight":1280,"videoKbps":2000}]}`:                                                                              "spec.renditions[0].container:NOT_ALLOWED",
		`{` + base + `,"renditions":[{"name":"a","container":"mp4","maxWidth":721,"maxHeight":1280,"videoKbps":2000}]}`:                                                                              "spec.renditions[0].maxWidth:OUT_OF_RANGE",
		`{` + base + `,"renditions":[{"name":"a","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":50}]}`:                                                                                "spec.renditions[0].videoKbps:OUT_OF_RANGE",
		`{` + base + `,"renditions":[{"name":"a","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":900,"audioKbps":9}]}`:                                                                 "spec.renditions[0].audioKbps:OUT_OF_RANGE",
		`{` + base + `,"renditions":[{"name":"a","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":900,"gopSeconds":11}]}`:                                                               "spec.renditions[0].gopSeconds:OUT_OF_RANGE",
		`{` + base + `,"presets":["mp4-720p"],"renditions":[{"name":"mp4-720p","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":900}]}`:                                                 "spec.renditions[0].name:NOT_ALLOWED",
		`{` + base + `,"renditions":[{"name":"a","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":900},{"name":"a","container":"mp4","maxWidth":360,"maxHeight":640,"videoKbps":500}]}`: "spec.renditions[1].name:NOT_ALLOWED",
		`{` + base + `,"hls":{"rungs":[]}}`:                                                                         "spec.hls.rungs:OUT_OF_RANGE",
		`{` + base + `,"hls":{"segmentSeconds":5,"rungs":[` + rung + `]}}`:                                          "spec.hls.segmentSeconds:NOT_ALLOWED",
		`{` + base + `,"hls":{"segmentSeconds":12,"rungs":[` + rung + `]}}`:                                         "spec.hls.segmentSeconds:OUT_OF_RANGE",
		`{` + base + `,"hls":{"rungs":[` + rung + `,` + rung + `]}}`:                                                "spec.hls.rungs[1].name:NOT_ALLOWED",
		`{` + base + `,"presets":["hls-720p"],"hls":{"name":"hls-720p","rungs":[` + rung + `]}}`:                    "spec.hls.name:NOT_ALLOWED",
		`{` + base + `,"presets":["mp3"],"notify":{}}`:                                                              "spec.notify:REQUIRED",
		`{` + base + `,"presets":["mp3"],"notify":{"webhook":{"url":"http://10.0.0.5/hook"}}}`:                      "spec.notify.webhook.url:INVALID_FORMAT",
		`{` + base + `,"presets":["mp3"],"notify":{"topic":"Not A Name"}}`:                                          "spec.notify.topic:INVALID_FORMAT",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth","prefix":"out/{meta:Video_ID}/"},"presets":["mp3"]}`:    "spec.output.prefix:INVALID_FORMAT",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth","prefix":"out/{date}/"},"presets":["mp3"]}`:             "spec.output.prefix:INVALID_FORMAT",
		`{"input":{"bucket":"up","prefix":"in/"},"output":{"bucket":"up","prefix":"{meta:id}/"},"presets":["mp3"]}`: "spec.output.prefix:NOT_ALLOWED",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindTranscoder, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	// A rendition and the ladder may share a name: one writes a file, the other a folder.
	if _, errs := normalize(t, KindTranscoder, `{`+base+`,"renditions":[{"name":"hls","container":"mp4","maxWidth":720,"maxHeight":1280,"videoKbps":900}],"hls":{"rungs":[`+rung+`]}}`); len(errs) != 0 {
		t.Errorf("rendition hls.mp4 beside the folder hls/: %v", errs)
	}
}

func TestRenderOutputPrefix(t *testing.T) {
	got, err := RenderOutputPrefix("processed/{meta:video-id}/{name}/", "clip", map[string]string{"video-id": "v42"})
	if err != nil || got != "processed/v42/clip/" {
		t.Errorf("%q %v", got, err)
	}
	for _, v := range []string{"..", "a/b", "x y", ""} {
		if _, err := RenderOutputPrefix("p/{meta:id}/", "n", map[string]string{"id": v}); err == nil || !strings.Contains(err.Error(), "id") {
			t.Errorf("tag %q: %v", v, err)
		}
	}
	if HasPlaceholders("processed/") || !HasPlaceholders("p/{name}/") {
		t.Error("placeholders")
	}
}
