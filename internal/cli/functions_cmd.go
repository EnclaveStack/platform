package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Functions (L2-016 CLI-59, ADR-055): an image started once per event.

type functionFlags struct {
	image, schedule, zone string
	queues, onBuckets     []string
	timeout               time.Duration
	memory, concurrency   int
	env, secretEnv        []string
	buckets               []string
	wait                  bool
	place                 placementFlags
}

func (f *functionFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.image, "image", "", "container image started once per event, e.g. alpine:3.22")
	fl.StringVar(&f.schedule, "schedule", "", `run on a cron schedule: 5 fields ("*/5 * * * *") or @hourly, @daily, @weekly, @monthly`)
	fl.StringVar(&f.zone, "time-zone", "", "IANA time zone for the schedule (default: the machine's)")
	fl.StringArrayVar(&f.queues, "queue", nil, "run for each message of this queue (repeatable)")
	fl.StringArrayVar(&f.onBuckets, "on-bucket", nil, "run for each new object in BUCKET or BUCKET/FOLDER/ (repeatable)")
	fl.DurationVar(&f.timeout, "run-timeout", 0, fmt.Sprintf("stop a run after this long (%ds–%ds, default %ds)", api.FunctionTimeoutMin, api.FunctionTimeoutMax, api.FunctionTimeoutDefault))
	fl.IntVar(&f.memory, "memory", 0, fmt.Sprintf("memory limit in MiB (default %d)", api.FunctionMemoryDefaultMiB))
	fl.IntVar(&f.concurrency, "concurrency", 0, fmt.Sprintf("runs at the same time (%d–%d, default %d)", api.FunctionConcurrencyMin, api.FunctionConcurrencyMax, api.FunctionConcurrencyDef))
	fl.StringArrayVar(&f.env, "env", nil, "environment variable KEY=VALUE (repeatable)")
	fl.StringArrayVar(&f.secretEnv, "secret-env", nil, "environment variable from a secret KEY=SECRET_NAME (repeatable)")
	fl.BoolVar(&f.wait, "wait", false, "wait until ready")
	f.place.bind(cmd)
	bindBuckets(cmd, &f.buckets)
}

var triggerFlags = []string{"schedule", "time-zone", "queue", "on-bucket"}

// triggers turns the trigger flags into a function's triggers.
func (f *functionFlags) triggers() ([]api.FunctionTrigger, error) {
	var out []api.FunctionTrigger
	if f.schedule != "" {
		out = append(out, api.FunctionTrigger{Schedule: f.schedule, TimeZone: f.zone})
	} else if f.zone != "" {
		return nil, usagef("--time-zone goes with --schedule")
	}
	for _, q := range f.queues {
		out = append(out, api.FunctionTrigger{Queue: q})
	}
	for _, b := range f.onBuckets {
		name, prefix, _ := strings.Cut(b, "/")
		out = append(out, api.FunctionTrigger{Bucket: name, Prefix: prefix})
	}
	return out, nil
}

