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

// commands maps each preset to its FFmpeg output arguments; its file is api.PresetFiles.
var commands = map[string]string{
	api.PresetMP4480p:   x264(480) + " -movflags +faststart",
	api.PresetMP4720p:   x264(720) + " -movflags +faststart",
	api.PresetMP41080p:  x264(1080) + " -movflags +faststart",
	api.PresetWebM720p:  "-vf \"scale=-2:'min(720,ih)'\" -c:v libvpx-vp9 -b:v 0 -crf 33 -deadline realtime -cpu-used 8 -c:a libopus",
	api.PresetHLS720p:   x264(720) + " -f hls -hls_time 6 -hls_playlist_type vod -hls_segment_filename " + OutDir + "/hls-720p/segment-%03d.ts",
	api.PresetMP3:       "-vn -c:a libmp3lame -q:a 2",
	api.PresetThumbnail: "-vf thumbnail,scale=640:-2 -frames:v 1 -update 1",
}

// File is where a preset's main output lands, relative to the run's output folder (TRN-06).
func File(preset string) string { return api.PresetFiles[preset] }

// InputName is the name the input gets inside the container: input plus the key's extension when it is
// a plain one, so FFmpeg can use it as a hint. The key itself never reaches the command line.
func InputName(key string) string {
	ext := strings.ToLower(path.Ext(key))
	if len(ext) < 2 || len(ext) > 6 || strings.Trim(ext[1:], "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return "input"
	}
	return "input" + ext
}

// Script returns the shell script converting InDir/<input> with each preset, rendition and rung in turn; it stops at the
// first failure, so the run's output ends with FFmpeg's reason.
func Script(input string, o Outputs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nmkdir -p %s\n", OutDir)
	for _, p := range o.Presets {
		args, ok := commands[p]
		if !ok {
			continue
		}
		file := File(p)
		if dir := path.Dir(file); dir != "." {
			fmt.Fprintf(&b, "mkdir -p %s/%s\n", OutDir, dir)
		}
		fmt.Fprintf(&b, "echo '== %s'\nffmpeg -hide_banner -nostdin -loglevel error -y -i %s/%s %s %s/%s\n", p, InDir, input, args, OutDir, file)
	}
	writeRenditions(&b, input, o.Renditions)
	if o.HLS != nil {
		writeLadder(&b, input, o.HLS)
	}
	return b.String()
}

// OutputKey is where a written file goes in the output bucket without placeholders in the output folder:
// RunFolder plus the file (TRN-06).
func OutputKey(outPrefix, inPrefix, key, file string) string {
	folder, _ := RunFolder(outPrefix, inPrefix, key, nil)
	return folder + file
}
