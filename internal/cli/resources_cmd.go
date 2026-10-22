package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// kindAliases maps what people type to kinds.
var kindAliases = map[string]api.Kind{
	"app": api.KindApp, "apps": api.KindApp,
	"database": api.KindDatabase, "databases": api.KindDatabase, "db": api.KindDatabase, "dbs": api.KindDatabase,
	"cache": api.KindCache, "caches": api.KindCache,
	"bucket": api.KindBucket, "buckets": api.KindBucket,
	"secret": api.KindSecret, "secrets": api.KindSecret,
	"domain": api.KindDomain, "domains": api.KindDomain,
	"job": api.KindJob, "jobs": api.KindJob,
	"backup": api.KindBackup, "backups": api.KindBackup,
	"network": api.KindNetwork, "networks": api.KindNetwork, "net": api.KindNetwork,
	"firewall": api.KindFirewall, "firewalls": api.KindFirewall, "fw": api.KindFirewall,
	"loadbalancer": api.KindLoadBalancer, "loadbalancers": api.KindLoadBalancer, "lb": api.KindLoadBalancer, "lbs": api.KindLoadBalancer,
	"zone": api.KindZone, "zones": api.KindZone,
	"record": api.KindRecord, "records": api.KindRecord,
	"vm": api.KindVM, "vms": api.KindVM,
	"volume": api.KindVolume, "volumes": api.KindVolume, "vol": api.KindVolume,
	"snapshot": api.KindSnapshot, "snapshots": api.KindSnapshot, "snap": api.KindSnapshot,
	"queue": api.KindQueue, "queues": api.KindQueue,
	"topic": api.KindTopic, "topics": api.KindTopic,
	"search":     api.KindSearch,
	"transcoder": api.KindTranscoder, "transcoders": api.KindTranscoder,
	"cdn": api.KindCDN, "cdns": api.KindCDN,
	"email": api.KindEmail, "emails": api.KindEmail,
	"dashboard": api.KindDashboard, "dashboards": api.KindDashboard,
	"alert": api.KindAlert, "alerts": api.KindAlert,
	"channel": api.KindChannel, "channels": api.KindChannel,
	"logexport": api.KindLogExport, "logexports": api.KindLogExport,
	"secretstore": api.KindSecretStore, "secretstores": api.KindSecretStore,
	"stack": api.KindStack, "stacks": api.KindStack,
}

func parseKind(s string) (api.Kind, error) {
	if k, ok := kindAliases[strings.ToLower(s)]; ok {
		return k, nil
	}
	return "", usagef("unknown kind %q: use app, database (db), cache, bucket, secret, domain, job, backup, network, firewall (fw), loadbalancer (lb), zone, record, vm, volume (vol), snapshot (snap), queue, topic, search, transcoder, cdn, email, dashboard, alert, channel, logexport, secretstore or stack", s)
}

const pollEvery = 500 * time.Millisecond

func (a *app) find(ctx context.Context, c *client.Client, kind api.Kind, name string) (api.Resource, error) {
	return c.FindResource(ctx, kind, name)
}

// show prints a resource as a table or the API's JSON.
func (a *app) show(r api.Resource) error {
	if a.output == "json" {
		return a.json(r)
	}
	rows := [][]string{{"ID", r.ID}, {"Kind", string(r.Kind)}, {"Name", r.Name}, {"Phase", phaseText(r)}}
	if r.Status.Message != "" {
		rows = append(rows, []string{"Message", r.Status.Message})
	}
	var spec map[string]any
	_ = json.Unmarshal(r.Spec, &spec)
	for _, k := range sortedKeys(spec) {
		rows = append(rows, []string{"spec." + k, fieldText(spec[k])})
	}
	for _, k := range sortedKeys(r.Outputs) {
		rows = append(rows, []string{k, fieldText(r.Outputs[k])})
	}
	if r.Kind == api.KindDatabase && r.Outputs["username"] != nil {
		rows = append(rows, []string{"Connect", connectionString(r)})
	}
	if r.OwnerID != nil {
		rows = append(rows, []string{"Managed by", *r.OwnerID})
	}
	rows = append(rows, []string{"Generation", fmt.Sprint(r.Generation)}, []string{"Created", r.CreatedAt.Local().Format(time.RFC3339)})
	a.table([]string{"FIELD", "VALUE"}, rows)
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func phaseText(r api.Resource) string {
	p := string(r.Status.Phase)
	if r.Status.Phase.InProgress() {
		for _, s := range r.Status.Steps {
			if s.State == api.StepActive {
				return p + " (" + s.Name + ")"
			}
		}
	}
	return p
}

// wait follows a resource to ready, printing progress to stderr (CLI-04).
func (a *app) wait(ctx context.Context, c *client.Client, r api.Resource) (api.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	last := ""
	out, err := c.WaitReady(ctx, r.ID, pollEvery, func(cur api.Resource) {
		line := fmt.Sprintf("%s %s: %s", cur.Kind, cur.Name, phaseText(cur))
		if cur.Status.Message != "" && cur.Status.Phase != api.PhaseReady {
			line += " — " + cur.Status.Message
		}
		if line != last {
			fmt.Fprintln(a.io.Err, line)
			last = line
		}
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return out, &waitTimeout{what: string(r.Kind) + " " + r.Name}
	}
	return out, err
}

func (a *app) waitGone(ctx context.Context, c *client.Client, r api.Resource) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	fmt.Fprintf(a.io.Err, "%s %s: deleting\n", r.Kind, r.Name)
	err := c.WaitGone(ctx, r.ID, pollEvery)
	if errors.Is(err, context.DeadlineExceeded) {
		return &waitTimeout{what: string(r.Kind) + " " + r.Name + " to be deleted"}
	}
	if err == nil {
		fmt.Fprintf(a.io.Err, "%s %s: deleted\n", r.Kind, r.Name)
	}
	return err
}

func (a *app) finish(ctx context.Context, c *client.Client, r api.Resource, wait bool) error {
	if wait {
		var err error
		if r, err = a.wait(ctx, c, r); err != nil {
			return err
		}
	}
	return a.show(r)
}

func (a *app) lsCmd() *cobra.Command {
	var kind, machine string
	var all bool
	cmd := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List resources", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var k api.Kind
			if kind != "" {
				var err error
				if k, err = parseKind(kind); err != nil {
					return err
				}
			}
			return a.listWhere(cmd.Context(), client.ListOptions{Kind: k, AllProjects: all, Machine: machine})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only this kind")
	cmd.Flags().StringVar(&machine, "machine", "", "only what runs on this machine")
	cmd.Flags().BoolVarP(&all, "all-projects", "A", false, "every project you can see, with a PROJECT column")
	return cmd
}

func (a *app) list(ctx context.Context, kind api.Kind, allProjects bool) error {
	return a.listWhere(ctx, client.ListOptions{Kind: kind, AllProjects: allProjects})
}

func (a *app) listWhere(ctx context.Context, o client.ListOptions) error {
	allProjects := o.AllProjects
	c, err := a.client()
	if err != nil {
		return err
	}
	items, err := c.ListAll(ctx, o)
	if err != nil {
		return err
	}
	many, _ := a.manyMachines(ctx, c)
	if a.output == "json" {
		return a.json(api.ResourceList{Items: items})
	}
	rows := [][]string{}
	for _, r := range items {
		row := []string{string(r.Kind), r.Name, phaseText(r), endpoint(r), r.ID}
		if many {
			row = append(row[:3], append([]string{machineOf(r)}, row[3:]...)...)
		}
		if allProjects {
			row = append([]string{r.Project}, row...)
		}
		rows = append(rows, row)
	}
	header := []string{"KIND", "NAME", "PHASE", "ENDPOINT", "ID"}
	if many {
		header = []string{"KIND", "NAME", "PHASE", "MACHINE", "ENDPOINT", "ID"}
	}
	if allProjects {
		header = append([]string{"PROJECT"}, header...)
	}
	a.table(header, rows)
	return nil
}

func endpoint(r api.Resource) string {
	if e, ok := r.Outputs["endpoint"]; ok {
		return fmt.Sprint(e)
	}
	if h, ok := r.Outputs["host"]; ok {
		return fmt.Sprintf("%v:%v", h, r.Outputs["port"])
	}
	if h, ok := r.Outputs["internalHost"]; ok {
		if p, ok := r.Outputs["internalPort"]; ok {
			return fmt.Sprintf("%v:%v (internal)", h, p)
		}
		return fmt.Sprintf("%v (internal)", h)
	}
	return "—"
}

func (a *app) getCmd() *cobra.Command {
	return &cobra.Command{
		Use: "get KIND NAME", Short: "Show one resource", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := parseKind(args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, kind, args[1])
			if err != nil {
				return err
			}
			return a.show(r)
		},
	}
}

