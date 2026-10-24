package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-002 CLI-56 – CLI-58, L2-014): tunnels, and the gate shared with public domains.

// gateFlags are the gate's flags on `tunnel` (app links) and `domain` (public domains).
type gateFlags struct {
	mode, passwordSecret string
	openPaths            []string
	yes                  bool
}

func (g *gateFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&g.mode, "gate", "", "who may pass: signin (people with access to the project), password, or none (anyone on the internet)")
	f.StringVar(&g.passwordSecret, "gate-password-secret", "", `with --gate password: a secret holding {"username","password"}`)
	f.StringArrayVar(&g.openPaths, "gate-open-path", nil, "a path prefix that skips the gate, e.g. /webhooks/ (repeat; \"\" clears them on update)")
	f.BoolVar(&g.yes, "yes", false, "don't ask before opening it to everyone (--gate none)")
}

// changed reports whether any gate flag was given.
func (g *gateFlags) changed(cmd *cobra.Command) bool {
	ch := cmd.Flags().Changed
	return ch("gate") || ch("gate-password-secret") || ch("gate-open-path")
}

// gate builds the gate from the flags, asking before --gate none (GATE-05).
func (a *app) gate(g *gateFlags) (*api.Gate, error) {
	mode := g.mode
	if mode == "" {
		mode = api.GateSignin
		if g.passwordSecret != "" {
			mode = api.GatePassword
		}
	}
	if mode == api.GateNone && !g.yes {
		if !a.io.Interactive {
			return nil, usagef("--gate none lets anyone on the internet open it; re-run with --yes to confirm")
		}
		if err := a.confirm("Anyone on the internet will be able to open it. Continue?", false); err != nil {
			return nil, err
		}
	}
	return &api.Gate{Mode: mode, PasswordSecret: g.passwordSecret, OpenPaths: nonEmpty(g.openPaths)}, nil
}

// tunnelFlags are shared by tunnel create and tunnel update.
type tunnelFlags struct {
	provider, credentials, accountID, machine, target, mode, hostname, domain, image, url string
	port                                                                                  int
	command, env, secretEnv                                                               []string
	gate                                                                                  gateFlags
	wait                                                                                  bool
}

func (t *tunnelFlags) register(cmd *cobra.Command, create bool) {
	f := cmd.Flags()
	if create {
		f.StringVar(&t.provider, "provider", "", "the way in: quick, tailscale, ngrok, custom (an address for one app), cloudflare or relay (your own domains)")
		f.StringVar(&t.machine, "machine", "", "relay: the edge machine it runs on")
	}
	f.StringVar(&t.credentials, "credentials", "", "cloudflare, tailscale, ngrok: the secret holding the API token, auth key or authtoken")
	f.StringVar(&t.accountID, "account-id", "", "cloudflare: the account ID, on the domain's overview page")
	f.StringVar(&t.target, "app", "", "app links: the app to make public")
	f.IntVar(&t.port, "port", 0, "app links: the app's port (default: the app's)")
	f.StringVar(&t.mode, "mode", "", "tailscale: private (your tailnet's devices) or public (anyone, with Funnel)")
	f.StringVar(&t.hostname, "hostname", "", "tailscale: the device's name (default <project>-<app>)")
	f.StringVar(&t.domain, "domain", "", "ngrok: a domain reserved in your account")
	f.StringVar(&t.image, "image", "", "custom: the connector's image")
	f.StringArrayVar(&t.command, "command", nil, "custom: the command, one argument per flag (repeat)")
	f.StringArrayVar(&t.env, "env", nil, "custom: KEY=VALUE (repeat)")
	f.StringArrayVar(&t.secretEnv, "secret-env", nil, "custom: KEY=SECRET (repeat)")
	f.StringVar(&t.url, "url", "", "custom: the https:// address the service gives")
	f.BoolVar(&t.wait, "wait", false, "wait until it is connected")
	t.gate.register(cmd)
}

