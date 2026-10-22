// Package transcode holds the fixed FFmpeg presets of transcoders (ADR-034, L2-008 TRN-02) and turns them
// into the script a run executes. It imports nothing of the control plane; nothing the user typed reaches
// the script except the choice of presets.
package transcode

import (
	"fmt"
	"path"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// OutDir is where a run writes; the driver copies it out.
const OutDir = "/out"

// InDir is where the driver puts the input.
const InDir = "/in"

func x264(height int) string {
	return fmt.Sprintf("-vf \"scale=-2:'min(%d,ih)'\" -c:v libx264 -preset veryfast -crf 23 -pix_fmt yuv420p -c:a aac -b:a 128k", height)
}

// commands maps each preset to its FFmpeg output arguments and file, relative to OutDir.
var commands = map[string]struct{ args, file string }{
	api.PresetMP4480p:   {x264(480) + " -movflags +faststart", "mp4-480p.mp4"},
	api.PresetMP4720p:   {x264(720) + " -movflags +faststart", "mp4-720p.mp4"},
	api.PresetMP41080p:  {x264(1080) + " -movflags +faststart", "mp4-1080p.mp4"},
	api.PresetWebM720p:  {"-vf \"scale=-2:'min(720,ih)'\" -c:v libvpx-vp9 -b:v 0 -crf 33 -deadline realtime -cpu-used 8 -c:a libopus", "webm-720p.webm"},
	api.PresetHLS720p:   {x264(720) + " -f hls -hls_time 6 -hls_playlist_type vod -hls_segment_filename " + OutDir + "/hls-720p/segment-%03d.ts", "hls-720p/index.m3u8"},
	api.PresetMP3:       {"-vn -c:a libmp3lame -q:a 2", "mp3.mp3"},
	api.PresetThumbnail: {"-vf thumbnail,scale=640:-2 -frames:v 1 -update 1", "thumbnail.jpg"},
}

// File is where a preset's main output lands, relative to the run's output folder (TRN-06).
func File(preset string) string { return commands[preset].file }

// InputName is the name the input gets inside the container: input plus the key's extension when it is
// a plain one, so FFmpeg can use it as a hint. The key itself never reaches the command line.
func InputName(key string) string {
	ext := strings.ToLower(path.Ext(key))
	if len(ext) < 2 || len(ext) > 6 || strings.Trim(ext[1:], "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return "input"
	}
	return "input" + ext
}

// Script returns the shell script converting InDir/<input> with each preset in turn; it stops at the
// first failure, so the run's output ends with FFmpeg's reason.
func Script(input string, presets []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nmkdir -p %s\n", OutDir)
	for _, p := range presets {
		c, ok := commands[p]
		if !ok {
			continue
		}
		if dir := path.Dir(c.file); dir != "." {
			fmt.Fprintf(&b, "mkdir -p %s/%s\n", OutDir, dir)
		}
		fmt.Fprintf(&b, "echo '== %s'\nffmpeg -hide_banner -nostdin -loglevel error -y -i %s/%s %s %s/%s\n", p, InDir, input, c.args, OutDir, c.file)
	}
	return b.String()
}

// OutputKey is where a written file goes in the output bucket: <output folder><input key without the
// input folder and its extension>/<file> (TRN-06).
func OutputKey(outPrefix, inPrefix, key, file string) string {
	base := strings.TrimPrefix(key, inPrefix)
	base = strings.TrimSuffix(base, path.Ext(base))
	return outPrefix + base + "/" + file
}
