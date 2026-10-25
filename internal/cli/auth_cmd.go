package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

func (a *app) loginCmd() *cobra.Command {
	var email, role string
	var projects []string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and save an API token for this machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, _ := a.resolved()
			if a.server != "" {
				server = a.server
			}
			ctx := cmd.Context()
			if a.token != "" {
				c := a.newClient(server, a.token)
				s, err := c.Session(ctx)
				if err != nil {
					return err
				}
				if err := a.saveConfig(Config{Server: server, Token: a.token}); err != nil {
					return err
				}
				fmt.Fprintf(a.io.Out, "Signed in to %s as %s with %s. Saved to %s.\n", server, s.User.Email, accessText(s), a.configPath())
				return nil
			}
			if email == "" {
				var err error
				if email, err = a.prompt("Email: "); err != nil {
					return err
				}
			}
			password, err := a.promptSecret("Password: ")
			if err != nil {
				return err
			}
			c := a.newClient(server, "")
			if _, err := c.SignIn(ctx, email, password); err != nil {
				return err
			}
			host, _ := os.Hostname()
			req := api.CreateTokenRequest{Name: "cli on " + host, Role: role}
			if len(projects) > 0 {
				req.Projects = projects
			}
			tok, err := c.CreateToken(ctx, req)
			if err != nil {
				return err
			}
			_ = c.SignOut(ctx) // the token replaces the short-lived session
			if err := a.saveConfig(Config{Server: server, Token: tok.Secret}); err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Signed in to %s as %s. Token %s… saved to %s.\n", server, email, tok.Token.Prefix, a.configPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email (prompted when omitted)")
	cmd.Flags().StringVar(&role, "role", "", "the saved token's role: viewer, editor or admin (default: your strongest)")
	cmd.Flags().StringArrayVar(&projects, "token-project", nil, "limit the saved token to this project (repeatable; default: all)")
	return cmd
}

func article(word string) string {
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// accessText describes a session's access in words: a token's role and projects, or the user's roles.
func accessText(s api.Session) string {
	if s.Token != nil {
		where := "all projects"
		if s.Token.Projects != nil {
			where = strings.Join(s.Token.Projects, ", ")
		}
		return article(s.Token.Role) + " " + s.Token.Role + " token on " + where
	}
	var parts []string
	if s.MachineRole != nil {
		parts = append(parts, *s.MachineRole+" of the machine")
	}
	for _, p := range sortedKeys(s.ProjectRoles) {
		parts = append(parts, s.ProjectRoles[p]+" of "+p)
	}
	if len(parts) == 0 {
		return "no roles"
	}
	return strings.Join(parts, ", ")
}

func (a *app) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke this machine's saved token and forget it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := a.loadConfig()
			if cfg.Token == "" {
				fmt.Fprintln(a.io.Out, "Not signed in.")
				return nil
			}
			c := a.newClient(cfg.Server, cfg.Token)
			if toks, err := c.ListTokens(cmd.Context(), false); err == nil {
				for _, t := range toks {
					if len(cfg.Token) >= len(t.Prefix) && cfg.Token[:len(t.Prefix)] == t.Prefix {
						_ = c.RevokeToken(cmd.Context(), t.ID)
					}
				}
			}
			cfg.Token = ""
			if err := a.saveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Signed out.")
			return nil
		},
	}
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use: "whoami", Short: "Show who you are signed in as", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			s, err := c.Session(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(s)
			}
			server, _ := a.resolved()
			fmt.Fprintf(a.io.Out, "%s on %s (%s): %s; working in %s\n", s.User.Email, s.MachineName, server, accessText(s), a.resolvedProject())
			return nil
		},
	}
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print the client version", Args: cobra.NoArgs,
		Run: func(*cobra.Command, []string) { fmt.Fprintln(a.io.Out, "enclave", version.Version) },
	}
}