func (a *app) deleteCmd() *cobra.Command {
	var yes, wait bool
	cmd := &cobra.Command{
		Use: "delete KIND NAME", Aliases: []string{"rm"}, Short: "Delete a resource (asks first; data is lost)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := parseKind(args[0])
			if err != nil {
				return err
			}
			return a.delete(cmd.Context(), kind, args[1], yes, wait)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it is gone")
	return cmd
}

func (a *app) delete(ctx context.Context, kind api.Kind, name string, yes, wait bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := a.find(ctx, c, kind, name)
	if err != nil {
		return err
	}
	warning := fmt.Sprintf("Delete the %s %s?", kind, name)
	if kind == api.KindDatabase || kind == api.KindCache || kind == api.KindBucket {
		warning = fmt.Sprintf("Delete the %s %s and all of its data? This cannot be undone.", kind, name)
	}
	if kind == api.KindBackup {
		// BAK-08: the schedule and run history go; the stored backups stay.
		warning = fmt.Sprintf("Delete the backup %s? Its schedule and run history are removed; backups already stored in the bucket are kept.", name)
	}
	if err := a.confirm(warning, yes); err != nil {
		return err
	}
	if _, err := c.DeleteResource(ctx, r.ID, nil); err != nil {
		return err
	}
	if wait {
		return a.waitGone(ctx, c, r)
	}
	fmt.Fprintf(a.io.Out, "Deleting %s %s.\n", kind, name)
	return nil
}

func (a *app) logsCmd() *cobra.Command {
	var tail int
	var buildLog bool
	cmd := &cobra.Command{
		Use: "logs KIND NAME", Short: "Show the last lines of a resource's output", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := parseKind(args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, kind, args[1])
			if err != nil {
				return err
			}
			logs := c.Logs
			if buildLog {
				logs = c.BuildLogs
			}
			lines, err := logs(cmd.Context(), r.ID, tail)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.LogList{Items: lines})
			}
			for _, l := range lines {
				fmt.Fprintf(a.io.Out, "%s %s %s\n", l.Time.Local().Format("15:04:05"), l.Stream, l.Message)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "how many lines (1–1000)")
	cmd.Flags().BoolVar(&buildLog, "build", false, "an app built from Git: show its latest build log")
	return cmd
}

func (a *app) eventsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "events KIND NAME", Short: "Show a resource's history", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := parseKind(args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, kind, args[1])
			if err != nil {
				return err
			}
			evs, err := c.Events(cmd.Context(), r.ID, 100)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.EventList{Items: evs})
			}
			rows := [][]string{}
			for _, e := range evs {
				rows = append(rows, []string{e.CreatedAt.Local().Format("2006-01-02 15:04:05"), e.Type, e.Message})
			}
			a.table([]string{"TIME", "TYPE", "MESSAGE"}, rows)
			return nil
		},
	}
}

func (a *app) retryCmd() *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use: "retry KIND NAME", Short: "Retry a failed resource (reconcile)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := parseKind(args[0])
			if err != nil {
				return err
			}
			return a.action(cmd.Context(), kind, args[1], api.ActionReconcile, wait)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	return cmd
}

