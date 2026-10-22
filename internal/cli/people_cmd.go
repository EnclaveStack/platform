package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Projects, people, passwords and the audit list (L2-002 CLI-37 – CLI-39, CLI-45).

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Aliases: []string{"projects"}, Short: "Manage projects: separate namespaces of resources"}
	var description string
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a project (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			p, err := c.CreateProject(cmd.Context(), api.CreateProject{Name: args[0], Description: description})
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(p)
			}
			fmt.Fprintf(a.io.Out, "Created the project %s. Work in it with --project %s, or enclave project use %s.\n", p.Name, p.Name, p.Name)
			return nil
		},
	}
	create.Flags().StringVar(&description, "description", "", "what the project is for")
	list := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List the projects you can see", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			items, err := c.ListProjects(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.ProjectList{Items: items})
			}
			current := a.resolvedProject()
			rows := [][]string{}
			for _, p := range items {
				mark := ""
				if p.Name == current {
					mark = "*"
				}
				rows = append(rows, []string{mark, p.Name, strconv.Itoa(p.Resources), p.Description})
			}
			a.table([]string{"", "NAME", "RESOURCES", "DESCRIPTION"}, rows)
			return nil
		},
	}
	get := &cobra.Command{
		Use: "get NAME", Short: "Show a project", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			p, err := c.GetProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(p)
			}
			a.table([]string{"FIELD", "VALUE"}, [][]string{{"Name", p.Name}, {"Description", p.Description}, {"Resources", strconv.Itoa(p.Resources)}, {"Created", ago(&p.CreatedAt)}})
			return nil
		},
	}
	var newDescription string
	update := &cobra.Command{
		Use: "update NAME --description D", Short: "Change a project's description (its admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return usagef("pass --description")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			p, err := c.UpdateProject(cmd.Context(), args[0], api.UpdateProject{Description: newDescription})
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(p)
			}
			fmt.Fprintln(a.io.Out, "Updated", p.Name)
			return nil
		},
	}
	update.Flags().StringVar(&newDescription, "description", "", "what the project is for")
	var yes bool
	del := &cobra.Command{
		Use: "delete NAME", Short: "Delete an empty project (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.confirm("Delete the project "+args[0]+"?", yes); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Deleted the project", args[0])
			return nil
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	use := &cobra.Command{
		Use: "use NAME", Short: "Work in this project from now on (saved in the config file)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if _, err := c.GetProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			cfg := a.loadConfig()
			cfg.Project = args[0]
			if cfg.Project == api.DefaultProject {
				cfg.Project = ""
			}
			if err := a.saveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Now working in %s.\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(create, list, get, update, del, use)
	return cmd
}

// parseProjectRoles reads repeated --project-role P=R flags; R "none" removes the role.
func parseProjectRoles(flags []string) (map[string]*string, error) {
	out := map[string]*string{}
	for _, f := range flags {
		proj, role, ok := strings.Cut(f, "=")
		if !ok || proj == "" || role == "" {
			return nil, usagef("--project-role takes PROJECT=ROLE, like shop=editor (none removes it)")
		}
		if role == "none" {
			out[proj] = nil
			continue
		}
		if !api.ValidRole(role) {
			return nil, usagef("--project-role %s: the role is viewer, editor, admin or none", f)
		}
		out[proj] = &role
	}
	return out, nil
}

func rolesText(u api.UserSummary) (string, string) {
	machine := "—"
	if u.MachineRole != nil {
		machine = *u.MachineRole
	}
	var parts []string
	for _, p := range sortedKeys(u.ProjectRoles) {
		parts = append(parts, p+"="+u.ProjectRoles[p])
	}
	if len(parts) == 0 {
		return machine, "—"
	}
	return machine, strings.Join(parts, ", ")
}