func (a *app) machineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "machine", Short: "Show the machine, server version and engine status", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			m, err := c.Machine(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(m)
			}
			engine := "available"
			if !m.Engine.Available {
				engine = "UNAVAILABLE — " + m.Engine.Message
			}
			a.table([]string{"FIELD", "VALUE"}, append([][]string{
				{"Name", m.Name}, {"Hostname", m.Hostname}, {"Server version", m.Version}, {"OS / arch", m.OS + " / " + m.Arch},
				{"CPUs", fmt.Sprint(m.CPUs)}, {"Memory reserved", mib(m.MemoryReservedBytes) + " of " + mib(m.MemoryTotalBytes)},
				{"Engine", m.Engine.Driver + " " + m.Engine.Version + " (" + engine + ")"},
				{"Edge proxy", edgeState(m.Edge)},
				{"Public address", deref(m.PublicAddress, "not set")},
				{"ACME email", deref(m.AcmeEmail, "the admin's email")},
				{"Invites emailed", inviteEmailText(m.InviteEmail)},
			}, vmRows(m)...))
			return nil
		},
	}
	var public, email, inviteService, inviteFrom string
	var noInviteEmail bool
	set := &cobra.Command{
		Use: "set", Short: "Set the public address, the Let's Encrypt email and how invites are emailed (empty clears)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ch := cmd.Flags().Changed
			if !ch("public-address") && !ch("acme-email") && !ch("invite-email") && !ch("no-invite-email") {
				return usagef("pass --public-address, --acme-email, --invite-email or --no-invite-email")
			}
			if ch("invite-email") && ch("no-invite-email") {
				return usagef("--invite-email and --no-invite-email can't go together")
			}
			if ch("invite-email") && inviteFrom == "" {
				return usagef("--invite-email needs --invite-from, the sender's address")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			cur, err := c.Machine(cmd.Context())
			if err != nil {
				return err
			}
			req := api.MachineSettings{PublicAddress: cur.PublicAddress, AcmeEmail: cur.AcmeEmail}
			if ch("public-address") {
				req.PublicAddress = &public
			}
			if ch("acme-email") {
				req.AcmeEmail = &email
			}
			if ch("invite-email") {
				ie := &api.InviteEmail{Service: inviteService, From: inviteFrom}
				if proj, svc, ok := strings.Cut(inviteService, "/"); ok {
					ie.Project, ie.Service = proj, svc
				}
				req.InviteEmail = ie
			}
			req.ClearInviteEmail = noInviteEmail
			m, err := c.UpdateMachine(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(m)
			}
			fmt.Fprintf(a.io.Out, "Public address: %s\nACME email: %s\nInvites emailed: %s\n", deref(m.PublicAddress, "not set"), deref(m.AcmeEmail, "the admin's email"), inviteEmailText(m.InviteEmail))
			return nil
		},
	}
	set.Flags().StringVar(&public, "public-address", "", "IP or hostname your domains' DNS records point to")
	set.Flags().StringVar(&email, "acme-email", "", "email for the Let's Encrypt account")
	set.Flags().StringVar(&inviteService, "invite-email", "", "email invite and reset links through this email service ([PROJECT/]NAME)")
	set.Flags().StringVar(&inviteFrom, "invite-from", "", "the sender of invite emails")
	set.Flags().BoolVar(&noInviteEmail, "no-invite-email", false, "stop emailing invite links")
	cmd.AddCommand(set)
	a.machineSubcommands(cmd)
	return cmd
}

func inviteEmailText(ie *api.InviteEmail) string {
	if ie == nil {
		return "no (links are shown to send yourself)"
	}
	return fmt.Sprintf("from %s through %s/%s", ie.From, ie.Project, ie.Service)
}

func edgeState(e api.Edge) string {
	if e.Available {
		return "running"
	}
	return "not running — " + e.Message
}

func deref(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

func mib(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", b>>20)
}

func (a *app) tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens for scripts, CI and OpenTofu"}
	var name, scope, role string
	var projects []string
	var days int
	create := &cobra.Command{
		Use: "create", Short: "Create a token (shown once)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			req := api.CreateTokenRequest{Name: name, Role: role, Scope: scope, ExpiresInDays: days}
			if len(projects) > 0 {
				req.Projects = projects
			}
			t, err := c.CreateToken(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(t)
			}
			fmt.Fprintln(a.io.Out, t.Secret)
			fmt.Fprintln(a.io.Err, "Copy this token now — it won't be shown again.")
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "what the token is for")
	create.Flags().StringVar(&role, "role", "", "viewer, editor or admin (default: your strongest role)")
	create.Flags().StringArrayVar(&projects, "project", nil, "limit the token to this project (repeatable; default: all projects)")
	create.Flags().StringVar(&scope, "scope", "", "v0.6 form of --role: read (viewer) or write")
	create.Flags().IntVar(&days, "expires-in-days", 0, "expire after this many days (0 = never)")
	_ = create.MarkFlagRequired("name")
	var allUsers bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List tokens", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			toks, err := c.ListTokens(cmd.Context(), allUsers)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.TokenList{Items: toks})
			}
			rows := [][]string{}
			for _, t := range toks {
				row := []string{t.ID, t.Name, t.Prefix + "…", t.Role, tokenScopeText(t), ago(&t.CreatedAt), ago(t.LastUsedAt), when(t.ExpiresAt)}
				if allUsers {
					row = append([]string{t.UserEmail}, row...)
				}
				rows = append(rows, row)
			}
			header := []string{"ID", "NAME", "PREFIX", "ROLE", "PROJECTS", "CREATED", "LAST USED", "EXPIRES"}
			if allUsers {
				header = append([]string{"OWNER"}, header...)
			}
			a.table(header, rows)
			return nil
		},
	}
	list.Flags().BoolVar(&allUsers, "all-users", false, "everyone's tokens (machine admins)")
	revoke := &cobra.Command{
		Use: "revoke ID", Short: "Revoke a token", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.RevokeToken(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Revoked", args[0])
			return nil
		},
	}
	cmd.AddCommand(create, list, revoke)
	return cmd
}

func ago(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02")
}