func (a *app) action(ctx context.Context, kind api.Kind, name, action string, wait bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := a.find(ctx, c, kind, name)
	if err != nil {
		return err
	}
	r, err = c.Action(ctx, r.ID, action)
	if err != nil {
		return err
	}
	if wait && action != api.ActionRestart {
		return a.finish(ctx, c, r, true)
	}
	fmt.Fprintf(a.io.Out, "%s requested for %s %s.\n", strings.ToUpper(action[:1])+action[1:], kind, name)
	return nil
}

// kindCmd builds `enclave <kind> list|create|update|delete|...`.
func (a *app) kindCmd(kind api.Kind, use string, aliases []string, short string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Aliases: aliases, Short: short}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List " + string(kind) + "s", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.list(cmd.Context(), kind, false) },
	})
	cmd.AddCommand(&cobra.Command{
		Use: "get NAME", Short: "Show a " + string(kind), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, kind, args[0])
			if err != nil {
				return err
			}
			return a.show(r)
		},
	})
	var yes, wait bool
	del := &cobra.Command{
		Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a " + string(kind), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.delete(cmd.Context(), kind, args[0], yes, wait)
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	del.Flags().BoolVar(&wait, "wait", false, "wait until it is gone")
	cmd.AddCommand(del)
	return cmd
}

func (a *app) create(ctx context.Context, kind api.Kind, name string, spec any, wait bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.CreateResource(ctx, kind, name, spec)
	if err != nil {
		return err
	}
	if !wait {
		fmt.Fprintf(a.io.Err, "Creating %s %s. Follow it with: enclave get %s %s\n", kind, name, kind, name)
	}
	return a.finish(ctx, c, r, wait)
}

func (a *app) update(ctx context.Context, kind api.Kind, name string, patch map[string]any, wait bool) error {
	if len(patch) == 0 {
		return usagef("nothing to change: pass at least one flag")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := a.find(ctx, c, kind, name)
	if err != nil {
		return err
	}
	gen := r.Generation
	r, err = c.UpdateResource(ctx, r.ID, patch, &gen)
	if err != nil {
		return err
	}
	return a.finish(ctx, c, r, wait)
}

func (a *app) dbCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindDatabase, "db", []string{"database", "databases"}, "Databases: PostgreSQL, MySQL, MongoDB or ClickHouse")
	var engine, version string
	var wait bool
	var cp placementFlags
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a database (engine and version can't change later)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindDatabase, args[0], api.DatabaseSpec{Engine: engine, Version: version, Network: cp.network, Subnet: cp.subnet, Placed: cp.placed()}, wait)
		},
	}
	var versions []string
	for _, e := range api.DatabaseEngines {
		versions = append(versions, e+" "+strings.Join(api.DatabaseEngineVersions[e], ", "))
	}
	create.Flags().StringVar(&engine, "engine", api.DatabaseEngine, "engine: "+strings.Join(api.DatabaseEngines, ", "))
	create.Flags().StringVar(&version, "version", "", "version (default the engine's own): "+strings.Join(versions, "; "))
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	cp.bind(create)
	var up placementFlags
	var uwait bool
	update := &cobra.Command{
		Use: "update NAME", Short: "Move a database to another network or subnet (its data is kept)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			up.patch(cmd.Flags().Changed, patch)
			return a.update(cmd.Context(), api.KindDatabase, args[0], patch, uwait)
		},
	}
	up.bind(update)
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	var restartWait bool
	restart := &cobra.Command{Use: "restart NAME", Short: "Restart a database", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), api.KindDatabase, args[0], api.ActionRestart, restartWait)
		}}
	cmd.AddCommand(create, update, restart)
	return cmd
}

func (a *app) cacheCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindCache, "cache", []string{"caches"}, "Redis caches")
	var mem int
	var wait bool
	var cp placementFlags
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a Redis cache", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindCache, args[0], api.CacheSpec{Engine: api.CacheEngine, Version: api.CacheVersion, MaxMemoryMiB: mem,
				Network: cp.network, Subnet: cp.subnet, Placed: cp.placed()}, wait)
		},
	}
	create.Flags().IntVar(&mem, "max-memory", api.CacheMemoryDefault, "max memory in MiB (64–4096)")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	cp.bind(create)
	var umem int
	var uwait bool
	var up placementFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a cache's max memory or network", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("max-memory") {
				patch["maxMemoryMiB"] = umem
			}
			up.patch(cmd.Flags().Changed, patch)
			return a.update(cmd.Context(), api.KindCache, args[0], patch, uwait)
		},
	}
	up.bind(update)
	update.Flags().IntVar(&umem, "max-memory", 0, "max memory in MiB (64–4096)")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	restart := &cobra.Command{Use: "restart NAME", Short: "Restart a cache", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), api.KindCache, args[0], api.ActionRestart, false)
		}}
	cmd.AddCommand(create, update, restart)
	return cmd
}

func (a *app) bucketCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindBucket, "bucket", []string{"buckets"}, "S3-compatible buckets")
	var versioning, wait bool
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a bucket", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindBucket, args[0], api.BucketSpec{Versioning: versioning}, wait)
		},
	}
	create.Flags().BoolVar(&versioning, "versioning", false, "keep old versions of objects")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	var uver, uwait bool
	update := &cobra.Command{
		Use: "update NAME", Short: "Turn versioning on or off", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("versioning") {
				patch["versioning"] = uver
			}
			return a.update(cmd.Context(), api.KindBucket, args[0], patch, uwait)
		},
	}
	update.Flags().BoolVar(&uver, "versioning", false, "keep old versions of objects")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update)
	return cmd
}

