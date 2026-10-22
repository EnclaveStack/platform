package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Moving, the catalogue and stacks (L2-002 CLI-49 – CLI-52).

func (a *app) moveCmd() *cobra.Command {
	var to string
	var wait bool
	cmd := &cobra.Command{
		Use: "move KIND/NAME --to MACHINE", Short: "Move a resource, with its data, to another machine", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kindName, name, ok := strings.Cut(args[0], "/")
			if !ok {
				return usagef("name it as KIND/NAME, like database/orders")
			}
			kind, err := parseKind(kindName)
			if err != nil {
				return err
			}
			if to == "" {
				return usagef("pass --to, the machine to move it to")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, kind, name)
			if err != nil {
				return err
			}
			// The move's own event comes after this one; an earlier move's doesn't count.
			var seen int64
			if ev, err := c.Events(cmd.Context(), r.ID, 1); err == nil && len(ev) > 0 {
				seen = ev[0].ID
			}
			gen := r.Generation
			r, err = c.UpdateResource(cmd.Context(), r.ID, map[string]any{"machine": to}, &gen)
			if err != nil {
				return err
			}
			if !wait {
				fmt.Fprintf(a.io.Err, "Moving %s %s to %s. Follow it with: enclave get %s %s\n", kind, name, to, kind, name)
				return nil
			}
			if r, err = a.wait(cmd.Context(), c, r); err != nil {
				return err
			}
			return a.waitMoved(cmd.Context(), c, r, seen)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the machine to move it to")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it runs there, printing the bytes copied")
	return cmd
}

// waitMoved waits for the move's outcome: the resource is ready on its new machine before the old
// copy is removed and the move recorded, so ready alone doesn't mean moved.
func (a *app) waitMoved(ctx context.Context, c *client.Client, r api.Resource, seen int64) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	for {
		ev, err := c.Events(ctx, r.ID, 20)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		for _, e := range ev {
			if e.ID <= seen {
				break
			}
			switch e.Type {
			case "moved":
				fmt.Fprintln(a.io.Out, e.Message)
				return nil
			case "move_failed":
				return fmt.Errorf("%s %s didn't move: %s", r.Kind, r.Name, e.Message)
			}
		}
		select {
		case <-ctx.Done():
			return &waitTimeout{what: "the move of " + string(r.Kind) + " " + r.Name}
		case <-time.After(pollEvery):
		}
	}
}

func (a *app) drainCmd() *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use: "drain NAME", Short: "Take nothing new on a machine and move everything not pinned there to others (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.DrainMachine(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" && !wait {
				return a.json(res)
			}
			fmt.Fprintf(a.io.Out, "Draining %s: moving %d, pinned %d.\n", args[0], len(res.Moving), len(res.Pinned))
			for _, p := range res.Pinned {
				fmt.Fprintf(a.io.Out, "  %s is pinned there: move it with enclave move %s --to MACHINE\n", p, p)
			}
			if !wait {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
			defer cancel()
			for {
				left, err := c.ListAll(ctx, client.ListOptions{Machine: args[0], AllProjects: true})
				if err != nil {
					return err
				}
				moving := 0
				for _, r := range left {
					if slices.Contains(res.Moving, qualifiedName(r)) {
						moving++
					}
				}
				if moving == 0 {
					fmt.Fprintln(a.io.Out, "Drained.")
					return nil
				}
				select {
				case <-ctx.Done():
					return &waitTimeout{what: "the drain of " + args[0]}
				case <-time.After(pollEvery):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until everything that moves has moved")
	return cmd
}

func qualifiedName(r api.Resource) string {
	if r.Project != "" && r.Project != api.DefaultProject {
		return r.Project + "/" + string(r.Kind) + "/" + r.Name
	}
	return string(r.Kind) + "/" + r.Name
}

func (a *app) catalogueCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "catalogue", Aliases: []string{"catalog"}, Short: "Ready-made stacks, and your own templates"}
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List the catalogue", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			items, err := c.Catalogue(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.CatalogueList{Items: items})
			}
			rows := [][]string{}
			for _, e := range items {
				from := "built in"
				if !e.BuiltIn {
					from = "yours"
				}
				rows = append(rows, []string{e.Name, e.Title, e.Version, from, e.Description})
			}
			a.table([]string{"NAME", "TITLE", "VERSION", "FROM", "DESCRIPTION"}, rows)
			return nil
		},
	})
	var version string
	show := &cobra.Command{
		Use: "show NAME", Short: "Show an entry: its parameters and what it creates", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			e, err := c.CatalogueEntry(cmd.Context(), args[0], version)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(e)
			}
			fmt.Fprintf(a.io.Out, "%s %s — %s\n\nParameters:\n", e.Title, e.Version, e.Description)
			for _, p := range e.Parameters {
				req := ""
				if p.Required {
					req = " (required)"
				} else if p.Default != nil {
					req = fmt.Sprintf(" (default %v)", p.Default)
				}
				flag := "--param"
				if p.Type == api.ParamSecret {
					flag = "--secret-param"
				}
				fmt.Fprintf(a.io.Out, "  %s %s=…  %s, %s%s\n", flag, p.Name, p.Title, p.Type, req)
			}
			fmt.Fprintln(a.io.Out, "\nCreates:")
			for _, m := range e.Members {
				opt := ""
				if m.Optional {
					opt = " (with some parameters)"
				}
				fmt.Fprintf(a.io.Out, "  %s %s%s\n", m.Kind, m.Name, opt)
			}
			return nil
		},
	}
	show.Flags().StringVar(&version, "version", "", "an older version")
	add := &cobra.Command{
		Use: "add FILE", Short: "Add your own template from a YAML or JSON file (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			e, err := c.AddTemplate(cmd.Context(), string(body))
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(e)
			}
			fmt.Fprintf(a.io.Out, "Added %s %s. Install it with: enclave stack create NAME --template %s\n", e.Name, e.Version, e.Name)
			return nil
		},
	}
	var yes bool
	remove := &cobra.Command{
		Use: "remove NAME", Short: "Remove one of your own templates, with every version (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.confirm("Remove the template "+args[0]+" from the catalogue?", yes); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.RemoveTemplate(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(a.io.Out, "Removed", args[0])
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.AddCommand(show, add, remove)
	return cmd
}

