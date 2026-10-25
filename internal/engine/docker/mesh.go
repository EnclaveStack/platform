package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mesh"
	"github.com/enclavestack/platform/pkg/api"
)

// Networks across machines (ADR-045): with two or more machines, es-system-mesh joins every EnclaveStack
// network and tunnels to the other machines, stand-ins hold the names and addresses of containers that
// live elsewhere, and every container takes its address from this machine's pool.

const (
	// MeshName is the mesh container, MeshVolume keeps its private key.
	MeshName         = "es-system-mesh"
	MeshVolume       = "es-system-mesh-data"
	labelStandInOf   = "enclave.standin-of"
	labelStandInFrom = "enclave.standin-machine"
	labelMeshConfig  = "enclave.mesh"
	standInPrefix    = "es-standin-"
	meshKeyDir       = "/var/lib/es-mesh"
)

// meshState is what the driver keeps of the last mesh configuration.
type meshState struct {
	cfg engine.MeshConfig
}

func (d *Driver) meshCfg() *engine.MeshConfig {
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	if d.mesh == nil {
		return nil
	}
	c := d.mesh.cfg
	return &c
}

// ConfigureMesh implements engine.Driver.
func (d *Driver) ConfigureMesh(ctx context.Context, cfg engine.MeshConfig) (engine.MeshStatus, error) {
	if !cfg.Enabled {
		return engine.MeshStatus{}, d.removeMesh(ctx)
	}
	if cfg.TunnelPort == 0 {
		cfg.TunnelPort = api.TunnelPortDefault
	}
	d.meshMu.Lock()
	d.mesh = &meshState{cfg: cfg}
	d.meshMu.Unlock()
	if err := d.ensureNetwork(ctx); err != nil {
		return engine.MeshStatus{}, err
	}
	if err := d.ensureMeshImage(ctx); err != nil {
		return engine.MeshStatus{}, err
	}
	if err := d.ensureMeshContainer(ctx, cfg); err != nil {
		return engine.MeshStatus{}, err
	}
	nets, err := d.meshNetworks(ctx, cfg)
	if err != nil {
		return engine.MeshStatus{}, err
	}
	body, err := json.Marshal(meshHelperConfig(cfg, nets))
	if err != nil {
		return engine.MeshStatus{}, err
	}
	if err := d.copyFiles(ctx, MeshName, filepath.Dir(mesh.ConfigPath), map[string][]byte{filepath.Base(mesh.ConfigPath): body}, 0o600); err != nil {
		return engine.MeshStatus{}, err
	}
	st, err := d.meshExec(ctx, "apply")
	if err != nil {
		return st, err
	}
	if err := d.syncStandIns(ctx, cfg.StandIns); err != nil {
		return st, err
	}
	return st, nil
}

// meshHelperConfig is the helper's own configuration: peers and every network's range.
func meshHelperConfig(cfg engine.MeshConfig, nets map[string]string) mesh.Config {
	out := mesh.Config{ListenPort: cfg.TunnelPort}
	for _, p := range cfg.Peers {
		if p.PublicKey == "" {
			continue // a peer that hasn't made its key yet
		}
		out.Peers = append(out.Peers, mesh.Peer{PublicKey: p.PublicKey, Endpoint: p.Endpoint, Addresses: p.Addresses, Limited: p.Limited, Reach: p.Reach})
	}
	names := make([]string, 0, len(nets))
	for n := range nets {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		out.Networks = append(out.Networks, mesh.Network{CIDR: nets[n]})
	}
	return out
}

// MeshStatus implements engine.Driver.
func (d *Driver) MeshStatus(ctx context.Context) (engine.MeshStatus, error) {
	c, err := d.inspect(ctx, MeshName)
	if err != nil || c == nil || c.State == nil || !c.State.Running {
		return engine.MeshStatus{}, err
	}
	return d.meshExec(ctx, "status")
}

