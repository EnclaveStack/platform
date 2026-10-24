package api

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Transcoder renditions, HLS ladders, notifications and tags (L2-012, L3-002 §13, ADR-050).

// Rendition is one output file of a size and bitrate the user chose (TRX-02).
type Rendition struct {
	Name       string `json:"name"`
	Container  string `json:"container"`
	MaxWidth   int    `json:"maxWidth"`
	MaxHeight  int    `json:"maxHeight"`
	VideoKbps  int    `json:"videoKbps"`
	AudioKbps  int    `json:"audioKbps,omitempty"`
	GOPSeconds int    `json:"gopSeconds,omitempty"`
}

// HLSRung is one quality of an HLS ladder (TRX-03).
type HLSRung struct {
	Name      string `json:"name"`
	MaxWidth  int    `json:"maxWidth"`
	MaxHeight int    `json:"maxHeight"`
	VideoKbps int    `json:"videoKbps"`
}

// HLSLadder is an adaptive stream: rungs with aligned keyframes and a master playlist (TRX-03).
type HLSLadder struct {
	Name           string    `json:"name,omitempty"`
	SegmentSeconds int       `json:"segmentSeconds,omitempty"`
	GOPSeconds     int       `json:"gopSeconds,omitempty"`
	AudioKbps      int       `json:"audioKbps,omitempty"`
	Rungs          []HLSRung `json:"rungs"`
}

// TranscodeWebhook is where a transcoder posts its message, signed with a secret when one is named.
type TranscodeWebhook struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// TranscodeNotify says where a run's message goes (TRX-05).
type TranscodeNotify struct {
	Webhook *TranscodeWebhook `json:"webhook,omitempty"`
	Topic   string            `json:"topic,omitempty"`
}

// Containers of renditions.
const (
	ContainerMP4  = "mp4"
	ContainerWebM = "webm"
)

// RenditionContainers lists the containers.
var RenditionContainers = []string{ContainerMP4, ContainerWebM}

// Bounds and defaults (L3-002 §13.4).
const (
	RenditionsMax                                               = 8
	HLSRungsMax                                                 = 6
	DimensionMin, DimensionMax                                  = 16, 7680
	VideoKbpsMin, VideoKbpsMax                                  = 100, 50000
	AudioKbpsMin, AudioKbpsMax, AudioKbpsDefault                = 32, 512, 128
	GOPSecondsMin, GOPSecondsMax, GOPSecondsDefault             = 1, 10, 2
	SegmentSecondsMin, SegmentSecondsMax, SegmentSecondsDefault = 2, 10, 6
	HLSNameDefault                                              = "hls"
	OutputNamePatternSource                                     = `^[a-z0-9][a-z0-9-]{0,31}$`
	TagNamePatternSource                                        = `^[a-z0-9][a-z0-9-]{0,62}$`
	TagValuePatternSource                                       = `^[A-Za-z0-9._-]{1,128}$`
)

// TranscodeEvent is the message's event name and the X-Enclave-Event header.
const TranscodeEvent = "transcode.finished"

var (
	outputNamePattern = regexp.MustCompile(OutputNamePatternSource)
	tagNamePattern    = regexp.MustCompile(TagNamePatternSource)
	tagValuePattern   = regexp.MustCompile(TagValuePatternSource)
	placeholder       = regexp.MustCompile(`\{(name|meta:([^{}]*))\}`)
)

// TranscodeObject is one object in the message.
type TranscodeObject struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
}

