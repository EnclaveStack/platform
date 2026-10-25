package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.4 commands (L2-006, L2-002 CLI-18 – CLI-23): networks, firewalls, load balancers, zones, records.

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// placementFlags place an app, database, cache or job in a network (CLI-18) and on a machine (CLI-48).
type placementFlags struct {
	network, subnet string
	machineFlags
}

func (p *placementFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.network, "network", "", `network to place it in ("" is the default network)`)
	cmd.Flags().StringVar(&p.subnet, "subnet", "", "subnet of the network (default: its first)")
	p.machineFlags.bind(cmd)
}

// machineFlags choose the machine a resource runs on (L2-002 CLI-48); without them EnclaveStack chooses.
type machineFlags struct {
	machine string
	labels  []string
}

func (m *machineFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&m.machine, "machine", "", "the machine to run on (default: EnclaveStack chooses; on update, moves it)")
	cmd.Flags().StringArrayVar(&m.labels, "machine-label", nil, "a label its machine must have, as key=value (repeat)")
}

// placed is the spec's placement; a malformed label is reported by the server, so it's passed on as given.
func (m *machineFlags) placed() api.Placed {
	p := api.Placed{Machine: m.machine}
	if len(m.labels) > 0 {
		p.MachineLabels = map[string]string{}
		for _, l := range m.labels {
			k, v, _ := strings.Cut(l, "=")
			p.MachineLabels[k] = v
		}
	}
	return p
}

// patch adds the changed machine flags: --machine "" lets EnclaveStack choose again.
func (m *machineFlags) patch(changed func(string) bool, patch map[string]any) {
	p := m.placed()
	if changed("machine") {
		patch["machine"] = nilIfEmpty(p.Machine)
	}
	if changed("machine-label") {
		if p.MachineLabels == nil {
			patch["machineLabels"] = nil
		} else {
			patch["machineLabels"] = p.MachineLabels
		}
	}
}

// patch adds the changed placement flags; moving to the default network also clears the subnet.
func (p *placementFlags) patch(changed func(string) bool, patch map[string]any) {
	p.machineFlags.patch(changed, patch)
	if changed("network") {
		patch["network"] = nilIfEmpty(p.network)
		if p.network == "" {
			patch["subnet"] = nil
		}
	}
	if changed("subnet") {
		patch["subnet"] = nilIfEmpty(p.subnet)
	}
}

func (a *app) networkCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindNetwork, "network", []string{"networks", "net"}, "Private networks with subnets")
	var cidr string
	var subnets []string
	var noInternet, wait bool
	create := &cobra.Command{
		Use: "create NAME --cidr CIDR", Short: "Create a private network (its addresses can't change later)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := api.NetworkSpec{CIDR: cidr}
			for _, s := range subnets {
				name, c, ok := strings.Cut(s, "=")
				if !ok || name == "" || c == "" {
					return usagef("--subnet needs NAME=CIDR, got %q", s)
				}
				spec.Subnets = append(spec.Subnets, api.Subnet{Name: name, CIDR: c})
			}
			if noInternet {
				f := false
				spec.Internet = &f
			}
			return a.create(cmd.Context(), api.KindNetwork, args[0], spec, wait)
		},
	}
	create.Flags().StringVar(&cidr, "cidr", "", "private IPv4 range, e.g. 10.40.0.0/16")
	create.Flags().StringArrayVar(&subnets, "subnet", nil, "subnet NAME=CIDR inside the range (repeatable; default one subnet covering it)")
	create.Flags().BoolVar(&noInternet, "no-internet", false, "no route to the internet (nothing in it can publish a host port)")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	_ = create.MarkFlagRequired("cidr")
	cmd.AddCommand(create)
	return cmd
}

// parseRule reads "<protocol> [ports] from|to <peer>[,<peer>…]", e.g. "tcp 5432 from app/api" or
// "icmp from any" (CLI-19).
func parseRule(s, dir string) (api.FirewallRule, error) {
	f := strings.Fields(s)
	r := api.FirewallRule{}
	if len(f) < 3 {
		return r, usagef("a rule reads PROTOCOL [PORTS] %s PEERS, like \"tcp 5432 %s app/api\"; got %q", dir, dir, s)
	}
	r.Protocol = strings.ToLower(f[0])
	i := 1
	if f[i] != dir {
		r.Ports = strings.Split(f[i], ",")
		i++
	}
	if i >= len(f) || f[i] != dir || i+1 >= len(f) {
		return r, usagef("a rule reads PROTOCOL [PORTS] %s PEERS, like \"tcp 5432 %s app/api\"; got %q", dir, dir, s)
	}
	peers := strings.Split(strings.Join(f[i+1:], ""), ",")
	if dir == "from" {
		r.From = peers
	} else {
		r.To = peers
	}
	return r, nil
}