func (a *app) tunnelCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindTunnel, "tunnel", []string{"tunnels"}, "Tunnels: ways in from the internet (quick, tailscale, ngrok, custom, cloudflare, relay)")
	var cf tunnelFlags
	create := &cobra.Command{
		Use:   "create NAME --provider P …",
		Short: "Make a way in from the internet",
		Long: `Make a way in from the internet (L2-014). Nothing on the router is opened: a connector dials out.

  quick      enclave tunnel create demo --provider quick --app web
  tailscale  enclave tunnel create stage-ts --provider tailscale --credentials ts-key --app web [--mode public]
  ngrok      enclave tunnel create stage-ngrok --provider ngrok --credentials ngrok --app web [--domain stage.ngrok.app]
  custom     enclave tunnel create stage-x --provider custom --app web --image IMAGE --command … [--url https://…]
  cloudflare enclave tunnel create home --provider cloudflare --credentials cf-token --account-id ID
  relay      enclave tunnel create edge --provider relay --machine vps1

App links (quick, tailscale, ngrok, custom) are gated: --gate signin (default), password or none.
Your own domains go through cloudflare or relay: see enclave domain create --tunnel.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cf.provider == "" {
				return usagef("pass --provider: quick, tailscale, ngrok, custom, cloudflare or relay")
			}
			spec := api.TunnelSpec{Provider: cf.provider, CredentialsSecret: cf.credentials, AccountID: cf.accountID, Machine: cf.machine, App: cf.target, Port: cf.port,
				Mode: cf.mode, Hostname: cf.hostname, Domain: cf.domain, Image: cf.image, Command: cf.command, URL: cf.url}
			var err error
			if spec.Env, err = parsePairs("env", cf.env); err != nil {
				return err
			}
			if spec.SecretEnv, err = parsePairs("secret-env", cf.secretEnv); err != nil {
				return err
			}
			// Without gate flags the server applies the default: signin (a private Tailscale link: none).
			if cf.gate.changed(cmd) {
				if !api.IsAppLink(cf.provider) {
					return usagef("--gate goes on app links; a domain tunnel's domains carry their own gate")
				}
				if spec.Gate, err = a.gate(&cf.gate); err != nil {
					return err
				}
			}
			return a.create(cmd.Context(), api.KindTunnel, args[0], spec, cf.wait)
		},
	}
	cf.register(create, true)
	var uf tunnelFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a tunnel's app, gate, Tailscale mode, custom settings or credentials (not its provider)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			for flag, field := range map[string]string{"credentials": "credentialsSecret", "account-id": "accountId", "app": "app", "mode": "mode",
				"hostname": "hostname", "domain": "domain", "image": "image", "url": "url"} {
				if ch(flag) {
					v := map[string]string{"credentials": uf.credentials, "account-id": uf.accountID, "app": uf.target, "mode": uf.mode,
						"hostname": uf.hostname, "domain": uf.domain, "image": uf.image, "url": uf.url}[flag]
					patch[field] = nilIfEmpty(v)
				}
			}
			if ch("port") {
				patch["port"] = nilIfZeroInt(uf.port)
			}
			if ch("command") {
				patch["command"] = uf.command
			}
			if ch("env") {
				env, err := parsePairs("env", uf.env)
				if err != nil {
					return err
				}
				patch["env"] = env
			}
			if ch("secret-env") {
				senv, err := parsePairs("secret-env", uf.secretEnv)
				if err != nil {
					return err
				}
				patch["secretEnv"] = senv
			}
			if uf.gate.changed(cmd) {
				g, err := a.gate(&uf.gate)
				if err != nil {
					return err
				}
				patch["gate"] = g
			}
			return a.update(cmd.Context(), api.KindTunnel, args[0], patch, uf.wait)
		},
	}
	uf.register(update, false)
	cmd.AddCommand(create, update)
	return cmd
}

// publicDomainFlags make a domain public through a domain tunnel (PUB-01, GATE-09).
type publicDomainFlags struct {
	tunnel                     string
	accessEmails, accessDomain []string
	gate                       gateFlags
}

func (p *publicDomainFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&p.tunnel, "tunnel", "", "make it public through this cloudflare or relay tunnel (empty on update: back to this machine only)")
	f.StringArrayVar(&p.accessEmails, "access-email", nil, "cloudflare: also put Cloudflare Access in front, letting in this address (repeat)")
	f.StringArrayVar(&p.accessDomain, "access-email-domain", nil, "cloudflare: Cloudflare Access lets in addresses at this domain, e.g. example.com (repeat)")
	p.gate.register(cmd)
}

// publicDomain builds the gate and Cloudflare Access from the flags; both nil when none was given.
func (a *app) publicDomain(cmd *cobra.Command, p *publicDomainFlags) (*api.Gate, *api.CloudflareAccess, error) {
	var g *api.Gate
	if p.gate.changed(cmd) {
		var err error
		if g, err = a.gate(&p.gate); err != nil {
			return nil, nil, err
		}
	}
	var access *api.CloudflareAccess
	emails, domains := nonEmpty(p.accessEmails), nonEmpty(p.accessDomain)
	if len(emails)+len(domains) > 0 {
		access = &api.CloudflareAccess{Emails: emails, EmailDomains: domains}
	}
	return g, access, nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
