package transcode

import (
	"fmt"
	"path"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// Renditions and HLS ladders (L2-012 TRX-02, TRX-03, ADR-050). Every value written into the script is a
// validated name (^[a-z0-9][a-z0-9-]{0,31}$) or a bounded integer; nothing else reaches the shell.

// Outputs is everything one run writes.
type Outputs struct {
	Presets    []string
	Renditions []api.Rendition
	HLS        *api.HLSLadder
}

// MasterPlaylist is the ladder's master playlist, relative to the run's folder.
func MasterPlaylist(h *api.HLSLadder) string { return h.Name + "/master.m3u8" }

// scale fits the video inside w×h, keeps its aspect ratio and never scales it up.
func scale(w, h int) string {
	return fmt.Sprintf("-vf \"scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2\"", w, h)
}

// keyframes forces a keyframe every gop seconds, closed GOPs and no scene-cut keyframes, so every rung of
// a ladder has keyframes at the same timestamps.
func keyframes(gop int) string {
	return fmt.Sprintf("-force_key_frames \"expr:gte(t,n_forced*%d)\" -sc_threshold 0 -flags +cgop", gop)
}

func h264(w, h, kbps, gop, audio int) string {
	return fmt.Sprintf("-map 0:v:0 -map '0:a:0?' %s -c:v libx264 -preset veryfast -crf 23 -maxrate %dk -bufsize %dk -pix_fmt yuv420p %s -c:a aac -b:a %dk -ar 48000 -ac 2",
		scale(w, h), kbps, 2*kbps, keyframes(gop), audio)
}

func renditionArgs(r api.Rendition) string {
	if r.Container == api.ContainerWebM {
		return fmt.Sprintf("-map 0:v:0 -map '0:a:0?' %s -c:v libvpx-vp9 -crf 33 -b:v %dk -deadline realtime -cpu-used 8 %s -c:a libopus -b:a %dk -ar 48000 -ac 2",
			scale(r.MaxWidth, r.MaxHeight), r.VideoKbps, keyframes(r.GOPSeconds), r.AudioKbps)
	}
	return h264(r.MaxWidth, r.MaxHeight, r.VideoKbps, r.GOPSeconds, r.AudioKbps) + " -movflags +faststart"
}

// RenditionFile is a rendition's file under the run's folder.
func RenditionFile(r api.Rendition) string { return r.Name + "." + r.Container }

const ffmpeg = "ffmpeg -hide_banner -nostdin -loglevel error -y -i"

func writeRenditions(b *strings.Builder, input string, rs []api.Rendition) {
	for _, r := range rs {
		fmt.Fprintf(b, "echo '== %s'\n%s %s/%s %s %s/%s\n", r.Name, ffmpeg, InDir, input, renditionArgs(r), OutDir, RenditionFile(r))
	}
}

// writeLadder writes one FFmpeg run per rung, then the master playlist with each rung's bandwidth and the
// resolution FFmpeg actually produced.
func writeLadder(b *strings.Builder, input string, h *api.HLSLadder) {
	dir := OutDir + "/" + h.Name
	fmt.Fprintf(b, "mkdir -p %s\nprintf '#EXTM3U\\n#EXT-X-VERSION:3\\n' > %s/master.tmp\n", dir, dir)
	for _, r := range h.Rungs {
		rd := dir + "/" + r.Name
		fmt.Fprintf(b, "echo '== %s/%s'\nmkdir -p %s\n%s %s/%s %s -f hls -hls_time %d -hls_playlist_type vod -hls_segment_filename %s/segment-%%03d.ts %s/index.m3u8\n",
			h.Name, r.Name, rd, ffmpeg, InDir, input, h264(r.MaxWidth, r.MaxHeight, r.VideoKbps, h.GOPSeconds, h.AudioKbps), h.SegmentSeconds, rd, rd)
		fmt.Fprintf(b, "res=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 %s/segment-000.ts | head -n 1)\n", rd)
		fmt.Fprintf(b, "printf '#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%%s\\n%s/index.m3u8\\n' \"$res\" >> %s/master.tmp\n", (r.VideoKbps+h.AudioKbps)*1000, r.Name, dir)
	}
	fmt.Fprintf(b, "mv %s/master.tmp %s/master.m3u8\n", dir, dir)
}

// RunFolder is where a run's outputs go in the output bucket (TRN-06, TRX-07): with {name} or {meta:<tag>}
// in the output folder, that folder filled in; otherwise <output folder><input key without the input
// folder and its extension>/.
func RunFolder(outPrefix, inPrefix, key string, tags map[string]string) (string, error) {
	if api.HasPlaceholders(outPrefix) {
		name := path.Base(key)
		return api.RenderOutputPrefix(outPrefix, strings.TrimSuffix(name, path.Ext(name)), tags)
	}
	base := strings.TrimPrefix(key, inPrefix)
	return outPrefix + strings.TrimSuffix(base, path.Ext(base)) + "/", nil
}
