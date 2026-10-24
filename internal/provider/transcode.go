package provider

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// Transcoder renditions, HLS ladders and notify (L2-003 TF-49, L2-012). Defaults the server fills stay
// null in state while the configuration leaves them unset, so a plan after apply is empty.

type renditionModel struct {
	Name       types.String `tfsdk:"name"`
	Container  types.String `tfsdk:"container"`
	MaxWidth   types.Int64  `tfsdk:"max_width"`
	MaxHeight  types.Int64  `tfsdk:"max_height"`
	VideoKbps  types.Int64  `tfsdk:"video_kbps"`
	AudioKbps  types.Int64  `tfsdk:"audio_kbps"`
	GOPSeconds types.Int64  `tfsdk:"gop_seconds"`
}

type rungModel struct {
	Name      types.String `tfsdk:"name"`
	MaxWidth  types.Int64  `tfsdk:"max_width"`
	MaxHeight types.Int64  `tfsdk:"max_height"`
	VideoKbps types.Int64  `tfsdk:"video_kbps"`
}

type hlsModel struct {
	Name           types.String `tfsdk:"name"`
	SegmentSeconds types.Int64  `tfsdk:"segment_seconds"`
	GOPSeconds     types.Int64  `tfsdk:"gop_seconds"`
	AudioKbps      types.Int64  `tfsdk:"audio_kbps"`
	Rungs          []rungModel  `tfsdk:"rungs"`
}

type notifyModel struct {
	WebhookURL    types.String `tfsdk:"webhook_url"`
	WebhookSecret types.String `tfsdk:"webhook_secret"`
	Topic         types.String `tfsdk:"topic"`
}

func boxAttrs(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	dim := []validator.Int64{int64validator.Between(api.DimensionMin, api.DimensionMax)}
	attrs["name"] = schema.StringAttribute{Required: true, Description: "Lowercase letters, digits and hyphens, up to 32 characters.",
		Validators: []validator.String{stringvalidator.RegexMatches(outputNamePattern, "must be lowercase letters, digits and hyphens, up to 32 characters")}}
	attrs["max_width"] = schema.Int64Attribute{Required: true, Description: "The widest the video may be, an even number of pixels; it keeps its aspect ratio and is never scaled up.", Validators: dim}
	attrs["max_height"] = schema.Int64Attribute{Required: true, Description: "The tallest the video may be, an even number of pixels.", Validators: dim}
	attrs["video_kbps"] = schema.Int64Attribute{Required: true, Description: fmt.Sprintf("The video bitrate ceiling, %d–%d kbps.", api.VideoKbpsMin, api.VideoKbpsMax),
		Validators: []validator.Int64{int64validator.Between(api.VideoKbpsMin, api.VideoKbpsMax)}}
	return attrs
}

var outputNamePattern = regexp.MustCompile(api.OutputNamePatternSource)

func withTranscodeOutputs(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	attrs["renditions"] = schema.ListNestedAttribute{Optional: true, Description: "Files of your own size and bitrate, each <name>.mp4 or <name>.webm in the run's folder.",
		Validators: []validator.List{listvalidator.SizeAtMost(api.RenditionsMax)},
		NestedObject: schema.NestedAttributeObject{Attributes: boxAttrs(map[string]schema.Attribute{
			"container": schema.StringAttribute{Required: true, Description: "mp4 (H.264 and AAC) or webm (VP9 and Opus).",
				Validators: []validator.String{stringvalidator.OneOf(api.RenditionContainers...)}},
			"audio_kbps":  optInt(fmt.Sprintf("Audio bitrate, %d–%d kbps (default %d).", api.AudioKbpsMin, api.AudioKbpsMax, api.AudioKbpsDefault)),
			"gop_seconds": optInt(fmt.Sprintf("Seconds between keyframes, %d–%d (default %d).", api.GOPSecondsMin, api.GOPSecondsMax, api.GOPSecondsDefault)),
		})}}
	attrs["hls"] = schema.SingleNestedAttribute{Optional: true, Description: "An adaptive HLS stream: <name>/master.m3u8 and each rung's playlist and segments, with keyframes at the same moments in every rung.",
		Attributes: map[string]schema.Attribute{
			"name":            optString(`The ladder's folder (default "hls").`),
			"segment_seconds": optInt(fmt.Sprintf("Seconds per segment, %d–%d and a multiple of gop_seconds (default %d).", api.SegmentSecondsMin, api.SegmentSecondsMax, api.SegmentSecondsDefault)),
			"gop_seconds":     optInt(fmt.Sprintf("Seconds between keyframes, %d–%d (default %d).", api.GOPSecondsMin, api.GOPSecondsMax, api.GOPSecondsDefault)),
			"audio_kbps":      optInt(fmt.Sprintf("Audio bitrate of every rung, %d–%d kbps (default %d).", api.AudioKbpsMin, api.AudioKbpsMax, api.AudioKbpsDefault)),
			"rungs": schema.ListNestedAttribute{Required: true, Description: "The qualities, lowest first by convention.",
				Validators:   []validator.List{listvalidator.SizeBetween(1, api.HLSRungsMax)},
				NestedObject: schema.NestedAttributeObject{Attributes: boxAttrs(map[string]schema.Attribute{})}},
		}}
	attrs["notify"] = schema.SingleNestedAttribute{Optional: true, Description: "Where a JSON message goes when each file is done, failed or timed out.",
		Attributes: map[string]schema.Attribute{
			"webhook_url":    optString("An https:// URL (http:// only to localhost), tried 3 times."),
			"webhook_secret": optString("A secret of the project whose value signs the body (X-Enclave-Signature: sha256=<HMAC>)."),
			"topic":          optString("A topic of the project the message is published to."),
		}}
	return attrs
}

