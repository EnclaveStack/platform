package docker

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/engine"
)

// Public access connectors (ADR-051, ADR-052): a tunnel's connector or a relay, each one container,
// hardened the same way whatever runs inside it.

// LabelHash is the configuration a connector was made with; another one replaces it.
const LabelHash = "enclave.hash"

const (
	connectorLogLines = 60
	connectorExecWait = 20 * time.Second
	fetchWait         = 5 * time.Second
)

// ensureTunnelNetwork makes the tunnel network and puts the edge proxy and the probe helper on it: the
// edge to receive, the probe to read connectors' status. Nothing else joins it (ADR-051).
func (d *Driver) ensureTunnelNetwork(ctx context.Context) error {
	if _, err := d.cli.NetworkInspect(ctx, engine.TunnelNetwork, network.InspectOptions{}); isNotFound(err) {
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.NetworkCreate(cctx, engine.TunnelNetwork, network.CreateOptions{Driver: "bridge",
			Labels: map[string]string{LabelManaged: "true", LabelKind: "tunnel"}})
		cancel()
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return d.classify(fmt.Errorf("create the tunnel network: %w", err))
		}
	} else if err != nil {
		return d.classify(fmt.Errorf("inspect the tunnel network: %w", err))
	}
	return nil
}

// joinTunnelNetwork connects a system container to the tunnel network.
func (d *Driver) joinTunnelNetwork(ctx context.Context, name string) error {
	c, err := d.inspect(ctx, name)
	if err != nil || c == nil {
		return err
	}
	if c.NetworkSettings != nil {
		if _, ok := c.NetworkSettings.Networks[engine.TunnelNetwork]; ok {
			return nil
		}
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.NetworkConnect(cctx, engine.TunnelNetwork, name, &network.EndpointSettings{}); err != nil && !strings.Contains(err.Error(), "already exists") {
		return d.classify(fmt.Errorf("connect %s to the tunnel network: %w", name, err))
	}
	return nil
}

// EnsureConnector implements engine.Driver.
func (d *Driver) EnsureConnector(ctx context.Context, c engine.Connector) (engine.ConnectorState, error) {
	if c.Network == engine.TunnelNetwork {
		if err := d.ensureTunnelNetwork(ctx); err != nil {
			return engine.ConnectorState{}, err
		}
	} else if err := d.ensureNetwork(ctx); err != nil {
		return engine.ConnectorState{}, err
	}
	existing, err := d.inspect(ctx, c.Name)
	if err != nil {
		return engine.ConnectorState{}, err
	}
	if existing != nil && existing.Config != nil && existing.Config.Labels[LabelHash] == c.Hash {
		if existing.State == nil || !existing.State.Running {
			if err := d.start(ctx, c.Name); err != nil {
				return engine.ConnectorState{}, err
			}
		}
		return d.ConnectorState(ctx, c)
	}
	if existing != nil {
		if err := d.remove(ctx, c.Name); err != nil {
			return engine.ConnectorState{}, err
		}
	}
	if err := d.ensureImage(ctx, c.Image, false); err != nil {
		return engine.ConnectorState{}, err
	}
	if err := d.createConnector(ctx, c); err != nil {
		return engine.ConnectorState{}, err
	}
	if len(c.Files) > 0 {
		if err := d.copyFilesInto(ctx, c.Name, c.FilesDir, c.Files, 0o644); err != nil {
			return engine.ConnectorState{}, err
		}
	}
	if err := d.start(ctx, c.Name); err != nil {
		return engine.ConnectorState{}, err
	}
	return d.ConnectorState(ctx, c)
}

func (d *Driver) createConnector(ctx context.Context, c engine.Connector) error {
	env := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	cfg := &container.Config{Image: c.Image, Cmd: c.Cmd, Env: env, ExposedPorts: nat.PortSet{},
		Labels: map[string]string{LabelManaged: "true", LabelKind: "tunnel", LabelResourceID: c.TunnelID, LabelHash: c.Hash}}
	memory := c.MemoryMiB
	if memory <= 0 {
		memory = 128
	}
	tmpfs := map[string]string{}
	for _, dir := range c.Tmpfs {
		tmpfs[dir] = "rw,nosuid,nodev,size=16m"
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{Memory: int64(memory) << 20},
		// Hardened whatever runs inside (L3-006 §14): no capabilities, no privilege gain, a read-only root.
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		ReadonlyRootfs: true,
		Tmpfs:          tmpfs,
		PortBindings:   nat.PortMap{},
	}
	// Files go into a volume of their own: a read-only root can't take them, and a tmpfs doesn't exist until
	// the start, when the program already wants them.
	volumes := map[string]string{}
	if len(c.Files) > 0 {
		volumes[filesVolume(c)] = c.FilesDir
	}
	if c.StateVolume != "" {
		volumes[c.StateVolume] = c.StatePath
	}
	for _, name := range sortedNames(volumes) {
		vctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: name, Labels: map[string]string{LabelManaged: "true", LabelResourceID: c.TunnelID}})
		cancel()
		if err != nil {
			return d.classify(fmt.Errorf("create %s: %w", name, err))
		}
		host.Binds = append(host.Binds, name+":"+volumes[name])
	}
	for _, p := range c.Publish {
		port := nat.Port(strconv.Itoa(p.Container) + "/tcp")
		cfg.ExposedPorts[port] = struct{}{}
		host.PortBindings[port] = []nat.PortBinding{{HostIP: p.HostIP, HostPort: strconv.Itoa(p.Host)}}
	}
	ep := &network.EndpointSettings{}
	if c.Network != engine.TunnelNetwork {
		var err error
		if ep, err = d.systemEndpoint(ctx, c.Network); err != nil { // an address from this machine's pool (ADR-045)
			return err
		}
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{c.Network: ep}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, c.Name); err != nil {
		return d.classify(fmt.Errorf("create %s: %w", c.Name, err))
	}
	return nil
}