func (a *app) domainCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindDomain, "domain", []string{"domains"}, "Domains: hostnames with HTTPS, routed to an app")
	var hostname, target, tls, zone string
	var port int
	var noRedirect, wait bool
	create := &cobra.Command{
		Use: "create NAME", Short: "Serve an app on a hostname with automatic HTTPS", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := api.DomainSpec{Hostname: hostname, App: target, Port: port, TLS: tls, Zone: zone}
			if noRedirect {
				f := false
				spec.RedirectHTTP = &f
			}
			return a.create(cmd.Context(), api.KindDomain, args[0], spec, wait)
		},
	}
	create.Flags().StringVar(&hostname, "hostname", "", "hostname to serve, e.g. api.example.com or web.localhost")
	create.Flags().StringVar(&target, "app", "", "app to route to")
	create.Flags().IntVar(&port, "port", 0, "app port (default: the app's port)")
	create.Flags().StringVar(&tls, "tls", "", "auto (Let's Encrypt), internal (EnclaveStack CA) or off; default internal for .localhost, else auto")
	create.Flags().BoolVar(&noRedirect, "no-redirect", false, "serve plain HTTP too instead of redirecting it to HTTPS")
	create.Flags().StringVar(&zone, "zone", "", "get the certificate through this zone's DNS (DNS-01); allows a wildcard hostname like '*.example.com'")
	create.Flags().BoolVar(&wait, "wait", false, "wait until the route and certificate are ready")
	_ = create.MarkFlagRequired("hostname")
	_ = create.MarkFlagRequired("app")
	var uapp, utls, uzone string
	var uport int
	var uredirect, uwait bool
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a domain's app, port or TLS (the hostname can't change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			ch := cmd.Flags().Changed
			if ch("app") {
				patch["app"] = uapp
			}
			if ch("port") {
				patch["port"] = nilIfZeroInt(uport)
			}
			if ch("tls") {
				patch["tls"] = utls
			}
			if ch("redirect") {
				patch["redirectHttp"] = uredirect
			}
			if ch("zone") {
				patch["zone"] = nilIfEmpty(uzone)
			}
			return a.update(cmd.Context(), api.KindDomain, args[0], patch, uwait)
		},
	}
	update.Flags().StringVar(&uapp, "app", "", "app to route to")
	update.Flags().IntVar(&uport, "port", 0, "app port (0: the app's port)")
	update.Flags().StringVar(&utls, "tls", "", "auto, internal or off")
	update.Flags().BoolVar(&uredirect, "redirect", true, "redirect plain HTTP to HTTPS")
	update.Flags().StringVar(&uzone, "zone", "", "zone to get the certificate through (empty: back to HTTP-01)")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	ca := &cobra.Command{
		Use: "ca", Short: "Print the EnclaveStack root certificate, to trust internal-TLS domains", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			pem, err := c.RootCA(cmd.Context())
			if err != nil {
				return err
			}
			_, err = a.io.Out.Write(pem)
			return err
		},
	}
	cmd.AddCommand(create, update, ca)
	return cmd
}

// jobFlags are shared by job create and job update.
type jobFlags struct {
	schedule, zone, target, image, overlap string
	timeout                                time.Duration
	memory                                 int
	env, secretEnv                         []string
	wait                                   bool
	place                                  placementFlags
	volumes                                []string
}

func (f *jobFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.schedule, "schedule", "", `cron schedule: 5 fields ("0 3 * * *") or @hourly, @daily, @weekly, @monthly`)
	cmd.Flags().StringVar(&f.zone, "time-zone", "", "IANA time zone for the schedule, e.g. Asia/Kolkata (default: the machine's)")
	cmd.Flags().StringVar(&f.target, "app", "", "run with this app's image and environment")
	cmd.Flags().StringVar(&f.image, "image", "", "run this image instead of an app's")
	cmd.Flags().StringVar(&f.overlap, "overlap", "", "forbid (default: skip a run while one is going) or allow")
	cmd.Flags().DurationVar(&f.timeout, "run-timeout", 0, "stop a run after this long (1s–24h, default 1h)")
	cmd.Flags().IntVar(&f.memory, "memory", 0, "memory limit in MiB (64–16384, default 256)")
	cmd.Flags().StringArrayVar(&f.env, "env", nil, "environment variable KEY=VALUE (repeatable)")
	cmd.Flags().StringArrayVar(&f.secretEnv, "secret-env", nil, "environment variable from a secret KEY=SECRET_NAME (repeatable)")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
	f.place.bind(cmd)
	bindVolumes(cmd, &f.volumes)
}

// bindVolumes adds --volume to app and job commands (CLI-26).
func bindVolumes(cmd *cobra.Command, v *[]string) {
	cmd.Flags().StringArrayVar(v, "volume", nil, `filesystem volume to mount, NAME:PATH or NAME:PATH:ro (repeatable; on update the list replaces the old one, "" for none)`)
}