func (d *Driver) meshExec(ctx context.Context, cmd string) (engine.MeshStatus, error) {
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.exec(ectx, MeshName, []string{"/usr/local/bin/es-mesh", cmd}, nil)
	if err != nil {
		return engine.MeshStatus{}, err
	}
	if res.ExitCode != 0 {
		return engine.MeshStatus{}, fmt.Errorf("the mesh couldn't apply its configuration: %s", res.Output)
	}
	var st mesh.Status
	out := res.Output
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:] // the status is the last line
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return engine.MeshStatus{}, fmt.Errorf("read the mesh's status: %w (%s)", err, res.Output)
	}
	ms := engine.MeshStatus{PublicKey: st.PublicKey, Handshakes: map[string]time.Time{}}
	for k, v := range st.Handshakes {
		if v > 0 {
			ms.Handshakes[k] = time.Unix(v, 0)
		}
	}
	return ms, nil
}

// ensureMeshImage builds enclave.local/mesh on first use, like the VM link's image (ADR-026).
func (d *Driver) ensureMeshImage(ctx context.Context) error {
	d.imgMu.Lock()
	ready := d.meshReady
	d.imgMu.Unlock()
	if ready {
		return nil
	}
	tag := mesh.ImageTag()
	if ok, err := d.ImageExists(ctx, tag); err != nil {
		return err
	} else if !ok {
		dir, err := os.MkdirTemp("", "es-mesh-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		for name, b := range mesh.BuildContext() {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
				return err
			}
		}
		sys := &domain.Resource{ID: "system", Kind: "system", Name: "mesh"}
		if _, err := d.Build(ctx, engine.BuildInput{Resource: sys, Tag: tag, ContextDir: dir, Dockerfile: "Dockerfile"}, io.Discard); err != nil {
			return fmt.Errorf("build the mesh image: %w", err)
		}
	}
	d.imgMu.Lock()
	d.meshReady = true
	d.imgMu.Unlock()
	return nil
}

// ensureMeshContainer runs es-system-mesh on the default network, publishing the tunnel's port; it is
// recreated when its image or port changes.
func (d *Driver) ensureMeshContainer(ctx context.Context, cfg engine.MeshConfig) error {
	key := sha256.Sum256([]byte(fmt.Sprint(mesh.ImageTag(), cfg.TunnelPort, cfg.TunnelBind)))
	config := hex.EncodeToString(key[:8])
	c, err := d.inspect(ctx, MeshName)
	if err != nil {
		return err
	}
	if c != nil && (c.Config.Labels[labelMeshConfig] != config || c.State == nil || !c.State.Running) {
		if err := d.remove(ctx, MeshName); err != nil {
			return err
		}
		c = nil
	}
	if c != nil {
		return nil
	}
	port := nat.Port(strconv.Itoa(cfg.TunnelPort) + "/udp")
	bindIP := cfg.TunnelBind
	if bindIP == "" {
		bindIP = "0.0.0.0"
	}
	ccfg := &container.Config{Image: mesh.ImageTag(), Cmd: []string{"run"}, ExposedPorts: nat.PortSet{port: {}},
		Labels: map[string]string{LabelManaged: "true", LabelKind: "system", labelMeshConfig: config}}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		CapDrop:       []string{"ALL"}, CapAdd: []string{"NET_ADMIN"},
		Sysctls:      map[string]string{"net.ipv4.ip_forward": "1"},
		PortBindings: nat.PortMap{port: {{HostIP: bindIP, HostPort: strconv.Itoa(cfg.TunnelPort)}}},
		Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: MeshVolume, Target: meshKeyDir}},
		Resources:    container.Resources{Memory: 64 << 20},
	}
	ep, err := d.meshEndpoint(ctx, engine.NetworkName)
	if err != nil {
		return err
	}
	ep.GwPriority = 1 // the tunnel leaves through the default network
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: ep}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	_, err = d.cli.ContainerCreate(cctx, ccfg, host, netCfg, nil, MeshName)
	cancel()
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		return d.classify(fmt.Errorf("create the mesh: %w", err))
	}
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.ContainerStart(sctx, MeshName, container.StartOptions{}); err != nil {
		return d.classify(fmt.Errorf("start the mesh: %w", err))
	}
	return nil
}

