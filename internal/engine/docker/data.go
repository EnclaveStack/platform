package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/transcode"
)

// v0.6 (ADR-029, ADR-033, ADR-034): the broker container, CDN caches, public bucket reads and transcodes.

const (
	brokerConfigDir  = "/etc/nats"
	brokerConfigFile = "enclave.conf"
	brokerMemoryMiB  = domain.BrokerMemoryMiB
)

// ConfigureBroker implements engine.Driver.
func (d *Driver) ConfigureBroker(ctx context.Context, cfg engine.BrokerConfig) (engine.BrokerStatus, error) {
	d.brokerMu.Lock()
	defer d.brokerMu.Unlock()
	c, err := d.inspect(ctx, engine.BrokerName)
	if err != nil {
		return engine.BrokerStatus{}, err
	}
	if cfg.Empty {
		if err := d.remove(ctx, engine.BrokerName); err != nil {
			return engine.BrokerStatus{}, err
		}
		vctx, cancel := context.WithTimeout(ctx, stepTimeout)
		defer cancel()
		if err := d.cli.VolumeRemove(vctx, engine.BrokerVolume, true); err != nil && !isNotFound(err) {
			return engine.BrokerStatus{}, d.classify(fmt.Errorf("remove broker volume: %w", err))
		}
		d.brokerConf = nil
		return d.brokerStatus(ctx), nil
	}
	keep := 0
	if c != nil && (c.Config == nil || c.Config.Image != d.opts.BrokerImage) {
		keep = hostPort(c, engine.BrokerPort)
		if err := d.remove(ctx, engine.BrokerName); err != nil {
			return engine.BrokerStatus{}, err
		}
		c = nil
	}
	if c == nil {
		if err := d.createBroker(ctx, keep); err != nil {
			return engine.BrokerStatus{}, err
		}
		d.brokerConf = nil
	}
	if !bytes.Equal(d.brokerConf, cfg.Config) {
		if err := d.copyFiles(ctx, engine.BrokerName, brokerConfigDir, map[string][]byte{brokerConfigFile: cfg.Config}, 0o600); err != nil {
			return engine.BrokerStatus{}, err
		}
	}
	running := c != nil && c.State != nil && c.State.Running
	switch {
	case !running:
		if err := d.start(ctx, engine.BrokerName); err != nil {
			return engine.BrokerStatus{}, err
		}
		if err := d.waitStable(ctx, engine.BrokerName, time.Second); err != nil {
			return engine.BrokerStatus{}, fmt.Errorf("the broker stopped right after starting: %w. Last output: %s", err, d.tail(ctx, engine.BrokerName, 5))
		}
	case !bytes.Equal(d.brokerConf, cfg.Config):
		// NATS reloads users and permissions on SIGHUP without dropping connections (DAR-021).
		kctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.ContainerKill(kctx, engine.BrokerName, "HUP")
		cancel()
		if err != nil {
			return engine.BrokerStatus{}, d.classify(fmt.Errorf("reload the broker: %w", err))
		}
	}
	d.brokerConf = cfg.Config
	st := d.brokerStatus(ctx)
	if !st.Available {
		return st, errors.New(st.Message)
	}
	return st, nil
}

func (d *Driver) createBroker(ctx context.Context, hostPortWanted int) error {
	if err := d.ensureNetwork(ctx); err != nil {
		return err
	}
	if err := d.ensureImage(ctx, d.opts.BrokerImage, false); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	_, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: engine.BrokerVolume, Labels: map[string]string{LabelManaged: "true"}})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("create broker volume: %w", err))
	}
	port := nat.Port(strconv.Itoa(engine.BrokerPort) + "/tcp")
	hp, err := d.hostPortFor(hostPortWanted)
	if err != nil {
		return err
	}
	cfg := &container.Config{Image: d.opts.BrokerImage, Cmd: []string{"-c", brokerConfigDir + "/" + brokerConfigFile},
		Labels: map[string]string{LabelManaged: "true", LabelKind: "system"}, ExposedPorts: nat.PortSet{port: {}}}
	host := &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources: container.Resources{Memory: brokerMemoryMiB << 20}, Binds: []string{engine.BrokerVolume + ":/data"},
		PortBindings: nat.PortMap{port: {{HostIP: d.opts.PublishAddress, HostPort: hp}}}} // never the monitoring port
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {Aliases: []string{engine.BrokerName}}}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, engine.BrokerName); err != nil {
		return d.classify(fmt.Errorf("create broker: %w", err))
	}
	return d.joinManagedNetworks(ctx, engine.BrokerName) // apps in every network reach it (ADR-029)
}

// BrokerStatus implements engine.Driver.
func (d *Driver) BrokerStatus(ctx context.Context) engine.BrokerStatus {
	return d.brokerStatus(ctx)
}

func (d *Driver) brokerStatus(ctx context.Context) engine.BrokerStatus {
	c, err := d.inspect(ctx, engine.BrokerName)
	switch {
	case err != nil:
		return engine.BrokerStatus{Message: "The container engine isn't reachable."}
	case c == nil:
		return engine.BrokerStatus{Message: "No queue or topic exists, so the broker isn't running."}
	case c.State == nil || !c.State.Running:
		return engine.BrokerStatus{Message: "The broker container isn't running."}
	}
	hp := hostPort(c, engine.BrokerPort)
	if hp == 0 {
		return engine.BrokerStatus{Message: "The broker has no port on this machine yet."}
	}
	host := d.opts.PublishAddress
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return engine.BrokerStatus{Available: true, Address: host + ":" + strconv.Itoa(hp)}
}