func (a *app) jobCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindJob, "job", []string{"jobs"}, "Jobs: commands on a schedule")
	var cf jobFlags
	create := &cobra.Command{
		Use: "create NAME --schedule CRON (--app APP | --image IMAGE) [-- COMMAND...]", Short: "Run a command on a schedule",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := parsePairs("env", cf.env)
			if err != nil {
				return err
			}
			senv, err := parsePairs("secret-env", cf.secretEnv)
			if err != nil {
				return err
			}
			mounts, err := parseMounts(cf.volumes)
			if err != nil {
				return err
			}
			spec := api.JobSpec{Schedule: cf.schedule, TimeZone: cf.zone, App: cf.target, Image: cf.image, Command: args[1:], Env: env,
				SecretEnv: senv, TimeoutSeconds: int(cf.timeout.Seconds()), Overlap: cf.overlap, MemoryMiB: cf.memory, Network: cf.place.network, Subnet: cf.place.subnet, Placed: cf.place.placed(),
				Volumes: mounts}
			return a.create(cmd.Context(), api.KindJob, args[0], spec, cf.wait)
		},
	}
	cf.bind(create)
	_ = create.MarkFlagRequired("schedule")
	var uf jobFlags
	update := &cobra.Command{
		Use: "update NAME [-- COMMAND...]", Short: "Change a job (only the flags you pass; a command after -- replaces it)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			ch := cmd.Flags().Changed
			for flag, key := range map[string]string{"schedule": "schedule", "time-zone": "timeZone", "app": "app", "image": "image", "overlap": "overlap"} {
				if ch(flag) {
					v := map[string]string{"schedule": uf.schedule, "time-zone": uf.zone, "app": uf.target, "image": uf.image, "overlap": uf.overlap}[flag]
					if v == "" {
						patch[key] = nil
					} else {
						patch[key] = v
					}
				}
			}
			if ch("run-timeout") {
				patch["timeoutSeconds"] = int(uf.timeout.Seconds())
			}
			if ch("memory") {
				patch["memoryMiB"] = uf.memory
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
			if len(args) > 1 {
				patch["command"] = args[1:]
			}
			if ch("volume") {
				mounts, err := parseMounts(uf.volumes)
				if err != nil {
					return err
				}
				patch["volumes"] = mounts
			}
			uf.place.patch(ch, patch)
			return a.update(cmd.Context(), api.KindJob, args[0], patch, uf.wait)
		},
	}
	uf.bind(update)
	run, runs := a.runCmd(api.KindJob), a.runsCmd(api.KindJob)
	var runID string
	logs := &cobra.Command{
		Use: "logs NAME", Short: "Print a run's output (the latest run unless --run)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			job, err := a.find(cmd.Context(), c, api.KindJob, args[0])
			if err != nil {
				return err
			}
			id := runID
			if id == "" {
				list, err := c.ListRuns(cmd.Context(), job.ID, 20, "")
				if err != nil {
					return err
				}
				for _, r := range list.Items {
					if r.Status != api.RunSkipped {
						id = r.ID
						break
					}
				}
				if id == "" {
					return fmt.Errorf("job %s hasn't run yet", job.Name)
				}
			}
			d, err := c.GetRun(cmd.Context(), job.ID, id)
			if err != nil {
				return err
			}
			_, err = io.WriteString(a.io.Out, d.OutputTail)
			return err
		},
	}
	logs.Flags().StringVar(&runID, "run", "", "run ID (default: the latest run)")
	cmd.AddCommand(create, update, run, runs, logs)
	return cmd
}

// runCmd starts a run of a job or backup now.
func (a *app) runCmd(kind api.Kind) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use: "run NAME", Short: "Run a " + string(kind) + " now", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := a.find(cmd.Context(), c, kind, args[0])
			if err != nil {
				return err
			}
			run, err := c.RunNow(cmd.Context(), res.ID)
			if err != nil {
				return err
			}
			if !wait {
				if a.output == "json" {
					return a.json(run)
				}
				fmt.Fprintf(a.io.Out, "Run %s started.\n", run.ID)
				return nil
			}
			return a.waitRun(cmd.Context(), c, res, run.ID)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the run to end (and print a job's output)")
	return cmd
}

// runsCmd lists a job's or backup's runs.
func (a *app) runsCmd(kind api.Kind) *cobra.Command {
	return &cobra.Command{
		Use: "runs NAME", Short: "List a " + string(kind) + "'s runs, newest first", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := a.find(cmd.Context(), c, kind, args[0])
			if err != nil {
				return err
			}
			list, err := c.ListRuns(cmd.Context(), res.ID, 50, "")
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(list)
			}
			rows := [][]string{}
			for _, r := range list.Items {
				exit, dur := "", ""
				if r.ExitCode != nil {
					exit = strconv.Itoa(*r.ExitCode)
				}
				if r.FinishedAt != nil {
					dur = r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond).String()
				}
				rows = append(rows, []string{r.ID, r.StartedAt.Local().Format("2006-01-02 15:04:05"), r.Trigger, r.Status, dur, exit, r.Message})
			}
			a.table([]string{"RUN", "STARTED", "TRIGGER", "STATUS", "DURATION", "EXIT", "MESSAGE"}, rows)
			return nil
		},
	}
}

