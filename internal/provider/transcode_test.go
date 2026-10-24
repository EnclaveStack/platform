package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/enclavestack/platform/pkg/api"
)

// renditions, hls and notify on enclave_transcoder with real OpenTofu (L2-003 TF-49, L2-012).
func TestAccTranscoderRenditionsLadderAndNotify(t *testing.T) {
	_, c := boot(t)
	config := func(body string) string {
		return `
resource "enclave_bucket" "media" { name = "media" }
resource "enclave_secret" "hook" {
  name  = "hook-key"
  value = "s3cret"
}
resource "enclave_topic" "events" { name = "video-events" }
resource "enclave_transcoder" "videos" {
  name   = "videos"
  input  = { bucket = enclave_bucket.media.name, prefix = "incoming/" }
  output = { bucket = enclave_bucket.media.name, prefix = "processed/{meta:video-id}/" }
` + body + `
}
`
	}
	full := `
  renditions = [{ name = "selfie", container = "mp4", max_width = 720, max_height = 1280, video_kbps = 2500 }]
  hls = {
    rungs = [
      { name = "360p", max_width = 360, max_height = 640, video_kbps = 600 },
      { name = "1080p", max_width = 1080, max_height = 1920, video_kbps = 4000 },
    ]
  }
  notify = {
    webhook_url    = "https://video.example.com/done"
    webhook_secret = enclave_secret.hook.name
    topic          = enclave_topic.events.name
  }`
	spec := func(check func(api.TranscoderSpec) error) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			r, err := c.GetResource(context.Background(), s.RootModule().Resources["enclave_transcoder.videos"].Primary.ID)
			if err != nil {
				return err
			}
			var ts api.TranscoderSpec
			if err := json.Unmarshal(r.Spec, &ts); err != nil {
				return err
			}
			return check(ts)
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(full),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_transcoder.videos", "renditions.0.name", "selfie"),
					resource.TestCheckNoResourceAttr("enclave_transcoder.videos", "renditions.0.audio_kbps"), // the default stays out of state
					resource.TestCheckResourceAttr("enclave_transcoder.videos", "hls.rungs.#", "2"),
					resource.TestCheckResourceAttr("enclave_transcoder.videos", "notify.topic", "video-events"),
					resource.TestCheckNoResourceAttr("enclave_transcoder.videos", "presets"),
					spec(func(s api.TranscoderSpec) error {
						if s.HLS.Name != "hls" || s.HLS.SegmentSeconds != 6 || s.Renditions[0].AudioKbps != 128 || s.Notify.Webhook.Secret != "hook-key" {
							return fmt.Errorf("server spec %+v %+v", s.HLS, s.Notify)
						}
						return nil
					}),
				),
			},
			{ResourceName: "enclave_transcoder.videos", ImportState: true, ImportStateId: "videos", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "hls.name", "hls.segment_seconds", "hls.gop_seconds", "hls.audio_kbps", "renditions.0.audio_kbps", "renditions.0.gop_seconds"}},
			{
				// Change in place: a ladder setting, notify without the topic, a preset instead of the rendition.
				Config: config(`
  presets = ["thumbnail"]
  hls = {
    gop_seconds     = 3
    segment_seconds = 9
    rungs           = [{ name = "720p", max_width = 720, max_height = 1280, video_kbps = 2000 }]
  }
  notify = { webhook_url = "https://video.example.com/done" }`),
				Check: spec(func(s api.TranscoderSpec) error {
					if len(s.Renditions) != 0 || len(s.Presets) != 1 || s.HLS.GOPSeconds != 3 || len(s.HLS.Rungs) != 1 || s.Notify.Topic != "" || s.Notify.Webhook.Secret != "" {
						return fmt.Errorf("after update %+v", s)
					}
					return nil
				}),
			},
			{
				Config:      config(`  hls = { segment_seconds = 5, rungs = [{ name = "720p", max_width = 720, max_height = 1280, video_kbps = 2000 }] }`),
				ExpectError: regexp.MustCompile(`(?s)segmentSeconds.*keyframe`),
			},
			{
				Config:      config(`  renditions = [{ name = "Big", container = "mp4", max_width = 720, max_height = 1280, video_kbps = 2500 }]`),
				ExpectError: regexp.MustCompile(`lowercase letters`),
			},
			{
				// Back to a valid configuration so the test can destroy what it made.
				Config: config(`  presets = ["mp3"]`),
				Check: spec(func(s api.TranscoderSpec) error {
					if s.HLS != nil || s.Notify != nil || len(s.Presets) != 1 {
						return fmt.Errorf("after clearing %+v", s)
					}
					return nil
				}),
			},
		},
	})
}