// ConnectorState implements engine.Driver.
func (d *Driver) ConnectorState(ctx context.Context, c engine.Connector) (engine.ConnectorState, error) {
	in, err := d.inspect(ctx, c.Name)
	if err != nil || in == nil {
		return engine.ConnectorState{}, err
	}
	st := engine.ConnectorState{Exists: true, Restarts: in.RestartCount}
	if in.State != nil {
		st.Running = in.State.Running && !in.State.Restarting
		if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil {
			st.StartedAt = t
		}
	}
	if in.NetworkSettings != nil {
		if n, ok := in.NetworkSettings.Networks[c.Network]; ok && n != nil {
			st.Address = n.IPAddress
		}
	}
	if lines, err := d.logs(ctx, c.Name, connectorLogLines); err == nil {
		for _, l := range lines {
			st.Logs = append(st.Logs, l.Message)
		}
	}
	return st, nil
}

// RemoveConnector implements engine.Driver.
func (d *Driver) RemoveConnector(ctx context.Context, c engine.Connector) error {
	if err := d.remove(ctx, c.Name); err != nil {
		return err
	}
	for _, v := range []string{filesVolume(c), c.StateVolume} {
		if v == "" {
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.VolumeRemove(vctx, v, true)
		cancel()
		if err != nil && !isNotFound(err) {
			return d.classify(fmt.Errorf("remove %s: %w", v, err))
		}
	}
	return nil
}

// filesVolume holds a connector's files.
func filesVolume(c engine.Connector) string { return c.Name + "-files" }

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ConnectorExec implements engine.Driver.
func (d *Driver) ConnectorExec(ctx context.Context, c engine.Connector, cmd []string) (string, int, error) {
	ectx, cancel := context.WithTimeout(ctx, connectorExecWait)
	defer cancel()
	res, err := d.exec(ectx, c.Name, cmd, nil)
	if err != nil {
		return "", 0, err
	}
	return res.Output, res.ExitCode, nil
}

// ConnectorFetch implements engine.Driver: the probe helper, on the connector's network, reads the endpoint.
func (d *Driver) ConnectorFetch(ctx context.Context, c engine.Connector, port int, path string) ([]byte, error) {
	if err := d.ensureProbe(ctx); err != nil {
		return nil, err
	}
	if c.Network == engine.TunnelNetwork {
		if err := d.joinTunnelNetwork(ctx, engine.ProbeName); err != nil {
			return nil, err
		}
	}
	fctx, cancel := context.WithTimeout(ctx, fetchWait+2*time.Second)
	defer cancel()
	url := "http://" + c.Name + ":" + strconv.Itoa(port) + path
	res, err := d.exec(fctx, engine.ProbeName, []string{"wget", "-q", "-T", strconv.Itoa(int(fetchWait.Seconds())), "-O", "-", url}, nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read %s: %s", url, res.Output)
	}
	return []byte(res.Output), nil
}

// GateBindAddress implements engine.Driver. Docker Desktop forwards host.docker.internal to the Mac's
// loopback, so loopback is enough; on Linux the containers reach the host at the default bridge's gateway,
// which nothing outside the machine can reach (ADR-053).
func (d *Driver) GateBindAddress(ctx context.Context) (string, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return "", d.classify(fmt.Errorf("read the engine's facts: %w", err))
	}
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		return "127.0.0.1", nil
	}
	n, err := d.cli.NetworkInspect(ctx, "bridge", network.InspectOptions{})
	if err != nil {
		return "", d.classify(fmt.Errorf("inspect the default bridge: %w", err))
	}
	for _, c := range n.IPAM.Config {
		if c.Gateway != "" && !strings.Contains(c.Gateway, ":") {
			return c.Gateway, nil
		}
	}
	return "", fmt.Errorf("the default bridge network has no IPv4 gateway for the gate to listen on")
}

// EdgeAddress implements engine.Driver.
func (d *Driver) EdgeAddress(ctx context.Context, net string) (string, error) {
	c, err := d.inspect(ctx, engine.EdgeName)
	if err != nil || c == nil || c.NetworkSettings == nil {
		return "", err
	}
	if n, ok := c.NetworkSettings.Networks[net]; ok && n != nil {
		return n.IPAddress, nil
	}
	return "", nil
}
