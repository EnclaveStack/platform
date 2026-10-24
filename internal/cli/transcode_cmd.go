package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Transcoder renditions, HLS ladders and notify (L2-012, L2-002 CLI-54).

type transcodeOutputFlags struct {
	renditions, rungs            []string
	hlsName                      string
	hlsSegment, hlsGOP, hlsAudio int
	noHLS                        bool
	notifyWebhook, notifySecret  string
	notifyTopic                  string
}

func (f *transcodeOutputFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVar(&f.renditions, "rendition", nil, `a file of your own, NAME:mp4|webm:WIDTHxHEIGHT:VIDEO_KBPS[:AUDIO_KBPS[:KEYFRAME_SECONDS]], like selfie:mp4:720x1280:2500 (repeatable; on update the list replaces the old one, "" for none)`)
	fl.StringArrayVar(&f.rungs, "hls-rung", nil, `a quality of the HLS ladder, NAME:WIDTHxHEIGHT:VIDEO_KBPS, like 720p:720x1280:2000 (repeatable; on update the list replaces the old one)`)
	fl.StringVar(&f.hlsName, "hls-name", "", "the ladder's folder (default hls)")
	fl.IntVar(&f.hlsSegment, "hls-segment", 0, "seconds per segment, a multiple of --hls-gop (2–10, default 6)")
	fl.IntVar(&f.hlsGOP, "hls-gop", 0, "seconds between keyframes, the same in every rung (1–10, default 2)")
	fl.IntVar(&f.hlsAudio, "hls-audio", 0, "audio kbps of every rung (32–512, default 128)")
	fl.BoolVar(&f.noHLS, "no-hls", false, "remove the HLS ladder")
	fl.StringVar(&f.notifyWebhook, "notify-webhook", "", `call this https:// URL when a file is done or failed ("" for none)`)
	fl.StringVar(&f.notifySecret, "notify-secret", "", `the secret whose value signs the webhook (X-Enclave-Signature; "" for none)`)
	fl.StringVar(&f.notifyTopic, "notify-topic", "", `publish to this topic when a file is done or failed ("" for none)`)
}

func atoiFlag(flag, part, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usagef("--%s: %s must be a number, got %q", flag, part, s)
	}
	return n, nil
}

// parseBox reads WIDTHxHEIGHT.
func parseBox(flag, s string) (int, int, error) {
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, usagef("--%s needs WIDTHxHEIGHT, like 720x1280; got %q", flag, s)
	}
	wi, err := atoiFlag(flag, "the width", w)
	if err != nil {
		return 0, 0, err
	}
	hi, err := atoiFlag(flag, "the height", h)
	return wi, hi, err
}

func parseRenditions(list []string) ([]api.Rendition, error) {
	out := []api.Rendition{}
	if len(list) == 1 && list[0] == "" {
		return out, nil
	}
	for _, s := range list {
		p := strings.Split(s, ":")
		if len(p) < 4 || len(p) > 6 {
			return nil, usagef("--rendition needs NAME:mp4|webm:WIDTHxHEIGHT:VIDEO_KBPS[:AUDIO_KBPS[:KEYFRAME_SECONDS]], like selfie:mp4:720x1280:2500; got %q", s)
		}
		r := api.Rendition{Name: p[0], Container: p[1]}
		var err error
		if r.MaxWidth, r.MaxHeight, err = parseBox("rendition", p[2]); err != nil {
			return nil, err
		}
		if r.VideoKbps, err = atoiFlag("rendition", "the video kbps", p[3]); err != nil {
			return nil, err
		}
		if len(p) > 4 {
			if r.AudioKbps, err = atoiFlag("rendition", "the audio kbps", p[4]); err != nil {
				return nil, err
			}
		}
		if len(p) > 5 {
			if r.GOPSeconds, err = atoiFlag("rendition", "the keyframe seconds", p[5]); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func parseRungs(list []string) ([]api.HLSRung, error) {
	var out []api.HLSRung
	for _, s := range list {
		p := strings.Split(s, ":")
		if len(p) != 3 {
			return nil, usagef("--hls-rung needs NAME:WIDTHxHEIGHT:VIDEO_KBPS, like 720p:720x1280:2000; got %q", s)
		}
		r := api.HLSRung{Name: p[0]}
		var err error
		if r.MaxWidth, r.MaxHeight, err = parseBox("hls-rung", p[1]); err != nil {
			return nil, err
		}
		if r.VideoKbps, err = atoiFlag("hls-rung", "the video kbps", p[2]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// patch adds renditions, hls and notify to a transcoder patch. Each replaces its field whole (L3-002
// §13.1), so on update the settings not given are taken from current.
func (f *transcodeOutputFlags) patch(p map[string]any, changed func(string) bool, current *api.TranscoderSpec) error {
	if current == nil {
		current = &api.TranscoderSpec{}
	}
	if changed("rendition") {
		r, err := parseRenditions(f.renditions)
		if err != nil {
			return err
		}
		if len(r) == 0 {
			p["renditions"] = nil
		} else {
			p["renditions"] = r
		}
	}
	hlsFlags := []string{"hls-rung", "hls-name", "hls-segment", "hls-gop", "hls-audio"}
	anyHLS := false
	for _, n := range hlsFlags {
		anyHLS = anyHLS || changed(n)
	}
	switch {
	case f.noHLS && anyHLS:
		return usagef("use either --no-hls or the --hls-* flags")
	case f.noHLS:
		p["hls"] = nil
	case anyHLS:
		h := api.HLSLadder{}
		if current.HLS != nil {
			h = *current.HLS
		}
		if changed("hls-rung") {
			rungs, err := parseRungs(f.rungs)
			if err != nil {
				return err
			}
			h.Rungs = rungs
		}
		if changed("hls-name") {
			h.Name = f.hlsName
		}
		if changed("hls-segment") {
			h.SegmentSeconds = f.hlsSegment
		}
		if changed("hls-gop") {
			h.GOPSeconds = f.hlsGOP
		}
		if changed("hls-audio") {
			h.AudioKbps = f.hlsAudio
		}
		if len(h.Rungs) == 0 {
			return usagef("an HLS ladder needs at least one --hls-rung NAME:WIDTHxHEIGHT:VIDEO_KBPS")
		}
		p["hls"] = h
	}
	if changed("notify-webhook") || changed("notify-secret") || changed("notify-topic") {
		n := api.TranscodeNotify{}
		if current.Notify != nil {
			n = *current.Notify
			if n.Webhook != nil {
				w := *n.Webhook
				n.Webhook = &w
			}
		}
		if changed("notify-webhook") {
			if f.notifyWebhook == "" {
				n.Webhook = nil
			} else if n.Webhook == nil {
				n.Webhook = &api.TranscodeWebhook{URL: f.notifyWebhook}
			} else {
				n.Webhook.URL = f.notifyWebhook
			}
		}
		if changed("notify-secret") {
			if n.Webhook == nil {
				return usagef("--notify-secret signs a webhook: give --notify-webhook too")
			}
			n.Webhook.Secret = f.notifySecret
		}
		if changed("notify-topic") {
			n.Topic = f.notifyTopic
		}
		if n.Webhook == nil && n.Topic == "" {
			p["notify"] = nil
		} else {
			p["notify"] = n
		}
	}
	return nil
}