func renditionsOf(ms []renditionModel) any {
	if len(ms) == 0 {
		return nil
	}
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = map[string]any{"name": m.Name.ValueString(), "container": m.Container.ValueString(), "maxWidth": m.MaxWidth.ValueInt64(),
			"maxHeight": m.MaxHeight.ValueInt64(), "videoKbps": m.VideoKbps.ValueInt64(), "audioKbps": intOrNil(m.AudioKbps), "gopSeconds": intOrNil(m.GOPSeconds)}
	}
	return out
}

func hlsOf(m *hlsModel) any {
	if m == nil {
		return nil
	}
	rungs := make([]api.HLSRung, len(m.Rungs))
	for i, r := range m.Rungs {
		rungs[i] = api.HLSRung{Name: r.Name.ValueString(), MaxWidth: int(r.MaxWidth.ValueInt64()), MaxHeight: int(r.MaxHeight.ValueInt64()), VideoKbps: int(r.VideoKbps.ValueInt64())}
	}
	return map[string]any{"name": strOrNil(m.Name), "segmentSeconds": intOrNil(m.SegmentSeconds), "gopSeconds": intOrNil(m.GOPSeconds), "audioKbps": intOrNil(m.AudioKbps), "rungs": rungs}
}

func notifyOf(m *notifyModel) any {
	if m == nil {
		return nil
	}
	n := map[string]any{"topic": strOrNil(m.Topic)}
	if u := strOrNil(m.WebhookURL); u != nil {
		n["webhook"] = map[string]any{"url": u, "secret": strOrNil(m.WebhookSecret)}
	} else if s := strOrNil(m.WebhookSecret); s != nil {
		n["webhook"] = map[string]any{"secret": s} // refused by the server, naming the field
	}
	return n
}

func renditionModels(prior []renditionModel, rs []api.Rendition) []renditionModel {
	if len(rs) == 0 {
		return nil
	}
	out := make([]renditionModel, len(rs))
	for i, r := range rs {
		var p renditionModel
		if i < len(prior) {
			p = prior[i]
		}
		out[i] = renditionModel{Name: types.StringValue(r.Name), Container: types.StringValue(r.Container), MaxWidth: types.Int64Value(int64(r.MaxWidth)),
			MaxHeight: types.Int64Value(int64(r.MaxHeight)), VideoKbps: types.Int64Value(int64(r.VideoKbps)),
			AudioKbps: defaultKeep(p.AudioKbps, r.AudioKbps, api.AudioKbpsDefault), GOPSeconds: defaultKeep(p.GOPSeconds, r.GOPSeconds, api.GOPSecondsDefault)}
	}
	return out
}

func hlsModelOf(prior *hlsModel, h *api.HLSLadder) *hlsModel {
	if h == nil {
		return nil
	}
	p := hlsModel{Name: types.StringNull(), SegmentSeconds: types.Int64Null(), GOPSeconds: types.Int64Null(), AudioKbps: types.Int64Null()}
	if prior != nil {
		p = *prior
	}
	m := &hlsModel{Name: types.StringValue(h.Name), SegmentSeconds: defaultKeep(p.SegmentSeconds, h.SegmentSeconds, api.SegmentSecondsDefault),
		GOPSeconds: defaultKeep(p.GOPSeconds, h.GOPSeconds, api.GOPSecondsDefault), AudioKbps: defaultKeep(p.AudioKbps, h.AudioKbps, api.AudioKbpsDefault)}
	if (p.Name.IsNull() || p.Name.IsUnknown()) && h.Name == api.HLSNameDefault {
		m.Name = types.StringNull()
	}
	for _, r := range h.Rungs {
		m.Rungs = append(m.Rungs, rungModel{Name: types.StringValue(r.Name), MaxWidth: types.Int64Value(int64(r.MaxWidth)), MaxHeight: types.Int64Value(int64(r.MaxHeight)), VideoKbps: types.Int64Value(int64(r.VideoKbps))})
	}
	return m
}

func notifyModelOf(n *api.TranscodeNotify) *notifyModel {
	if n == nil {
		return nil
	}
	m := &notifyModel{WebhookURL: types.StringNull(), WebhookSecret: types.StringNull(), Topic: types.StringNull()}
	if n.Webhook != nil {
		m.WebhookURL = types.StringValue(n.Webhook.URL)
		if n.Webhook.Secret != "" {
			m.WebhookSecret = types.StringValue(n.Webhook.Secret)
		}
	}
	if n.Topic != "" {
		m.Topic = types.StringValue(n.Topic)
	}
	return m
}
