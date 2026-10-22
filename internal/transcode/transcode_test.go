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
	s := Script(InputName("videos/$(rm -rf).MOV"), []string{api.PresetMP4720p, api.PresetHLS720p})
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
