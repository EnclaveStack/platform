package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/vmlink"
	"github.com/enclavestack/platform/pkg/api"
)

// VM links (ADR-026): one container per VM in the VM's network, carrying its private IP, DNS names and
// published ports. QEMU on the host streams the VM's frames to it over a loopback port, and es-vmlink
// inside moves them to a tap device.

const (
	labelLinkConfig = "enclave.vmlink"
	linkStartWait   = 5 * time.Second
)

// EnsureVMLink implements engine.Driver.
func (d *Driver) EnsureVMLink(ctx context.Context, in engine.EnsureInput) (engine.Outputs, error) {
	r := in.Resource
	spec, ok := in.Spec.(api.VMSpec)
	if !ok {
		return nil, engine.Permanent(fmt.Errorf("a VM link needs a VM spec"))
	}
	if err := d.ensureVMLinkImage(ctx); err != nil {
		return nil, err
	}
	netName := in.Placement.NetworkName()
	if netName == engine.NetworkName {
		if err := d.ensureNetwork(ctx); err != nil {
			return nil, err
		}
	}
	data, err := d.cli.NetworkInspect(ctx, netName, network.InspectOptions{})
	if err != nil {
		return nil, d.classify(fmt.Errorf("inspect network %s: %w", netName, err))
	}
	if len(data.IPAM.Config) == 0 {
		return nil, fmt.Errorf("the network %s has no address range yet", netName)
	}
	internal := data.Internal
	gateways := []string{data.IPAM.Config[0].Gateway}
	if internal {
		mgmt, err := d.ensureVMLinkNetwork(ctx)
		if err != nil {
			return nil, err
		}
		gateways = append(gateways, mgmt)
	}
	mgmtPorts := []int{api.SSHPort}
	if !internal {
		mgmtPorts = nil
	}
	if internal {
		mgmtPorts = append(mgmtPorts, spec.Publish...)
	}
	env := []string{"ES_DATA_CIDR=" + data.IPAM.Config[0].Subnet, "ES_GATEWAYS=" + strings.Join(gateways, ","), "ES_MGMT_PORTS=" + joinInts(mgmtPorts)}
	// The container is replaced when anything it was created with changes (placement, published ports,
	// the helper image); its host ports stay (in.HostPorts).
	key := sha256.Sum256([]byte(fmt.Sprint(vmlink.ImageTag(), in.Placement, spec.Publish, env, d.opts.PublishAddress)))
	config := hex.EncodeToString(key[:8])
	name := engine.ContainerName(r)
	existing, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	keep := map[int]int{}
	for k, v := range in.HostPorts {
		keep[k] = v
	}
	if existing != nil {
		for _, p := range []int{engine.VMLinkFramePort, api.SSHPort} {
			if keep[p] == 0 {
				keep[p] = hostPort(existing, p)
			}
		}
		running := existing.State != nil && existing.State.Running
		if !running || existing.Config.Labels[labelLinkConfig] != config || existing.Config.Labels[LabelResourceID] != r.ID {
			if err := d.remove(ctx, name); err != nil {
				return nil, err
			}
			existing = nil
		}
	}
	if existing == nil {
		if err := d.createVMLink(ctx, r, name, spec, in.Placement, internal, env, config, keep); err != nil {
			return nil, err
		}
		if err := d.applyToNew(ctx, in, name); err != nil {
			return nil, err
		}
		if err := started(ctx, in); err != nil {
			return nil, err
		}
	}
	c, err := d.inspect(ctx, name)
	if err != nil || c == nil {
		return nil, fmt.Errorf("the VM link %s vanished after it started", name)
	}
	out := engine.Outputs{engine.OutputPrivateDNS: engine.PrivateDNS(r), engine.OutputLinkPort: hostPort(c, engine.VMLinkFramePort),
		engine.OutputSSHAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort(c, api.SSHPort)))}
	if ep := c.NetworkSettings.Networks[netName]; ep != nil && ep.IPAddress != "" {
		out[engine.OutputPrivateIP] = ep.IPAddress
	}
	if len(spec.Publish) > 0 {
		out[engine.OutputHost] = d.opts.PublishAddress
	}
	return out, nil
}