// meshEndpoint is the mesh's place on a network: its fixed MAC, and an address of this machine's pool.
func (d *Driver) meshEndpoint(ctx context.Context, net string) (*network.EndpointSettings, error) {
	ep := &network.EndpointSettings{MacAddress: mesh.MAC(net)}
	addr, err := d.poolAddress(ctx, net, "")
	if err != nil {
		return nil, err
	}
	if addr != "" {
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: addr}
	}
	return ep, nil
}

// meshNetworks connects the mesh to every EnclaveStack network that exists here, and returns their
// ranges by engine name, the default network's included.
func (d *Driver) meshNetworks(ctx context.Context, cfg engine.MeshConfig) (map[string]string, error) {
	c, err := d.inspect(ctx, MeshName)
	if err != nil || c == nil {
		return nil, fmt.Errorf("the mesh isn't running: %v", err)
	}
	out := map[string]string{}
	names := append([]string{engine.NetworkName}, sortedNetworkNames(cfg.Networks)...)
	for _, name := range names {
		n, err := d.cli.NetworkInspect(ctx, name, network.InspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue // not made here yet: the router makes it, and the next sync connects the mesh
		}
		if err != nil {
			return nil, d.classify(fmt.Errorf("inspect network %s: %w", name, err))
		}
		if len(n.IPAM.Config) > 0 {
			out[name] = n.IPAM.Config[0].Subnet
		}
		if c.NetworkSettings != nil {
			if _, ok := c.NetworkSettings.Networks[name]; ok {
				continue
			}
		}
		ep, err := d.meshEndpoint(ctx, name)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err = d.cli.NetworkConnect(cctx, name, MeshName, ep)
		cancel()
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return nil, d.classify(fmt.Errorf("connect the mesh to %s: %w", name, err))
		}
	}
	return out, nil
}

func sortedNetworkNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		if n != engine.NetworkName {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// syncStandIns keeps exactly the stand-ins asked for: one per remote container and network.
func (d *Driver) syncStandIns(ctx context.Context, want []engine.StandIn) error {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", labelStandInOf))})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("list stand-ins: %w", err))
	}
	meshC, err := d.inspect(ctx, MeshName)
	if err != nil || meshC == nil {
		return fmt.Errorf("the mesh isn't running: %v", err)
	}
	wanted := map[string]engine.StandIn{}
	for _, s := range want {
		wanted[standInName(s)] = s
	}
	have := map[string]bool{}
	for _, c := range list {
		name := strings.TrimPrefix(firstName(c.Names), "/")
		s, ok := wanted[name]
		if !ok || c.Labels[labelMeshConfig] != standInConfig(s, meshC) || c.State != "running" {
			if err := d.remove(ctx, c.ID); err != nil {
				return err
			}
			continue
		}
		have[name] = true
	}
	var errs []error
	for name, s := range wanted {
		if have[name] {
			continue
		}
		if err := d.createStandIn(ctx, name, s, meshC); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// standInName is stable for a network and address.
func standInName(s engine.StandIn) string {
	h := sha256.Sum256([]byte(s.Network + "/" + s.Address))
	return standInPrefix + hex.EncodeToString(h[:6])
}

// standInConfig changes when what a stand-in was made with changes: its names, or the mesh it points at.
func standInConfig(s engine.StandIn, meshC *container.InspectResponse) string {
	meshIP := ""
	if meshC.NetworkSettings != nil {
		if ep := meshC.NetworkSettings.Networks[s.Network]; ep != nil {
			meshIP = ep.IPAddress
		}
	}
	aliases := slices.Clone(s.Aliases)
	slices.Sort(aliases)
	h := sha256.Sum256([]byte(fmt.Sprint(mesh.ImageTag(), s.Address, aliases, meshIP, mesh.MAC(s.Network))))
	return hex.EncodeToString(h[:8])
}

func (d *Driver) createStandIn(ctx context.Context, name string, s engine.StandIn, meshC *container.InspectResponse) error {
	var meshIP string
	if meshC.NetworkSettings != nil {
		if ep := meshC.NetworkSettings.Networks[s.Network]; ep != nil {
			meshIP = ep.IPAddress
		}
	}
	if meshIP == "" {
		return nil // the network isn't here yet; the next sync makes the stand-in once the mesh is on it
	}
	n, err := d.cli.NetworkInspect(ctx, s.Network, network.InspectOptions{})
	if err != nil {
		return d.classify(fmt.Errorf("inspect network %s: %w", s.Network, err))
	}
	if len(n.IPAM.Config) == 0 {
		return fmt.Errorf("the network %s has no range", s.Network)
	}
	prefix, err := netip.ParsePrefix(n.IPAM.Config[0].Subnet)
	if err != nil {
		return err
	}
	cfg := &container.Config{Image: mesh.ImageTag(), Cmd: []string{"standin", s.Address, strconv.Itoa(prefix.Bits()), meshIP, mesh.MAC(s.Network)},
		Labels: map[string]string{LabelManaged: "true", LabelKind: "system", labelStandInOf: s.Of, labelStandInFrom: s.Machine, labelMeshConfig: standInConfig(s, meshC)}}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		CapDrop:       []string{"ALL"}, CapAdd: []string{"NET_ADMIN"},
		Sysctls: map[string]string{"net.ipv4.ip_forward": "1", "net.ipv4.conf.all.send_redirects": "0", "net.ipv4.conf.default.send_redirects": "0",
			"net.ipv4.conf.all.rp_filter": "0", "net.ipv4.conf.default.rp_filter": "0"},
		Resources: container.Resources{Memory: 16 << 20},
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		s.Network: {Aliases: s.Aliases, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: s.Address}}}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	_, err = d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, name)
	cancel()
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		return d.classify(fmt.Errorf("create the stand-in for %s (%s): %w", s.Of, s.Address, err))
	}
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.ContainerStart(sctx, name, container.StartOptions{}); err != nil {
		return d.classify(fmt.Errorf("start the stand-in for %s: %w", s.Of, err))
	}
	return nil
}

