package fake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/transcode"
)

// v0.6 (ADR-029 – ADR-034): the broker, CDN caches, public buckets and transcodes, in memory.

// BrokerAddress is where the fake broker "listens".
const BrokerAddress = "127.0.0.1:44222"

type dataState struct {
	brokerConfig  []byte
	brokerUp      bool
	brokerReloads int
	brokerDown    bool
	caches        map[string]int64 // CDN resource ID → cached bytes
	purges        map[string]int
	publicBuckets map[string]bool
	transcodes    []engine.TranscodeRun
}

func (d *Driver) data6() *dataState {
	if d.v6 == nil {
		d.v6 = &dataState{caches: map[string]int64{}, purges: map[string]int{}, publicBuckets: map[string]bool{}}
	}
	return d.v6
}

// ConfigureBroker implements engine.Driver.
func (d *Driver) ConfigureBroker(ctx context.Context, cfg engine.BrokerConfig) (engine.BrokerStatus, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.BrokerStatus{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.data6()
	if cfg.Empty {
		st.brokerConfig, st.brokerUp = nil, false
		return engine.BrokerStatus{Message: "No queue or topic exists, so the broker isn't running."}, nil
	}
	if !bytes.Equal(st.brokerConfig, cfg.Config) {
		st.brokerReloads++
	}
	st.brokerConfig, st.brokerUp, st.brokerDown = cfg.Config, true, false
	return engine.BrokerStatus{Available: true, Address: BrokerAddress}, nil
}

// BrokerStatus implements engine.Driver.
func (d *Driver) BrokerStatus(context.Context) engine.BrokerStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.data6()
	switch {
	case st.brokerUp && !st.brokerDown:
		return engine.BrokerStatus{Available: true, Address: BrokerAddress}
	case st.brokerDown:
		return engine.BrokerStatus{Message: "The broker container isn't running."}
	}
	return engine.BrokerStatus{Message: "No queue or topic exists, so the broker isn't running."}
}

// StopBroker simulates the broker container stopped outside EnclaveStack (drift).
func (d *Driver) StopBroker() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data6().brokerDown = true
}

// BrokerConfig returns the configuration the broker was last given, and how many times it changed.
func (d *Driver) BrokerConfig() ([]byte, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data6().brokerConfig, d.data6().brokerReloads
}

// PurgeCache implements engine.Driver.
func (d *Driver) PurgeCache(ctx context.Context, r *domain.Resource) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.data6()
	st.caches[r.ID] = 0
	st.purges[r.ID]++
	return nil
}

// Purges returns how many times a CDN's cache was emptied.
func (d *Driver) Purges(id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data6().purges[id]
}

// SetCached sets the bytes a CDN's cache holds.
func (d *Driver) SetCached(id string, n int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data6().caches[id] = n
}

// CacheUsage implements engine.Driver.
func (d *Driver) CacheUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	if err := d.Ping(ctx); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data6().caches[r.ID], nil
}

// SetBucketPublicRead implements engine.Driver.
func (d *Driver) SetBucketPublicRead(ctx context.Context, bucket string, public bool, _ engine.ObjectStore) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if public {
		d.data6().publicBuckets[bucket] = true
	} else {
		delete(d.data6().publicBuckets, bucket)
	}
	return nil
}

// BucketPublic reports whether a bucket is readable without credentials.
func (d *Driver) BucketPublic(bucket string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data6().publicBuckets[bucket]
}

// NotMedia is what an input starts with when the fake transcode should fail as FFmpeg does on a file
// that isn't media.
const NotMedia = "NOT MEDIA"

// Transcode implements engine.Driver: every preset writes a small file named as FFmpeg's would be, with
// the input's size in it; HLS writes a playlist and one segment.
func (d *Driver) Transcode(ctx context.Context, run engine.TranscodeRun, src io.Reader, out func(name string, size int64, r io.Reader) error) (engine.JobResult, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.JobResult{}, err
	}
	in, err := io.ReadAll(src)
	if err != nil {
		return engine.JobResult{}, err
	}
	d.mu.Lock()
	d.data6().transcodes = append(d.data6().transcodes, run)
	d.mu.Unlock()
	if err := d.sleep(ctx); err != nil {
		return engine.JobResult{}, err
	}
	var log strings.Builder
	for _, p := range run.Presets {
		fmt.Fprintf(&log, "== %s\n", p)
		if bytes.HasPrefix(in, []byte(NotMedia)) {
			fmt.Fprintf(&log, "%s/%s: Invalid data found when processing input\n", transcode.InDir, run.InputName)
			return engine.JobResult{ExitCode: 1, Output: []byte(log.String())}, nil
		}
		files := []string{transcode.File(p)}
		if strings.HasSuffix(files[0], ".m3u8") {
			files = append(files, strings.TrimSuffix(files[0], "index.m3u8")+"segment-000.ts")
		}
		for _, f := range files {
			body := fmt.Sprintf("%s of %d bytes\n", p, len(in))
			if err := out(f, int64(len(body)), strings.NewReader(body)); err != nil {
				return engine.JobResult{}, err
			}
		}
	}
	return engine.JobResult{Output: []byte(log.String())}, nil
}

// Transcodes returns the runs the fake engine converted.
func (d *Driver) Transcodes() []engine.TranscodeRun {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]engine.TranscodeRun(nil), d.data6().transcodes...)
}