func (d *Driver) createVMLink(ctx context.Context, r *domain.Resource, name string, spec api.VMSpec, p engine.Placement, internal bool, env []string, config string, keep map[int]int) error {
	frame, ssh := nat.Port(strconv.Itoa(engine.VMLinkFramePort)+"/tcp"), nat.Port(strconv.Itoa(api.SSHPort)+"/tcp")
	var bindErr error
	bind := func(addr string, port int) []nat.PortBinding {
		hp, err := hostPortOn(addr, port) // fixed now, so a restarted link keeps its ports (QEMU reconnects to the frame port)
		if err != nil {
			bindErr = err
		}
		return []nat.PortBinding{{HostIP: addr, HostPort: hp}}
	}
	cfg := &container.Config{Image: vmlink.ImageTag(), Env: env, ExposedPorts: nat.PortSet{frame: {}, ssh: {}},
		Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: string(r.Kind), labelLinkConfig: config}}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		PortBindings:  nat.PortMap{frame: bind("127.0.0.1", keep[engine.VMLinkFramePort]), ssh: bind("127.0.0.1", keep[api.SSHPort])},
		CapDrop:       []string{"ALL"},
		CapAdd:        []string{"NET_ADMIN"},
		Resources:     container.Resources{Memory: 64 << 20, Devices: []container.DeviceMapping{{PathOnHost: "/dev/net/tun", PathInContainer: "/dev/net/tun", CgroupPermissions: "rwm"}}},
		Sysctls:       map[string]string{"net.ipv4.ip_forward": "1"},
		// Never privileged, never host network, no mounts (L3-006 §7).
	}
	for _, port := range spec.Publish {
		pp := nat.Port(strconv.Itoa(port) + "/tcp")
		cfg.ExposedPorts[pp] = struct{}{}
		host.PortBindings[pp] = bind(d.opts.PublishAddress, port)
	}
	if bindErr != nil {
		return bindErr
	}
	netCfg, err := d.endpoint(ctx, p, []string{name, engine.PrivateDNS(r)})
	if err != nil {
		return err
	}
	if internal {
		// Docker publishes nothing from an internal network, so the frame and SSH ports come through
		// es-vmlink, which becomes the container's gateway network; es-vmlink drops the VM's own traffic.
		netCfg.EndpointsConfig[engine.VMLinkNetwork] = &network.EndpointSettings{GwPriority: 1}
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	_, err = d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, name)
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("create VM link %s: %w", name, err))
	}
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	err = d.cli.ContainerStart(sctx, name, container.StartOptions{})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("start VM link %s: %w", name, err))
	}
	// es-vmlink exits at once on a setup error; give it a moment to fail loudly.
	deadline := time.Now().Add(linkStartWait)
	for time.Now().Before(deadline) {
		c, err := d.inspect(ctx, name)
		if err != nil {
			return err
		}
		if c == nil || c.State == nil || !c.State.Running {
			return fmt.Errorf("the VM link stopped: %s", d.tail(ctx, name, 5))
		}
		if strings.Contains(d.tail(ctx, name, 5), "vmlink: ready") {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the VM link didn't get ready within %s: %s", linkStartWait, d.tail(ctx, name, 5))
}

// DeleteVMLink implements engine.Driver.
func (d *Driver) DeleteVMLink(ctx context.Context, r *domain.Resource) error {
	return d.remove(ctx, engine.ContainerName(r))
}

// ensureVMLinkNetwork makes sure es-vmlink exists and returns its gateway.
func (d *Driver) ensureVMLinkNetwork(ctx context.Context) (string, error) {
	n, err := d.cli.NetworkInspect(ctx, engine.VMLinkNetwork, network.InspectOptions{})
	if cerrdefs.IsNotFound(err) {
		_, err = d.cli.NetworkCreate(ctx, engine.VMLinkNetwork, network.CreateOptions{Driver: "bridge", Labels: map[string]string{LabelManaged: "true"}})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return "", d.classify(fmt.Errorf("create %s: %w", engine.VMLinkNetwork, err))
		}
		n, err = d.cli.NetworkInspect(ctx, engine.VMLinkNetwork, network.InspectOptions{})
	}
	if err != nil {
		return "", d.classify(fmt.Errorf("inspect %s: %w", engine.VMLinkNetwork, err))
	}
	if len(n.IPAM.Config) == 0 {
		return "", fmt.Errorf("%s has no address range", engine.VMLinkNetwork)
	}
	return n.IPAM.Config[0].Gateway, nil
}

// ensureVMLinkImage builds enclave.local/vmlink on first use (ADR-026, like the firewall helper).
func (d *Driver) ensureVMLinkImage(ctx context.Context) error {
	d.fwMu.Lock()
	ready := d.vmlinkReady
	d.fwMu.Unlock()
	if ready {
		return nil
	}
	tag := vmlink.ImageTag()
	if ok, err := d.ImageExists(ctx, tag); err != nil {
		return err
	} else if !ok {
		dir, err := os.MkdirTemp("", "es-vmlink-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		for name, b := range vmlink.BuildContext() {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
				return err
			}
		}
		sys := &domain.Resource{ID: "system", Kind: "system", Name: "vmlink"}
		if _, err := d.Build(ctx, engine.BuildInput{Resource: sys, Tag: tag, ContextDir: dir, Dockerfile: "Dockerfile"}, io.Discard); err != nil {
			return fmt.Errorf("build the VM link image: %w", err)
		}
	}
	d.fwMu.Lock()
	d.vmlinkReady = true
	d.fwMu.Unlock()
	return nil
}

func joinInts(ns []int) string {
	s := slices.Clone(ns)
	slices.Sort(s)
	out := make([]string, 0, len(s))
	for _, n := range slices.Compact(s) {
		out = append(out, strconv.Itoa(n))
	}
	return strings.Join(out, ",")
}