// PurgeCache implements engine.Driver.
func (d *Driver) PurgeCache(ctx context.Context, r *domain.Resource) error {
	res, err := d.exec(ctx, engine.ContainerName(r), []string{"find", "/cache", "-type", "f", "-delete"}, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("emptying the cache failed: %s", res.Output)
	}
	return nil
}

// CacheUsage implements engine.Driver.
func (d *Driver) CacheUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	res, err := d.exec(ctx, engine.ContainerName(r), []string{"du", "-sk", "/cache"}, nil)
	if err != nil {
		return 0, err
	}
	kib, err := strconv.ParseInt(strings.Fields(res.Output + " x")[0], 10, 64)
	if res.ExitCode != 0 || err != nil {
		return 0, fmt.Errorf("measuring the cache failed: %s", res.Output)
	}
	return kib << 10, nil
}

// SetBucketPublicRead implements engine.Driver: an anonymous policy allowing GetObject only (CDN-02).
func (d *Driver) SetBucketPublicRead(ctx context.Context, bucket string, public bool, store engine.ObjectStore) error {
	mc, _, err := d.ensureObjectStore(ctx, store)
	if err != nil {
		return err
	}
	policy := ""
	if public {
		policy = fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := mc.SetBucketPolicy(pctx, bucket, policy); err != nil {
		return fmt.Errorf("set the policy of bucket %s: %w", bucket, err)
	}
	return nil
}

// Transcode implements engine.Driver (ADR-034): a container without network, the input copied in as a
// one-file tar streamed from src, /out copied out and each file handed to out.
func (d *Driver) Transcode(ctx context.Context, run engine.TranscodeRun, src io.Reader, out func(name string, size int64, r io.Reader) error) (engine.JobResult, error) {
	if err := d.ensureImage(ctx, d.opts.TranscodeImage, false); err != nil {
		return engine.JobResult{}, err
	}
	name := "es-transcode-" + strings.TrimPrefix(run.RunID, "run_")
	_ = d.remove(ctx, name) // a leftover of an interrupted attempt
	cfg := &container.Config{Image: d.opts.TranscodeImage, Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{transcode.Script(run.InputName, run.Presets)},
		Labels: map[string]string{LabelManaged: "true", LabelResourceID: run.ResourceID, LabelKind: "transcode", LabelRun: run.RunID}}
	host := &container.HostConfig{NetworkMode: "none", Resources: container.Resources{Memory: int64(run.MemoryMiB) << 20}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	created, err := d.cli.ContainerCreate(cctx, cfg, host, nil, nil, name)
	cancel()
	if err != nil {
		return engine.JobResult{}, d.classify(fmt.Errorf("create transcode container: %w", err))
	}
	defer func() {
		if err := d.remove(context.WithoutCancel(ctx), created.ID); err != nil {
			d.log.Warn("remove transcode container", "run_id", run.RunID, "error", err)
		}
	}()
	if err := d.copyIn(ctx, created.ID, run.InputName, run.InputSize, src); err != nil {
		return engine.JobResult{}, err
	}
	if err := d.start(ctx, created.ID); err != nil {
		return engine.JobResult{}, err
	}
	var res engine.JobResult
	wctx, wcancel := context.WithTimeout(ctx, run.Timeout)
	defer wcancel()
	statusCh, errCh := d.cli.ContainerWait(wctx, created.ID, container.WaitConditionNotRunning)
	select {
	case st := <-statusCh:
		res.ExitCode = int(st.StatusCode)
	case err := <-errCh:
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if wctx.Err() == nil {
			return res, d.classify(fmt.Errorf("wait for transcode: %w", err))
		}
		res.TimedOut, res.ExitCode = true, 137
	}
	res.Output = d.jobOutput(context.WithoutCancel(ctx), created.ID)
	if res.ExitCode != 0 || res.TimedOut {
		return res, nil
	}
	return res, d.copyOut(ctx, created.ID, out)
}

// copyIn streams one file into the container's input folder.
func (d *Driver) copyIn(ctx context.Context, id, name string, size int64, src io.Reader) error {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(transcode.InDir, "/") + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		if err == nil {
			err = tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(transcode.InDir, "/") + "/" + name, Mode: 0o644, Size: size})
		}
		if err == nil {
			var n int64
			n, err = io.Copy(tw, src)
			if err == nil && n != size {
				err = fmt.Errorf("the input was %d bytes, expected %d", n, size)
			}
		}
		if err == nil {
			err = tw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	if err := d.cli.CopyToContainer(ctx, id, "/", pr, container.CopyToContainerOptions{}); err != nil {
		_ = pr.CloseWithError(err)
		return d.classify(fmt.Errorf("copy the input in: %w", err))
	}
	return nil
}

// copyOut streams the output folder out and hands each regular file to out, by its path below it.
func (d *Driver) copyOut(ctx context.Context, id string, out func(name string, size int64, r io.Reader) error) error {
	rc, _, err := d.cli.CopyFromContainer(ctx, id, transcode.OutDir)
	if err != nil {
		return d.classify(fmt.Errorf("copy the outputs out: %w", err))
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	base := path.Base(transcode.OutDir) + "/"
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the outputs: %w", err)
		}
		rel := strings.TrimPrefix(h.Name, base)
		if h.Typeflag != tar.TypeReg || rel == "" || strings.Contains(rel, "..") {
			continue
		}
		if err := out(rel, h.Size, tr); err != nil {
			return err
		}
	}
}