func (a *app) functionCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindFunction, "function", []string{"functions", "fn"}, "Functions: an image started once per event")
	var cf functionFlags
	create := &cobra.Command{
		Use: "create NAME --image IMAGE [--schedule CRON] [--queue Q] [--on-bucket B/FOLDER/] [-- COMMAND...]", Short: "Create a function",
		Long: "Create a function: the image starts once per event and reads it from $ENCLAVE_EVENT_FILE.\n" +
			"Triggers are optional: invoke it with `enclave function invoke`, or send a topic to it.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cf.image == "" {
				return usagef("pass --image")
			}
			spec, err := cf.spec(args[1:])
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindFunction, args[0], spec, cf.wait)
		},
	}
	cf.bind(create)
	var uf functionFlags
	update := &cobra.Command{
		Use: "update NAME [-- COMMAND...]", Short: "Change a function (only the flags you pass; trigger flags replace all triggers)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("image") {
				patch["image"] = uf.image
			}
			if len(args) > 1 {
				patch["command"] = args[1:]
			}
			if ch("run-timeout") {
				patch["timeoutSeconds"] = int(uf.timeout.Seconds())
			}
			if ch("memory") {
				patch["memoryMiB"] = uf.memory
			}
			if ch("concurrency") {
				patch["concurrency"] = uf.concurrency
			}
			if anyChanged(ch, triggerFlags) {
				t, err := uf.triggers()
				if err != nil {
					return err
				}
				patch["triggers"] = t
			}
			for flag, key := range map[string]string{"env": "env", "secret-env": "secretEnv"} {
				if ch(flag) {
					v, err := parsePairs(flag, map[string][]string{"env": uf.env, "secret-env": uf.secretEnv}[flag])
					if err != nil {
						return err
					}
					patch[key] = v
				}
			}
			if ch("bucket") {
				b, err := parseBuckets(uf.buckets)
				if err != nil {
					return err
				}
				patch["bucketAccess"] = b
			}
			uf.place.patch(ch, patch)
			return a.update(cmd.Context(), api.KindFunction, args[0], patch, uf.wait)
		},
	}
	uf.bind(update)
	var data, dataFile string
	var wait bool
	invoke := &cobra.Command{
		Use: "invoke NAME [--data JSON | --data-file FILE] [--wait]", Short: "Run a function with a JSON body", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := json.RawMessage("{}")
			switch {
			case data != "" && dataFile != "":
				return usagef("use --data or --data-file")
			case data != "":
				body = json.RawMessage(data)
			case dataFile != "":
				b, err := a.readFileFlag("data-file", dataFile)
				if err != nil {
					return err
				}
				body = json.RawMessage(b)
			}
			if !json.Valid(body) {
				return usagef("the body must be JSON")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			fn, err := a.find(cmd.Context(), c, api.KindFunction, args[0])
			if err != nil {
				return err
			}
			d, err := c.Invoke(cmd.Context(), fn.ID, body, wait)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(d)
			}
			if !wait || d.Status == api.RunRunning {
				fmt.Fprintf(a.io.Out, "Run %s started.\n", d.ID)
				return nil
			}
			_, _ = io.WriteString(a.io.Out, d.OutputTail)
			if d.Status != api.RunSucceeded {
				code := ""
				if d.ExitCode != nil {
					code = " (exit " + strconv.Itoa(*d.ExitCode) + ")"
				}
				return fmt.Errorf("run %s %s%s: %s", d.ID, d.Status, code, d.Message)
			}
			return nil
		},
	}
	invoke.Flags().StringVar(&data, "data", "", "the JSON body (default {})")
	invoke.Flags().StringVar(&dataFile, "data-file", "", "read the JSON body from a file, or - for stdin")
	invoke.Flags().BoolVar(&wait, "wait", false, fmt.Sprintf("wait up to %s for the run to end and print its output", api.InvokeWaitMax))
	cmd.AddCommand(create, update, invoke, a.runCmd(api.KindFunction), a.runsCmd(api.KindFunction))
	return cmd
}

func (f *functionFlags) spec(command []string) (api.FunctionSpec, error) {
	env, err := parsePairs("env", f.env)
	if err != nil {
		return api.FunctionSpec{}, err
	}
	senv, err := parsePairs("secret-env", f.secretEnv)
	if err != nil {
		return api.FunctionSpec{}, err
	}
	buckets, err := parseBuckets(f.buckets)
	if err != nil {
		return api.FunctionSpec{}, err
	}
	t, err := f.triggers()
	if err != nil {
		return api.FunctionSpec{}, err
	}
	return api.FunctionSpec{Image: f.image, Command: command, Env: env, SecretEnv: senv, MemoryMiB: f.memory, TimeoutSeconds: int(f.timeout.Seconds()),
		Concurrency: f.concurrency, Triggers: t, Network: f.place.network, Subnet: f.place.subnet, BucketAccess: buckets, Placed: f.place.placed()}, nil
}

// Previews (L2-016 CLI-60, ADR-056): `enclave app preview create / ls / rm`.

type previewFlags struct {
	on                       bool
	suffix, zone, tunnel, tl string
	ttl, max                 int
}

var previewFlagNames = []string{"previews", "preview-domain", "preview-zone", "preview-tunnel", "preview-tls", "preview-ttl", "preview-max"}

func (f *previewFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&f.on, "previews", false, "make an app per pull request (needs --git)")
	fl.StringVar(&f.suffix, "preview-domain", "", "previews answer at NAME.SUFFIX, e.g. preview.example.com")
	fl.StringVar(&f.zone, "preview-zone", "", "the zone that holds the preview domain")
	fl.StringVar(&f.tunnel, "preview-tunnel", "", "the tunnel previews are reached through")
	fl.StringVar(&f.tl, "preview-tls", "", "the previews' certificates: auto, internal or off (default auto with a zone or tunnel, else internal)")
	fl.IntVar(&f.ttl, "preview-ttl", 0, fmt.Sprintf("hours a preview lives without a push (%d–%d, default %d)", api.PreviewTTLMin, api.PreviewTTLMax, api.PreviewTTLDefault))
	fl.IntVar(&f.max, "preview-max", 0, fmt.Sprintf("previews at the same time (%d–%d, default %d)", api.PreviewMaxActiveMin, api.PreviewMaxActiveMax, api.PreviewMaxDefault))
}