// stackFlags are a stack's template and parameters.
type stackFlags struct {
	template, file, version string
	params, secrets         []string
	wait                    bool
}

func (f *stackFlags) bind(cmd *cobra.Command, create bool) {
	if create {
		cmd.Flags().StringVar(&f.template, "template", "", "a catalogue entry")
		cmd.Flags().StringVar(&f.file, "template-file", "", "a template of your own, from a file")
	}
	cmd.Flags().StringVar(&f.version, "version", "", "the template's version (default: the newest)")
	cmd.Flags().StringArrayVar(&f.params, "param", nil, "a parameter, as name=value (repeat; name= clears it on update)")
	cmd.Flags().StringArrayVar(&f.secrets, "secret-param", nil, "a secret parameter, as name=value (repeat)")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until every member is ready")
}

func (f *stackFlags) spec(update bool) (api.StackSpec, error) {
	s := api.StackSpec{Template: f.template, Version: f.version}
	if f.file != "" {
		b, err := os.ReadFile(f.file)
		if err != nil {
			return s, err
		}
		s.TemplateBody = string(b)
	}
	if len(f.params) > 0 {
		s.Parameters = map[string]any{}
	}
	for _, p := range f.params {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return s, usagef("a parameter is name=value, not %q", p)
		}
		switch {
		case v == "" && update:
			s.Parameters[k] = nil
		case v == "true" || v == "false":
			s.Parameters[k] = v == "true"
		default:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				s.Parameters[k] = n
			} else {
				s.Parameters[k] = v
			}
		}
	}
	for _, p := range f.secrets {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return s, usagef("a secret parameter is name=value, not %q", p)
		}
		if s.SecretParameters == nil {
			s.SecretParameters = map[string]string{}
		}
		s.SecretParameters[k] = v
	}
	return s, nil
}

func (a *app) stackCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindStack, "stack", []string{"stacks"}, "Stacks: products installed from the catalogue, as one thing")
	var cf stackFlags
	create := &cobra.Command{
		Use: "create NAME (--template ENTRY | --template-file FILE) [--param k=v]…", Short: "Install a stack", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (cf.template == "") == (cf.file == "") {
				return usagef("pass --template or --template-file, one of them")
			}
			spec, err := cf.spec(false)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindStack, args[0], spec, cf.wait)
		},
	}
	cf.bind(create, true)
	var uf stackFlags
	update := &cobra.Command{
		Use: "update NAME [--param k=v]… [--version V]", Short: "Change a stack's parameters or template version; its members follow", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := uf.spec(true)
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if spec.Version != "" {
				patch["version"] = spec.Version
			}
			if spec.Parameters != nil {
				patch["parameters"] = spec.Parameters
			}
			if spec.SecretParameters != nil {
				patch["secretParameters"] = spec.SecretParameters
			}
			return a.update(cmd.Context(), api.KindStack, args[0], patch, uf.wait)
		},
	}
	uf.bind(update, false)
	// The generic get shows the stack; members and outputs read better as tables.
	for _, sub := range cmd.Commands() {
		if sub.Name() == "get" {
			cmd.RemoveCommand(sub)
		}
	}
	get := &cobra.Command{
		Use: "get NAME", Short: "Show a stack: its members and outputs", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, api.KindStack, args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(r)
			}
			fmt.Fprintf(a.io.Out, "Stack %s: %s, from %v %v\n", r.Name, phaseText(r), r.Outputs[api.OutputTemplate], r.Outputs[api.OutputTemplateVersion])
			if r.Status.Message != "" {
				fmt.Fprintln(a.io.Out, r.Status.Message)
			}
			var members []api.StackMember
			b, _ := json.Marshal(r.Outputs[api.OutputMembers])
			_ = json.Unmarshal(b, &members)
			rows := [][]string{}
			for _, m := range members {
				rows = append(rows, []string{string(m.Kind), m.Name, string(m.Phase)})
			}
			a.table([]string{"KIND", "NAME", "PHASE"}, rows)
			if outs, ok := r.Outputs[api.OutputStackOutputs].(map[string]any); ok && len(outs) > 0 {
				fmt.Fprintln(a.io.Out)
				orows := [][]string{}
				for _, k := range sortedAnyKeys(outs) {
					orows = append(orows, []string{k, fmt.Sprint(outs[k])})
				}
				a.table([]string{"OUTPUT", "VALUE"}, orows)
			}
			return nil
		},
	}
	cmd.AddCommand(create, update, get)
	return cmd
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