func (a *app) userCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Aliases: []string{"users"}, Short: "Manage people and their roles"}
	var role string
	var projectRoles []string
	invite := &cobra.Command{
		Use: "invite EMAIL", Short: "Invite a person; prints the link to send them", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roles, err := parseProjectRoles(projectRoles)
			if err != nil {
				return err
			}
			req := api.InviteUser{Email: args[0], ProjectRoles: map[string]string{}}
			for p, r := range roles {
				if r == nil {
					return usagef("--project-role %s=none has nothing to remove on an invite", p)
				}
				req.ProjectRoles[p] = *r
			}
			if role != "" {
				if !api.ValidRole(role) {
					return usagef("--role is viewer, editor or admin")
				}
				req.MachineRole = &role
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			inv, err := c.InviteUser(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(inv)
			}
			a.printInvitation(inv)
			return nil
		},
	}
	invite.Flags().StringVar(&role, "role", "", "machine role: viewer, editor or admin (every project)")
	invite.Flags().StringArrayVar(&projectRoles, "project-role", nil, "PROJECT=ROLE, repeatable (shop=editor)")
	list := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List people and their roles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			users, err := c.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.UserList{Items: users})
			}
			rows := [][]string{}
			for _, u := range users {
				machine, projects := rolesText(u)
				rows = append(rows, []string{u.Email, u.Status, machine, projects, ago(u.LastSignInAt)})
			}
			a.table([]string{"EMAIL", "STATUS", "MACHINE ROLE", "PROJECT ROLES", "LAST SIGN-IN"}, rows)
			return nil
		},
	}
	get := &cobra.Command{
		Use: "get EMAIL", Short: "Show a person", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.FindUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(u)
			}
			machine, projects := rolesText(u)
			a.table([]string{"FIELD", "VALUE"}, [][]string{{"Email", u.Email}, {"ID", u.ID}, {"Status", u.Status}, {"Machine role", machine},
				{"Project roles", projects}, {"Last sign-in", ago(u.LastSignInAt)}})
			return nil
		},
	}
	var newRole string
	var noRole bool
	var newProjectRoles []string
	update := &cobra.Command{
		Use: "update EMAIL", Short: "Change a person's roles", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roles, err := parseProjectRoles(newProjectRoles)
			if err != nil {
				return err
			}
			setMachine := cmd.Flags().Changed("role") || noRole
			if !setMachine && len(roles) == 0 {
				return usagef("pass --role, --no-role or --project-role")
			}
			var machine *string
			if cmd.Flags().Changed("role") {
				if !api.ValidRole(newRole) {
					return usagef("--role is viewer, editor or admin")
				}
				machine = &newRole
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.FindUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			u, err = c.UpdateUser(cmd.Context(), u.ID, setMachine, machine, roles)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(u)
			}
			m, p := rolesText(u)
			fmt.Fprintf(a.io.Out, "%s: machine role %s; project roles %s.\n", u.Email, m, p)
			return nil
		},
	}
	update.Flags().StringVar(&newRole, "role", "", "machine role: viewer, editor or admin")
	update.Flags().BoolVar(&noRole, "no-role", false, "remove the machine role")
	update.Flags().StringArrayVar(&newProjectRoles, "project-role", nil, "PROJECT=ROLE or PROJECT=none, repeatable")
	update.MarkFlagsMutuallyExclusive("role", "no-role")
	var yes bool
	remove := &cobra.Command{
		Use: "remove EMAIL", Aliases: []string{"rm", "delete"}, Short: "Remove a person; their sessions and tokens stop working", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.confirm("Remove "+args[0]+" from this machine?", yes); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.FindUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := c.DeleteUser(cmd.Context(), u.ID); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Removed", u.Email)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	reset := &cobra.Command{
		Use: "reset EMAIL", Short: "A new invite link, or a password-reset link for an active person", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.FindUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			inv, err := c.ResetUser(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(inv)
			}
			a.printInvitation(inv)
			return nil
		},
	}
	cmd.AddCommand(invite, list, get, update, remove, reset)
	return cmd
}

func (a *app) passwordCmd() *cobra.Command {
	var email string
	return &cobra.Command{
		Use: "password", Short: "Change your password (ends your other sessions)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, _ := a.resolved()
			ctx := cmd.Context()
			if email == "" {
				var err error
				if email, err = a.prompt("Email: "); err != nil {
					return err
				}
			}
			current, err := a.promptSecret("Current password: ")
			if err != nil {
				return err
			}
			next, err := a.promptSecret("New password: ")
			if err != nil {
				return err
			}
			c := a.newClient(server, "") // a password changes over a session, never with a token
			if _, err := c.SignIn(ctx, email, current); err != nil {
				return err
			}
			defer func() { _ = c.SignOut(ctx) }()
			if err := c.ChangePassword(ctx, api.ChangePassword{CurrentPassword: current, NewPassword: next}); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Password changed. Your other sessions were signed out; API tokens keep working.")
			return nil
		},
	}
}

func (a *app) auditCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "audit", Short: "Show the audit list, newest first (machine admins)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			page, err := c.Audit(cmd.Context(), limit, "")
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(page)
			}
			rows := [][]string{}
			for _, e := range page.Items {
				rows = append(rows, []string{e.At.Local().Format("2006-01-02 15:04:05"), e.Actor, e.Action, e.Target, e.Detail, e.ClientIP})
			}
			a.table([]string{"TIME", "WHO", "ACTION", "TARGET", "DETAIL", "FROM"}, rows)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "how many entries (1–200)")
	return cmd
}

// tokenScopeText describes what a token reaches.
func tokenScopeText(t api.Token) string {
	if t.Projects == nil {
		return "all projects"
	}
	p := slices.Clone(t.Projects)
	slices.Sort(p)
	return strings.Join(p, ", ")
}

// printInvitation prints the link, and whether it was emailed (IAM-04).
func (a *app) printInvitation(inv api.Invitation) {
	fmt.Fprintln(a.io.Out, inv.URL)
	until := inv.ExpiresAt.Local().Format("2 Jan 15:04")
	switch {
	case inv.Emailed:
		fmt.Fprintf(a.io.Err, "Emailed to %s. The link works once, until %s.\n", inv.User.Email, until)
	case inv.EmailError != "":
		fmt.Fprintf(a.io.Err, "%s\nSend this link to %s. It works once, until %s.\n", inv.EmailError, inv.User.Email, until)
	default:
		fmt.Fprintf(a.io.Err, "Send this link to %s. It works once, until %s.\n", inv.User.Email, until)
	}
}