// TranscodeMessage is what a transcoder sends when a run ends (L2-012 §4).
type TranscodeMessage struct {
	Event      string            `json:"event"`
	Transcoder string            `json:"transcoder"`
	Project    string            `json:"project"`
	RunID      string            `json:"runId"`
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	Input      TranscodeObject   `json:"input"`
	Outputs    []TranscodeObject `json:"outputs"`
	HLSMaster  *string           `json:"hlsMaster"`
	Metadata   map[string]string `json:"metadata"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt time.Time         `json:"finishedAt"`
}

// HasPlaceholders reports whether an output folder uses {name} or {meta:<tag>} (TRX-07).
func HasPlaceholders(prefix string) bool { return placeholder.MatchString(prefix) }

// staticPrefix is the part of an output folder before its first placeholder.
func staticPrefix(prefix string) string {
	if i := strings.Index(prefix, "{"); i >= 0 {
		return prefix[:i]
	}
	return prefix
}

// RenderOutputPrefix fills {name} and {meta:<tag>} in an output folder (TRX-07). A tag the folder needs
// and the input lacks, or whose value can't be part of a key, is an error naming it.
func RenderOutputPrefix(prefix, name string, tags map[string]string) (string, error) {
	var missing error
	out := placeholder.ReplaceAllStringFunc(prefix, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		if sub[1] == "name" {
			return name
		}
		v, ok := tags[sub[2]]
		switch {
		case !ok || v == "":
			missing = fmt.Errorf("the input has no tag %s, which the output folder %s needs", sub[2], prefix)
		case !tagValuePattern.MatchString(v) || v == "." || v == "..":
			missing = fmt.Errorf("the input's tag %s is %q; a folder takes letters, digits, '.', '_' and '-' only", sub[2], v)
		}
		return v
	})
	return out, missing
}

func normalizeTranscodeOutputs(s TranscoderSpec) TranscoderSpec {
	for i := range s.Renditions {
		r := &s.Renditions[i]
		if r.AudioKbps == 0 {
			r.AudioKbps = AudioKbpsDefault
		}
		if r.GOPSeconds == 0 {
			r.GOPSeconds = GOPSecondsDefault
		}
	}
	if h := s.HLS; h != nil {
		l := *h
		if l.Name == "" {
			l.Name = HLSNameDefault
		}
		if l.SegmentSeconds == 0 {
			l.SegmentSeconds = SegmentSecondsDefault
		}
		if l.GOPSeconds == 0 {
			l.GOPSeconds = GOPSecondsDefault
		}
		if l.AudioKbps == 0 {
			l.AudioKbps = AudioKbpsDefault
		}
		s.HLS = &l
	}
	return s
}

func between(v, lo, hi int) bool { return v >= lo && v <= hi }

func validateBox(w, h, kbps int, f string) []FieldError {
	var errs []FieldError
	for k, v := range map[string]int{"maxWidth": w, "maxHeight": h} {
		if !between(v, DimensionMin, DimensionMax) || v%2 != 0 {
			errs = append(errs, FieldError{Field: f + "." + k, Code: FieldOutOfRange, Message: fmt.Sprintf("Use an even number from %d to %d.", DimensionMin, DimensionMax)})
		}
	}
	if !between(kbps, VideoKbpsMin, VideoKbpsMax) {
		errs = append(errs, FieldError{Field: f + ".videoKbps", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d kbps.", VideoKbpsMin, VideoKbpsMax)})
	}
	return errs
}

func validateOutputName(name, f string) []FieldError {
	if !outputNamePattern.MatchString(name) {
		return []FieldError{{Field: f, Code: FieldInvalidFormat, Message: "Use lowercase letters, digits and hyphens, up to 32 characters."}}
	}
	return nil
}

// validateTranscodeOutputs checks renditions, the ladder, notify and the output folder's placeholders
// (L3-002 §13.4); existence of the secret and topic is the service's job.
func validateTranscodeOutputs(s TranscoderSpec) []FieldError {
	var errs []FieldError
	if len(s.Presets) == 0 && len(s.Renditions) == 0 && s.HLS == nil {
		errs = append(errs, FieldError{Field: "spec.presets", Code: FieldRequired, Message: "Choose at least one preset, rendition or HLS ladder."})
	}
	files := map[string]string{} // file or folder → what writes it
	for _, p := range s.Presets {
		files[strings.Split(PresetFiles[p], "/")[0]] = "the preset " + p
	}
	if len(s.Renditions) > RenditionsMax {
		errs = append(errs, FieldError{Field: "spec.renditions", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d renditions.", RenditionsMax)})
	}
	for i, r := range s.Renditions {
		f := fmt.Sprintf("spec.renditions[%d]", i)
		errs = append(errs, validateOutputName(r.Name, f+".name")...)
		if !slices.Contains(RenditionContainers, r.Container) {
			errs = append(errs, FieldError{Field: f + ".container", Code: FieldNotAllowed, Message: "Choose mp4 or webm."})
		}
		errs = append(errs, validateBox(r.MaxWidth, r.MaxHeight, r.VideoKbps, f)...)
		if !between(r.AudioKbps, AudioKbpsMin, AudioKbpsMax) {
			errs = append(errs, FieldError{Field: f + ".audioKbps", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d kbps.", AudioKbpsMin, AudioKbpsMax)})
		}
		if !between(r.GOPSeconds, GOPSecondsMin, GOPSecondsMax) {
			errs = append(errs, FieldError{Field: f + ".gopSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", GOPSecondsMin, GOPSecondsMax)})
		}
		file := r.Name + "." + r.Container
		if who, taken := files[file]; taken {
			errs = append(errs, FieldError{Field: f + ".name", Code: FieldNotAllowed, Message: fmt.Sprintf("%s also writes %s; choose another name.", strings.ToUpper(who[:1])+who[1:], file)})
		} else if slices.IndexFunc(s.Renditions, func(o Rendition) bool { return o.Name == r.Name }) < i {
			errs = append(errs, FieldError{Field: f + ".name", Code: FieldNotAllowed, Message: "Another rendition has this name."})
		}
		files[file] = "the rendition " + r.Name
	}
	if h := s.HLS; h != nil {
		errs = append(errs, validateOutputName(h.Name, "spec.hls.name")...)
		if who, taken := files[h.Name]; taken {
			errs = append(errs, FieldError{Field: "spec.hls.name", Code: FieldNotAllowed, Message: fmt.Sprintf("%s also writes %s; choose another name.", strings.ToUpper(who[:1])+who[1:], h.Name)})
		}
		if !between(h.GOPSeconds, GOPSecondsMin, GOPSecondsMax) {
			errs = append(errs, FieldError{Field: "spec.hls.gopSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", GOPSecondsMin, GOPSecondsMax)})
		}
		switch {
		case !between(h.SegmentSeconds, SegmentSecondsMin, SegmentSecondsMax):
			errs = append(errs, FieldError{Field: "spec.hls.segmentSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", SegmentSecondsMin, SegmentSecondsMax)})
		case h.GOPSeconds > 0 && h.SegmentSeconds%h.GOPSeconds != 0:
			errs = append(errs, FieldError{Field: "spec.hls.segmentSeconds", Code: FieldNotAllowed, Message: "Use a whole number of keyframe intervals, so every segment starts on a keyframe."})
		}
		if !between(h.AudioKbps, AudioKbpsMin, AudioKbpsMax) {
			errs = append(errs, FieldError{Field: "spec.hls.audioKbps", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d kbps.", AudioKbpsMin, AudioKbpsMax)})
		}
		if len(h.Rungs) == 0 || len(h.Rungs) > HLSRungsMax {
			errs = append(errs, FieldError{Field: "spec.hls.rungs", Code: FieldOutOfRange, Message: fmt.Sprintf("Use 1–%d rungs.", HLSRungsMax)})
		}
		for i, r := range h.Rungs {
			f := fmt.Sprintf("spec.hls.rungs[%d]", i)
			errs = append(errs, validateOutputName(r.Name, f+".name")...)
			if slices.IndexFunc(h.Rungs, func(o HLSRung) bool { return o.Name == r.Name }) < i {
				errs = append(errs, FieldError{Field: f + ".name", Code: FieldNotAllowed, Message: "Another rung has this name."})
			}
			errs = append(errs, validateBox(r.MaxWidth, r.MaxHeight, r.VideoKbps, f)...)
		}
	}
	if n := s.Notify; n != nil {
		if n.Webhook == nil && n.Topic == "" {
			errs = append(errs, FieldError{Field: "spec.notify", Code: FieldRequired, Message: "Name a webhook, a topic or both."})
		}
		if w := n.Webhook; w != nil {
			if !ValidWebhookURL(w.URL) {
				errs = append(errs, FieldError{Field: "spec.notify.webhook.url", Code: FieldInvalidFormat, Message: "Use an https:// URL (http:// only to localhost)."})
			}
			if w.Secret != "" && ValidateName(w.Secret) != nil {
				errs = append(errs, FieldError{Field: "spec.notify.webhook.secret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
			}
		}
		if n.Topic != "" && ValidateName(n.Topic) != nil {
			errs = append(errs, FieldError{Field: "spec.notify.topic", Code: FieldInvalidFormat, Message: "Name an existing topic."})
		}
	}
	// {name} and {meta:<tag>} in the output folder; no other braces (TRX-07).
	for _, m := range placeholder.FindAllStringSubmatch(s.Output.Prefix, -1) {
		if m[1] != "name" && !tagNamePattern.MatchString(m[2]) {
			errs = append(errs, FieldError{Field: "spec.output.prefix", Code: FieldInvalidFormat, Message: "A tag name uses lowercase letters, digits and hyphens: {meta:video-id}."})
		}
	}
	if rest := placeholder.ReplaceAllString(s.Output.Prefix, ""); strings.ContainsAny(rest, "{}") {
		errs = append(errs, FieldError{Field: "spec.output.prefix", Code: FieldInvalidFormat, Message: "Use {name} or {meta:<tag>} in the folder; no other braces."})
	}
	return errs
}