func parseRules(list []string, dir string) ([]api.FirewallRule, error) {
	out := []api.FirewallRule{}
	for _, s := range list {
		r, err := parseRule(s, dir)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

type firewallFlags struct {
	targets, allow, allowOut []string
	egress                   string
	wait                     bool
}

func (f *firewallFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.targets, "target", nil, "resource to protect: app/NAME, database/NAME, cache/NAME, loadbalancer/NAME, vm/NAME, search/NAME or email/NAME (repeatable)")
	cmd.Flags().StringArrayVar(&f.allow, "allow", nil, `incoming rule, e.g. "tcp 5432 from app/api,10.0.0.0/8" or "icmp from any" (repeatable)`)
	cmd.Flags().StringVar(&f.egress, "egress", "", "outgoing traffic not allowed by a rule: allow (default) or deny")
	cmd.Flags().StringArrayVar(&f.allowOut, "allow-out", nil, `outgoing rule with --egress deny, e.g. "tcp 443 to any" (repeatable)`)
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until the rules are applied")
}

func (a *app) firewallCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindFirewall, "firewall", []string{"firewalls", "fw"},
		"Firewalls: allow rules for resources (published host ports stay reachable through the machine)")
	var cf firewallFlags
	create := &cobra.Command{
		Use: "create NAME --target KIND/NAME [--allow RULE]…", Short: "Allow only what the rules say into the targets", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := parseRules(cf.allow, "from")
			if err != nil {
				return err
			}
			out, err := parseRules(cf.allowOut, "to")
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindFirewall, args[0], api.FirewallSpec{Targets: cf.targets, Ingress: in, EgressDefault: cf.egress, Egress: out}, cf.wait)
		},
	}
	cf.bind(create)
	_ = create.MarkFlagRequired("target")
	var uf firewallFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a firewall (each list you pass replaces the stored one)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("target") {
				patch["targets"] = uf.targets
			}
			if ch("allow") {
				in, err := parseRules(uf.allow, "from")
				if err != nil {
					return err
				}
				patch["ingress"] = in
			}
			if ch("egress") {
				patch["egressDefault"] = uf.egress
				if uf.egress == api.EgressAllow && !ch("allow-out") {
					patch["egress"] = nil
				}
			}
			if ch("allow-out") {
				out, err := parseRules(uf.allowOut, "to")
				if err != nil {
					return err
				}
				patch["egress"] = out
			}
			return a.update(cmd.Context(), api.KindFirewall, args[0], patch, uf.wait)
		},
	}
	uf.bind(update)
	cmd.AddCommand(create, update)
	return cmd
}

// parseTarget reads APP[:PORT][@WEIGHT] (CLI-20).
func parseTarget(s string) (api.LoadBalancerTarget, error) {
	t := api.LoadBalancerTarget{}
	rest, weight, hasWeight := strings.Cut(s, "@")
	name, port, hasPort := strings.Cut(rest, ":")
	t.App = name
	var err error
	if hasPort {
		if t.Port, err = strconv.Atoi(port); err != nil {
			return t, usagef("--target APP:PORT needs a number, got %q", s)
		}
	}
	if hasWeight {
		if t.Weight, err = strconv.Atoi(weight); err != nil {
			return t, usagef("--target APP@WEIGHT needs a number, got %q", s)
		}
	}
	return t, nil
}

// parseLBHealth reads tcp or http:/PATH.
func parseLBHealth(s string) (*api.LoadBalancerCheck, error) {
	kind, path, _ := strings.Cut(s, ":")
	switch kind {
	case api.HealthTCP:
		return &api.LoadBalancerCheck{Type: api.HealthTCP}, nil
	case api.HealthHTTP:
		return &api.LoadBalancerCheck{Type: api.HealthHTTP, Path: path}, nil
	}
	return nil, usagef("--health must be tcp or http:/PATH, got %q", s)
}

type lbFlags struct {
	port                        int
	protocol, algorithm, health string
	targets                     []string
	publish, wait               bool
	machineFlags
}