// previews builds the settings from the flags, starting from cur; nil when no preview flag was passed.
func (f *previewFlags) previews(ch func(string) bool, cur *api.Previews) *api.Previews {
	if !anyChanged(ch, previewFlagNames) {
		return nil
	}
	p := api.Previews{Enabled: true}
	if cur != nil {
		p = *cur
	}
	if ch("previews") {
		p.Enabled = f.on
	}
	if ch("preview-ttl") {
		p.TTLHours = f.ttl
	}
	if ch("preview-max") {
		p.MaxActive = f.max
	}
	if anyChanged(ch, previewFlagNames[1:5]) {
		d := api.PreviewDomain{}
		if p.Domain != nil {
			d = *p.Domain
		}
		for flag, v := range map[string]*string{"preview-domain": &d.Suffix, "preview-zone": &d.Zone, "preview-tunnel": &d.Tunnel, "preview-tls": &d.TLS} {
			if ch(flag) {
				*v = map[string]string{"preview-domain": f.suffix, "preview-zone": f.zone, "preview-tunnel": f.tunnel, "preview-tls": f.tl}[flag]
			}
		}
		p.Domain = &d
		if d.Suffix == "" {
			p.Domain = nil
		}
	}
	return &p
}

func (a *app) previewCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "preview", Aliases: []string{"previews"}, Short: "An app's previews: one app per pull request or ref"}
	var ref, name string
	var pr int
	var wait bool
	create := &cobra.Command{
		Use: "create APP (--ref REF | --pr N) [--name NAME]", Short: "Make (or update) a preview of a ref", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (ref == "") == (pr == 0) {
				return usagef("pass --ref REF or --pr N")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			app, err := a.find(cmd.Context(), c, api.KindApp, args[0])
			if err != nil {
				return err
			}
			if pr > 0 {
				var spec api.AppSpec
				_ = json.Unmarshal(app.Spec, &spec)
				if spec.Source == nil {
					return usagef("the app %s isn't built from Git", app.Name)
				}
				ref = api.PullRequestRef(spec.Source.Git, pr)
				if name == "" {
					name = api.PreviewNameForPR(app.Name, pr)
				}
			}
			res, err := c.CreatePreview(cmd.Context(), app.ID, api.CreatePreview{Ref: ref, Name: name})
			if err != nil {
				return err
			}
			if wait {
				if res, err = a.wait(cmd.Context(), c, res); err != nil {
					return err
				}
			}
			return a.show(res)
		},
	}
	create.Flags().StringVar(&ref, "ref", "", "the branch, tag or commit to build")
	create.Flags().IntVar(&pr, "pr", 0, "the pull request (merge request) number to build")
	create.Flags().StringVar(&name, "name", "", "the preview's name (default APP-REF)")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	ls := &cobra.Command{
		Use: "ls APP", Aliases: []string{"list"}, Short: "List an app's previews", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			app, err := a.find(cmd.Context(), c, api.KindApp, args[0])
			if err != nil {
				return err
			}
			list, err := c.ListPreviews(cmd.Context(), app.ID)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.PreviewList{Items: list})
			}
			rows := [][]string{}
			for _, p := range list {
				rows = append(rows, []string{p.Name, p.Ref, string(p.Phase), orDash(p.URL), p.ExpiresAt.Local().Format(time.DateTime)})
			}
			a.table([]string{"NAME", "REF", "PHASE", "URL", "EXPIRES"}, rows)
			return nil
		},
	}
	var yes bool
	rm := &cobra.Command{
		Use: "rm APP NAME", Aliases: []string{"delete"}, Short: "Delete a preview", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			app, err := a.find(cmd.Context(), c, api.KindApp, args[0])
			if err != nil {
				return err
			}
			list, err := c.ListPreviews(cmd.Context(), app.ID)
			if err != nil {
				return err
			}
			for _, p := range list {
				if p.Name == args[1] {
					return a.delete(cmd.Context(), api.KindApp, p.Name, yes, false)
				}
			}
			return fmt.Errorf("%s has no preview named %s", app.Name, args[1])
		},
	}
	rm.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.AddCommand(create, ls, rm)
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
