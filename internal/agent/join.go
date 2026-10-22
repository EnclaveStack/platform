package agent

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// JoinOptions is what `enclave agent join` is told.
type JoinOptions struct {
	Server     string // the main machine's URL
	Code       string
	Advertise  string // where the main machine reaches this one; default: the address that reaches Server
	Listen     string // the agent's listen address, default ":7071"
	TunnelPort int    // default 51820
	StateDir   string
}

// Join makes this computer's key, joins with the code, and saves the identity (MCH-04, ADR-043). The key
// never leaves the computer: only a certificate request is sent.
func Join(ctx context.Context, o JoinOptions) (Identity, error) {
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
	if _, err := Load(o.StateDir); err == nil {
		return Identity{}, fmt.Errorf("this computer already joined an EnclaveStack (%s). Run `enclave agent leave` first", o.StateDir)
	}
	if o.Listen == "" {
		o.Listen = ":" + strconv.Itoa(api.AgentPortDefault)
	}
	if o.TunnelPort == 0 {
		o.TunnelPort = api.TunnelPortDefault
	}
	_, portS, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return Identity{}, fmt.Errorf("--listen %q: %w", o.Listen, err)
	}
	port, err := strconv.Atoi(portS)
	if err != nil {
		return Identity{}, fmt.Errorf("--listen %q: the port isn't a number", o.Listen)
	}
	server := strings.TrimRight(o.Server, "/")
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	if o.Advertise == "" {
		if o.Advertise, err = outboundAddress(server); err != nil {
			return Identity{}, fmt.Errorf("find this computer's address (give --advertise): %w", err)
		}
	}
	keyPEM, csrPEM, err := NewKeyAndRequest()
	if err != nil {
		return Identity{}, err
	}
	c := client.New(server, "")
	res, err := c.JoinMachine(ctx, api.JoinRequest{Code: strings.TrimSpace(o.Code), CSR: string(csrPEM), Advertise: o.Advertise, AgentPort: port,
		TunnelPort: o.TunnelPort, OS: runtime.GOOS, Arch: runtime.GOARCH, AgentVersion: version.Version})
	if err != nil {
		return Identity{}, err
	}
	if res.Server != "" {
		server = res.Server
	}
	id := Identity{MachineID: res.MachineID, Name: res.Name, Server: server, Advertise: o.Advertise, Listen: o.Listen, TunnelPort: o.TunnelPort}
	if err := Save(o.StateDir, id, keyPEM, []byte(res.Certificate), []byte(res.CA)); err != nil {
		return Identity{}, err
	}
	id.dir = o.StateDir
	return id, nil
}

// outboundAddress is the local address this computer uses to reach server.
func outboundAddress(server string) (string, error) {
	host := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80")
	}
	conn, err := net.Dial("udp", host) // no packet is sent: this only picks the route
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