func (a *app) backupCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindBackup, "backup", []string{"backups"}, "Backups: scheduled dumps of a database or cache, with restore")
	type flags struct {
		source, bucket, endpoint, region, s3bucket, creds, prefix, schedule, zone string
		keep, keepDays                                                            int
		timeout                                                                   time.Duration
		wait                                                                      bool
	}
	bind := func(c *cobra.Command, f *flags, withSource bool) {
		if withSource {
			c.Flags().StringVar(&f.source, "source", "", "what to back up: database/NAME or cache/NAME")
		}
		c.Flags().StringVar(&f.bucket, "bucket", "", "store backups in this EnclaveStack bucket")
		c.Flags().StringVar(&f.endpoint, "s3-endpoint", "", "or in external S3: endpoint, e.g. https://s3.eu-central-1.amazonaws.com")
		c.Flags().StringVar(&f.region, "s3-region", "", "external S3 region")
		c.Flags().StringVar(&f.s3bucket, "s3-bucket", "", "external S3 bucket")
		c.Flags().StringVar(&f.creds, "s3-credentials", "", `secret holding {"accessKey","secretKey"} for external S3`)
		c.Flags().StringVar(&f.prefix, "prefix", "", "object key prefix (default enclave-backups)")
		c.Flags().StringVar(&f.schedule, "schedule", "", `cron schedule (default "0 3 * * *")`)
		c.Flags().StringVar(&f.zone, "time-zone", "", "IANA time zone for the schedule")
		c.Flags().IntVar(&f.keep, "keep", 0, "keep this many backups (1–365, default 7)")
		c.Flags().IntVar(&f.keepDays, "keep-days", 0, "also delete backups older than this many days")
		c.Flags().DurationVar(&f.timeout, "run-timeout", 0, "stop a backup after this long (default 2h)")
		c.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
	}
	dest := func(f *flags) api.BackupDestination {
		if f.endpoint != "" || f.s3bucket != "" || f.creds != "" {
			return api.BackupDestination{S3: &api.S3Destination{Endpoint: f.endpoint, Region: f.region, Bucket: f.s3bucket, CredentialsSecret: f.creds}}
		}
		return api.BackupDestination{Bucket: f.bucket}
	}
	var cf flags
	create := &cobra.Command{
		Use: "create NAME --source database/NAME|cache/NAME (--bucket BUCKET | --s3-endpoint URL --s3-bucket B --s3-credentials SECRET)", Short: "Back up a database or cache on a schedule",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, ok := strings.Cut(cf.source, "/")
			var src api.BackupSource
			switch {
			case ok && kind == "database":
				src.Database = name
			case ok && kind == "cache":
				src.Cache = name
			default:
				return usagef("--source takes database/NAME or cache/NAME")
			}
			spec := api.BackupSpec{Source: src, Destination: dest(&cf), Prefix: cf.prefix,
				Schedule: cf.schedule, TimeZone: cf.zone, Keep: cf.keep, KeepDays: cf.keepDays, TimeoutSeconds: int(cf.timeout.Seconds())}
			return a.create(cmd.Context(), api.KindBackup, args[0], spec, cf.wait)
		},
	}
	bind(create, &cf, true)
	var uf flags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a backup's schedule, retention or destination (the source can't change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("bucket") || ch("s3-endpoint") || ch("s3-bucket") || ch("s3-credentials") || ch("s3-region") {
				patch["destination"] = dest(&uf)
			}
			for flag, v := range map[string]any{"prefix": uf.prefix, "schedule": uf.schedule, "time-zone": uf.zone, "keep": uf.keep, "keep-days": uf.keepDays} {
				if ch(flag) {
					patch[map[string]string{"prefix": "prefix", "schedule": "schedule", "time-zone": "timeZone", "keep": "keep", "keep-days": "keepDays"}[flag]] = v
				}
			}
			if ch("run-timeout") {
				patch["timeoutSeconds"] = int(uf.timeout.Seconds())
			}
			return a.update(cmd.Context(), api.KindBackup, args[0], patch, uf.wait)
		},
	}
	bind(update, &uf, false)
	var runID, into string
	var replace, yes, wait bool
	restore := &cobra.Command{
		Use: "restore NAME --run ID (--new NAME | --replace)", Short: "Restore a backup run into a new database or cache, or over the source",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (into == "") == !replace {
				return usagef("choose --new NAME (recommended) or --replace")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			b, err := a.find(cmd.Context(), c, api.KindBackup, args[0])
			if err != nil {
				return err
			}
			req := api.RestoreRequest{RunID: runID, Mode: api.RestoreNew, Name: into}
			if replace {
				var s api.BackupSpec
				_ = json.Unmarshal(b.Spec, &s)
				source := s.Source.Database + s.Source.Cache
				if err := a.confirm(fmt.Sprintf("Replace all data in %s with backup run %s? A safety backup is taken first.", source, runID), yes); err != nil {
					return err
				}
				req = api.RestoreRequest{RunID: runID, Mode: api.RestoreReplace, ConfirmName: source}
			}
			res, err := c.Restore(cmd.Context(), b.ID, req)
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
	restore.Flags().StringVar(&runID, "run", "", "the backup run to restore (see backup runs)")
	restore.Flags().StringVar(&into, "new", "", "restore into a new database or cache with this name")
	restore.Flags().BoolVar(&replace, "replace", false, "replace the source's data (asks first; a safety backup is taken)")
	restore.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	restore.Flags().BoolVar(&wait, "wait", false, "wait until the restore is done")
	_ = restore.MarkFlagRequired("run")
	cmd.AddCommand(create, update, a.runCmd(api.KindBackup), a.runsCmd(api.KindBackup), restore)
	return cmd
}

// fieldText shows a value in the get table: nested objects and lists as compact JSON, not Go's map[…].
func fieldText(v any) string {
	switch n := v.(type) {
	case float64: // JSON numbers: whole ones print as integers, never 4.9152e+06
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return strconv.FormatInt(int64(n), 10)
		}
	case map[string]any, []any:
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// waitRun follows a run to its end, prints its output and fails unless it succeeded.
func (a *app) waitRun(ctx context.Context, c *client.Client, job api.Resource, runID string) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	for {
		d, err := c.GetRun(ctx, job.ID, runID)
		if err != nil {
			if ctx.Err() != nil {
				return &waitTimeout{what: "run " + runID}
			}
			return err
		}
		if d.Status != api.RunRunning {
			_, _ = io.WriteString(a.io.Out, d.OutputTail)
			fmt.Fprintf(a.io.Err, "%s %s run %s: %s — %s\n", job.Kind, job.Name, runID, d.Status, d.Message)
			if d.Status != api.RunSucceeded {
				return fmt.Errorf("%s %s run %s %s: %s", job.Kind, job.Name, runID, d.Status, d.Message)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return &waitTimeout{what: "run " + runID}
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func nilIfZeroInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// appFlags are shared by app create and app update.
type appFlags struct {
	image     string
	port      int
	env       []string
	secretEnv []string
	command   []string
	publish   bool
	memory    int
	wait      bool
	strategy  string
	health    string
	interval  int
	timeout   int
	retries   int
	start     int
	// Git source (CLI-17, ADR-019)
	git, ref, dockerfile, context, gitCreds string
	buildArgs                               []string
	replicas                                int
	place                                   placementFlags
	volumes                                 []string
}

var gitFlags = []string{"git", "ref", "dockerfile", "context", "git-credentials", "build-arg"}

// source builds a Git source from the flags, starting from cur (an update keeps unchanged fields).
func (f *appFlags) source(changed func(string) bool, cur *api.GitSource) (*api.GitSource, error) {
	s := api.GitSource{}
	if cur != nil {
		s = *cur
	}
	if changed("git") {
		s.Git = f.git
	}
	if changed("ref") {
		s.Ref = f.ref
	}
	if changed("dockerfile") {
		s.Dockerfile = f.dockerfile
	}
	if changed("context") {
		s.Context = f.context
	}
	if changed("git-credentials") {
		s.CredentialsSecret = f.gitCreds
	}
	if changed("build-arg") {
		args, err := parsePairs("build-arg", f.buildArgs)
		if err != nil {
			return nil, err
		}
		s.BuildArgs = args
	}
	if s.Git == "" || s.Ref == "" {
		return nil, usagef("building from Git needs --git URL and --ref (a branch, tag or commit)")
	}
	return &s, nil
}

func anyChanged(changed func(string) bool, names []string) bool {
	for _, n := range names {
		if changed(n) {
			return true
		}
	}
	return false
}

var healthFlags = []string{"health", "health-interval", "health-timeout", "health-retries", "health-start-period"}

func (f *appFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.image, "image", "", "container image, e.g. nginx:1.27-alpine (or build with --git)")
	cmd.Flags().StringVar(&f.git, "git", "", "build from this Git repository (https:// or ssh://) instead of an image")
	cmd.Flags().StringVar(&f.ref, "ref", "", "branch, tag or commit to build (with --git)")
	cmd.Flags().StringVar(&f.dockerfile, "dockerfile", "", "Dockerfile path relative to the context (default Dockerfile)")
	cmd.Flags().StringVar(&f.context, "context", "", "build context directory in the repository (default .)")
	cmd.Flags().StringVar(&f.gitCreds, "git-credentials", "", `secret holding {"token"} or {"privateKey","knownHosts"} for a private repository`)
	cmd.Flags().StringArrayVar(&f.buildArgs, "build-arg", nil, "build argument KEY=VALUE, not for secrets (repeatable)")
	cmd.Flags().IntVar(&f.port, "port", 0, "port the app listens on")
	cmd.Flags().StringArrayVar(&f.env, "env", nil, "environment variable KEY=VALUE (repeatable)")
	cmd.Flags().StringArrayVar(&f.secretEnv, "secret-env", nil, "environment variable from a secret KEY=SECRET_NAME (repeatable)")
	cmd.Flags().StringArrayVar(&f.command, "command", nil, "command argument (repeatable, replaces the image's command)")
	cmd.Flags().BoolVar(&f.publish, "publish", false, "publish the port on the host")
	cmd.Flags().IntVar(&f.memory, "memory", 0, "memory limit in MiB (64–16384, default 256)")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
	cmd.Flags().StringVar(&f.strategy, "strategy", "", "deploy strategy: rolling (default) or recreate")
	cmd.Flags().StringVar(&f.health, "health", "", "health check: http:/PATH, tcp, tcp:PORT, cmd:COMMAND or none")
	cmd.Flags().IntVar(&f.interval, "health-interval", 0, "seconds between health checks (default 5)")
	cmd.Flags().IntVar(&f.timeout, "health-timeout", 0, "seconds before one check gives up (default 3)")
	cmd.Flags().IntVar(&f.retries, "health-retries", 0, "checks before a new revision is discarded (default 12)")
	cmd.Flags().IntVar(&f.start, "health-start-period", 0, "seconds to wait before the first check")
	cmd.Flags().IntVar(&f.replicas, "replicas", 0, "containers per revision, 1–10 (default 1; one when --publish)")
	f.place.bind(cmd)
	bindVolumes(cmd, &f.volumes)
}

// healthCheck turns the --health flags into a check; nil when --health was not given.
func (f *appFlags) healthCheck(changed func(string) bool) (*api.HealthCheck, error) {
	if !changed("health") {
		for _, n := range healthFlags[1:] {
			if changed(n) {
				return nil, usagef("--%s needs --health too", n)
			}
		}
		return nil, nil
	}
	h := &api.HealthCheck{IntervalSeconds: f.interval, TimeoutSeconds: f.timeout, Retries: f.retries, StartPeriodSeconds: f.start}
	kind, arg, _ := strings.Cut(f.health, ":")
	switch kind {
	case "http":
		h.Type, h.Path = api.HealthHTTP, arg
	case "tcp":
		h.Type = api.HealthTCP
		if arg != "" {
			p, err := strconv.Atoi(arg)
			if err != nil {
				return nil, usagef("--health tcp:PORT needs a number, got %q", arg)
			}
			h.Port = p
		}
	case "cmd":
		h.Type, h.Command = api.HealthCommand, strings.Fields(arg)
	case "none":
		h.Type = api.HealthNone
	default:
		return nil, usagef("--health must be http:/PATH, tcp, tcp:PORT, cmd:COMMAND or none, got %q", f.health)
	}
	return h, nil
}

func parsePairs(flag string, pairs []string) (map[string]string, error) {
	if pairs == nil {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, usagef("--%s needs KEY=VALUE, got %q", flag, p)
		}
		out[k] = v
	}
	return out, nil
}

func (a *app) appCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindApp, "app", []string{"apps"}, "Apps: long-running containers")
	var cf appFlags
	create := &cobra.Command{
		Use: "create NAME (--image IMAGE | --git URL --ref REF)", Short: "Run a container image, or build one from Git", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			if ch("image") == anyChanged(ch, gitFlags) {
				return usagef("choose --image IMAGE or --git URL --ref REF")
			}
			var src *api.GitSource
			if !ch("image") {
				var err error
				if src, err = cf.source(ch, nil); err != nil {
					return err
				}
			}
			env, err := parsePairs("env", cf.env)
			if err != nil {
				return err
			}
			senv, err := parsePairs("secret-env", cf.secretEnv)
			if err != nil {
				return err
			}
			hc, err := cf.healthCheck(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			mounts, err := parseMounts(cf.volumes)
			if err != nil {
				return err
			}
			spec := api.AppSpec{Image: cf.image, Source: src, Port: cf.port, Env: env, SecretEnv: senv, Command: cf.command, Publish: cf.publish, MemoryMiB: cf.memory,
				Strategy: cf.strategy, HealthCheck: hc, Replicas: cf.replicas, Network: cf.place.network, Subnet: cf.place.subnet, Volumes: mounts, Placed: cf.place.placed()}
			return a.create(cmd.Context(), api.KindApp, args[0], spec, cf.wait)
		},
	}
	cf.bind(create)
	var uf appFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change an app (only the flags you pass; --env/--secret-env replace the whole set)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			ch := cmd.Flags().Changed
			if ch("image") && anyChanged(ch, gitFlags) {
				return usagef("choose --image IMAGE or the --git flags, not both")
			}
			if ch("image") {
				patch["image"], patch["source"] = uf.image, nil // switching to an image drops the Git source
			}
			if anyChanged(ch, gitFlags) {
				c, err := a.client()
				if err != nil {
					return err
				}
				cur, err := a.find(cmd.Context(), c, api.KindApp, args[0])
				if err != nil {
					return err
				}
				var spec api.AppSpec
				_ = json.Unmarshal(cur.Spec, &spec)
				src, err := uf.source(ch, spec.Source)
				if err != nil {
					return err
				}
				patch["source"], patch["image"] = src, nil
			}
			if ch("port") {
				patch["port"] = uf.port
			}
			if ch("publish") {
				patch["publish"] = uf.publish
			}
			if ch("memory") {
				patch["memoryMiB"] = uf.memory
			}
			if ch("command") {
				patch["command"] = uf.command
			}
			if ch("strategy") {
				patch["strategy"] = uf.strategy
			}
			if ch("replicas") {
				patch["replicas"] = uf.replicas
			}
			if ch("volume") {
				mounts, err := parseMounts(uf.volumes)
				if err != nil {
					return err
				}
				patch["volumes"] = mounts
			}
			uf.place.patch(ch, patch)
			hc, err := uf.healthCheck(ch)
			if err != nil {
				return err
			}
			if hc != nil {
				patch["healthCheck"] = hc
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
			return a.update(cmd.Context(), api.KindApp, args[0], patch, uf.wait)
		},
	}
	uf.bind(update)
	var rwait bool
	restart := &cobra.Command{Use: "restart NAME", Short: "Restart an app (same version)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), api.KindApp, args[0], api.ActionRestart, false)
		}}
	redeploy := &cobra.Command{Use: "redeploy NAME", Short: "Pull the image again (or build the ref's latest commit) and deploy a new revision", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), api.KindApp, args[0], api.ActionRedeploy, rwait)
		}}
	redeploy.Flags().BoolVar(&rwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update, restart, redeploy)
	return cmd
}