func (f *lbFlags) bind(cmd *cobra.Command) {
	f.machineFlags.bind(cmd)
	cmd.Flags().IntVar(&f.port, "port", 0, "port the load balancer listens on")
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "http (default) or tcp")
	cmd.Flags().StringArrayVar(&f.targets, "target", nil, "app to send traffic to: APP[:PORT][@WEIGHT] (repeatable)")
	cmd.Flags().StringVar(&f.algorithm, "algorithm", "", "round_robin (default), least_conn or source")
	cmd.Flags().StringVar(&f.health, "health", "", "replica check: tcp (default) or http:/PATH")
	cmd.Flags().BoolVar(&f.publish, "publish", false, "also listen on a host port")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *lbFlags) targetList() ([]api.LoadBalancerTarget, error) {
	out := []api.LoadBalancerTarget{}
	for _, s := range f.targets {
		t, err := parseTarget(s)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (a *app) lbCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindLoadBalancer, "lb", []string{"loadbalancer", "loadbalancers", "lbs"}, "Load balancers in front of apps' replicas")
	var cf lbFlags
	create := &cobra.Command{
		Use: "create NAME --port PORT --target APP…", Short: "Balance TCP or HTTP traffic across apps' replicas", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := cf.targetList()
			if err != nil {
				return err
			}
			spec := api.LoadBalancerSpec{Port: cf.port, Protocol: cf.protocol, Targets: targets, Algorithm: cf.algorithm, Publish: cf.publish, Placed: cf.placed()}
			if cf.health != "" {
				if spec.HealthCheck, err = parseLBHealth(cf.health); err != nil {
					return err
				}
			}
			return a.create(cmd.Context(), api.KindLoadBalancer, args[0], spec, cf.wait)
		},
	}
	cf.bind(create)
	_ = create.MarkFlagRequired("port")
	_ = create.MarkFlagRequired("target")
	var uf lbFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a load balancer (--target replaces the list)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("port") {
				patch["port"] = uf.port
			}
			if ch("protocol") {
				patch["protocol"] = uf.protocol
			}
			if ch("algorithm") {
				patch["algorithm"] = uf.algorithm
			}
			if ch("publish") {
				patch["publish"] = uf.publish
			}
			uf.machineFlags.patch(ch, patch)
			if ch("target") {
				targets, err := uf.targetList()
				if err != nil {
					return err
				}
				patch["targets"] = targets
			}
			if ch("health") {
				h, err := parseLBHealth(uf.health)
				if err != nil {
					return err
				}
				patch["healthCheck"] = h
			}
			return a.update(cmd.Context(), api.KindLoadBalancer, args[0], patch, uf.wait)
		},
	}
	uf.bind(update)
	cmd.AddCommand(create, update)
	return cmd
}

func (a *app) zoneCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindZone, "zone", []string{"zones"}, "DNS zones at Cloudflare or Route 53")
	var provider, zone, creds string
	var wait bool
	create := &cobra.Command{
		Use: "create NAME --provider PROVIDER --zone ZONE --credentials SECRET", Short: "Let EnclaveStack keep records in a zone", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindZone, args[0], api.ZoneSpec{Provider: provider, Zone: zone, CredentialsSecret: creds}, wait)
		},
	}
	create.Flags().StringVar(&provider, "provider", "", "cloudflare or route53")
	create.Flags().StringVar(&zone, "zone", "", "the zone's name, e.g. example.com")
	create.Flags().StringVar(&creds, "credentials", "", `secret holding {"apiToken"} (Cloudflare) or {"accessKeyId","secretAccessKey"} (Route 53)`)
	create.Flags().BoolVar(&wait, "wait", false, "wait until the zone is checked")
	for _, f := range []string{"provider", "zone", "credentials"} {
		_ = create.MarkFlagRequired(f)
	}
	var ucreds string
	var uwait bool
	update := &cobra.Command{
		Use: "update NAME --credentials SECRET", Short: "Use other credentials for a zone", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("credentials") {
				patch["credentialsSecret"] = ucreds
			}
			return a.update(cmd.Context(), api.KindZone, args[0], patch, uwait)
		},
	}
	update.Flags().StringVar(&ucreds, "credentials", "", "secret holding the provider's credentials")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update)
	return cmd
}

func (a *app) recordCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindRecord, "record", []string{"records"}, "DNS records kept at a zone's provider")
	var zone, name, typ string
	var values []string
	var machine, wait bool
	var ttl int
	create := &cobra.Command{
		Use: "create NAME --zone ZONE --name NAME --type TYPE (--value V… | --machine)", Short: "Keep a DNS record at the provider", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindRecord, args[0], api.RecordSpec{Zone: zone, Name: name, Type: typ, Values: values, Machine: machine, TTL: ttl}, wait)
		},
	}
	create.Flags().StringVar(&zone, "zone", "", "zone resource the record belongs to")
	create.Flags().StringVar(&name, "name", "", "name inside the zone: api, @ for the zone itself, *.dev for a wildcard")
	create.Flags().StringVar(&typ, "type", "", "A, AAAA, CNAME or TXT")
	create.Flags().StringArrayVar(&values, "value", nil, "value (repeatable)")
	create.Flags().BoolVar(&machine, "machine", false, "point an A or AAAA record at this machine's public address")
	create.Flags().IntVar(&ttl, "ttl", 0, "TTL in seconds (60–86400, default 300)")
	create.Flags().BoolVar(&wait, "wait", false, "wait until the provider has it")
	for _, f := range []string{"zone", "name", "type"} {
		_ = create.MarkFlagRequired(f)
	}
	var uvalues []string
	var umachine, uwait bool
	var uttl int
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a record's values or TTL (zone, name and type can't change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("value") {
				patch["values"], patch["machine"] = uvalues, nil
			}
			if ch("machine") {
				patch["machine"] = umachine
				if umachine {
					patch["values"] = nil
				}
			}
			if ch("ttl") {
				patch["ttl"] = uttl
			}
			return a.update(cmd.Context(), api.KindRecord, args[0], patch, uwait)
		},
	}
	update.Flags().StringArrayVar(&uvalues, "value", nil, "value (repeatable; replaces the values)")
	update.Flags().BoolVar(&umachine, "machine", false, "point at this machine's public address")
	update.Flags().IntVar(&uttl, "ttl", 0, "TTL in seconds")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update)
	return cmd
}