// removeStandInAt removes the stand-in holding addr on net: what it stood for is moving here, keeping its
// address (ADR-047), and the next mesh sync no longer asks for it since the old copy has stopped.
func (d *Driver) removeStandInAt(ctx context.Context, net, addr string) error {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", labelStandInOf))})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("list stand-ins: %w", err))
	}
	for _, c := range list {
		if c.NetworkSettings == nil {
			continue
		}
		if ep := c.NetworkSettings.Networks[net]; ep != nil && (ep.IPAddress == addr || (ep.IPAMConfig != nil && ep.IPAMConfig.IPv4Address == addr)) {
			if err := d.remove(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeMesh takes the mesh and every stand-in away (one machine again); the key's volume stays.
func (d *Driver) removeMesh(ctx context.Context) error {
	d.meshMu.Lock()
	d.mesh = nil
	d.meshMu.Unlock()
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", labelStandInOf))})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("list stand-ins: %w", err))
	}
	for _, c := range list {
		if err := d.remove(ctx, c.ID); err != nil {
			return err
		}
	}
	return d.remove(ctx, MeshName)
}

// NetworkEndpoints implements engine.Driver.
func (d *Driver) NetworkEndpoints(ctx context.Context) ([]engine.NetworkInfo, error) {
	if !d.opts.Agent {
		// The other machines take the main machine's default range, so it exists before anything runs
		// here: a job's first run on main must already reach names on the others.
		if err := d.ensureNetwork(ctx); err != nil {
			return nil, err
		}
	}
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.NetworkList(lctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))})
	cancel()
	if err != nil {
		return nil, d.classify(fmt.Errorf("list networks: %w", err))
	}
	var out []engine.NetworkInfo
	for _, summary := range list {
		if summary.Name != engine.NetworkName && summary.Labels[LabelKind] != string(api.KindNetwork) {
			continue // es-vmlink and other private networks don't span machines
		}
		n, err := d.cli.NetworkInspect(ctx, summary.ID, network.InspectOptions{})
		if err != nil {
			return nil, d.classify(fmt.Errorf("inspect network %s: %w", summary.Name, err))
		}
		info := engine.NetworkInfo{Name: n.Name}
		if len(n.IPAM.Config) > 0 {
			info.Subnet = n.IPAM.Config[0].Subnet
		}
		ids := make([]string, 0, len(n.Containers))
		for id := range n.Containers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			ep := n.Containers[id]
			if ep.Name == MeshName || strings.HasPrefix(ep.Name, standInPrefix) {
				continue
			}
			p, err := netip.ParsePrefix(ep.IPv4Address)
			if err != nil {
				continue
			}
			c, err := d.inspect(ctx, id)
			if err != nil || c == nil || c.Config == nil || c.Config.Labels[LabelManaged] != "true" {
				continue
			}
			m := engine.NetworkMember{Container: ep.Name, Address: p.Addr().String(), ResourceID: c.Config.Labels[LabelResourceID]}
			if c.NetworkSettings != nil {
				if e := c.NetworkSettings.Networks[n.Name]; e != nil {
					m.Aliases = append(slices.Clone(e.Aliases), e.DNSNames...)
				}
			}
			m.Aliases = append(m.Aliases, ep.Name)
			slices.Sort(m.Aliases)
			m.Aliases = slices.Compact(m.Aliases)
			info.Members = append(info.Members, m)
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b engine.NetworkInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// poolAddress picks, when a mesh gives this machine a pool for net, the lowest free address of the pool in
// subnet ("" for the network's automatic range). Without a mesh, or for a network the mesh doesn't span,
// it returns "" and the engine chooses.
func (d *Driver) poolAddress(ctx context.Context, net, subnet string) (string, error) {
	cfg := d.meshCfg()
	if cfg == nil {
		return "", nil
	}
	if _, spans := cfg.Networks[net]; !spans && net != engine.NetworkName {
		// Not one of the mesh's networks (a leftover of another install): it doesn't span machines, so
		// the engine chooses, as without a mesh. The default network always spans them.
		return "", nil
	}
	pool := cfg.Pools[net]
	n, err := d.cli.NetworkInspect(ctx, net, network.InspectOptions{})
	if err != nil {
		return "", d.classify(fmt.Errorf("inspect network %s: %w", net, err))
	}
	rng := ""
	if len(n.IPAM.Config) > 0 {
		rng = n.IPAM.Config[0].IPRange
		if rng == "" {
			rng = n.IPAM.Config[0].Subnet
		}
	}
	if subnet != "" {
		rng = subnet
	}
	within, err := netip.ParsePrefix(rng)
	if err != nil {
		return "", fmt.Errorf("the network %s has no range", net)
	}
	used := map[string]bool{}
	for _, ep := range n.Containers {
		if p, err := netip.ParsePrefix(ep.IPv4Address); err == nil {
			used[p.Addr().String()] = true
		}
	}
	addrs := make([]netip.Addr, 0, len(pool))
	for _, a := range pool {
		if ip, err := netip.ParseAddr(a); err == nil && within.Contains(ip) {
			addrs = append(addrs, ip)
		}
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	d.addrMu.Lock()
	defer d.addrMu.Unlock()
	now := time.Now()
	for _, a := range addrs {
		key := net + "/" + a.String()
		if used[a.String()] || (!d.reserved[key].IsZero() && now.Sub(d.reserved[key]) < reservationTTL) {
			continue
		}
		d.reserved[key] = now
		return a.String(), nil
	}
	return "", fmt.Errorf("this machine has no free addresses left in %s; waiting for the main machine to give it more", rng)
}