// readSecretValue takes the value from --value, stdin (--from-stdin) or a hidden prompt (CLI-08).
func (a *app) readSecretValue(value string, fromStdin, valueSet bool) (string, error) {
	switch {
	case valueSet && fromStdin:
		return "", usagef("use either --value or --from-stdin")
	case valueSet:
		fmt.Fprintln(a.io.Err, "Note: --value is visible in your shell history; prefer --from-stdin.")
		return value, nil
	case fromStdin:
		b, err := io.ReadAll(a.reader)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case a.io.Interactive:
		return a.promptSecret("Value: ")
	}
	return "", usagef("pass the value with --from-stdin (or --value)")
}

func (a *app) secretCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindSecret, "secret", []string{"secrets"}, "Encrypted secrets")
	var value, fromStore, key, field string
	var fromStdin, wait bool
	create := &cobra.Command{
		Use: "create NAME", Short: "Store a secret, or sync one from a secret store", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromStore != "" {
				if cmd.Flags().Changed("value") || fromStdin {
					return usagef("a secret from a store has no value of its own: drop --value and --from-stdin")
				}
				return a.create(cmd.Context(), api.KindSecret, args[0], api.SecretSpec{Source: &api.SecretSource{Store: fromStore, Key: key, Field: field}}, wait)
			}
			v, err := a.readSecretValue(value, fromStdin, cmd.Flags().Changed("value"))
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindSecret, args[0], api.SecretSpec{Value: v}, wait)
		},
	}
	create.Flags().StringVar(&value, "value", "", "the value (visible in shell history; prefer --from-stdin)")
	create.Flags().BoolVar(&fromStdin, "from-stdin", false, "read the value from standard input")
	create.Flags().StringVar(&fromStore, "from-store", "", "read the value from this secret store, and keep it in sync")
	create.Flags().StringVar(&key, "key", "", "with --from-store: the path, item or secret name in the store")
	create.Flags().StringVar(&field, "field", "", "with --from-store: the field of the value")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	var uvalue string
	var ustdin, uwait bool
	update := &cobra.Command{
		Use: "update NAME", Short: "Replace a secret's value", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.readSecretValue(uvalue, ustdin, cmd.Flags().Changed("value"))
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindSecret, args[0], map[string]any{"value": v}, uwait)
		},
	}
	update.Flags().StringVar(&uvalue, "value", "", "the new value")
	update.Flags().BoolVar(&ustdin, "from-stdin", false, "read the value from standard input")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	var reveal bool
	get := &cobra.Command{
		Use: "get NAME", Short: "Show a secret (add --reveal to print its value; audited)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, api.KindSecret, args[0])
			if err != nil {
				return err
			}
			if !reveal {
				return a.show(r)
			}
			v, err := c.RevealSecret(cmd.Context(), r.ID)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.SecretValue{Value: v})
			}
			fmt.Fprintln(a.io.Out, v)
			return nil
		},
	}
	get.Flags().BoolVar(&reveal, "reveal", false, "print the value")
	// Replace the generic get with the reveal-aware one.
	for _, c := range cmd.Commands() {
		if c.Name() == "get" {
			cmd.RemoveCommand(c)
		}
	}
	var refreshWait bool
	refresh := &cobra.Command{Use: "refresh NAME", Short: "Read a synced secret from its store now", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), api.KindSecret, args[0], api.ActionRefresh, refreshWait)
		}}
	refresh.Flags().BoolVar(&refreshWait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update, get, refresh)
	return cmd
}
