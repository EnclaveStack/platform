package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Metrics, dashboards, alerts, channels, log exports and secret stores (L2-002 CLI-40 – CLI-44).

func (a *app) metricsCmd() *cobra.Command {
	var metrics []string
	var since time.Duration
	var from, to string
	var points bool
	cmd := &cobra.Command{
		Use: "metrics KIND/NAME|machine", Short: "Show a resource's or the machine's metrics", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var start, end time.Time
			if from != "" || to != "" {
				var err error
				if start, err = time.Parse(time.RFC3339, from); from != "" && err != nil {
					return usagef("--from takes a time like 2026-10-22T09:00:00Z")
				}
				if end, err = time.Parse(time.RFC3339, to); to != "" && err != nil {
					return usagef("--to takes a time like 2026-10-22T10:00:00Z")
				}
			} else {
				start = time.Now().Add(-since)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var data api.MetricData
			if args[0] == api.MachineRef {
				data, err = c.MachineMetrics(cmd.Context(), metrics, start, end)
			} else {
				kind, name, ok := api.ParseRef(args[0])
				if !ok {
					return usagef("name a resource as kind/name, like app/api, or machine")
				}
				r, ferr := a.find(cmd.Context(), c, kind, name)
				if ferr != nil {
					return ferr
				}
				data, err = c.ResourceMetrics(cmd.Context(), r.ID, metrics, start, end)
			}
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(data)
			}
			if points {
				rows := [][]string{}
				for _, s := range data.Items {
					for _, p := range s.Points {
						rows = append(rows, []string{time.Unix(p.T, 0).Local().Format("2006-01-02 15:04:05"), s.Metric, api.FormatMetric(p.V, s.Unit)})
					}
				}
				a.table([]string{"TIME", "METRIC", "VALUE"}, rows)
				return nil
			}
			rows := [][]string{}
			for _, s := range data.Items {
				if len(s.Points) == 0 {
					rows = append(rows, []string{s.Metric, "—", "—", "—", "0"})
					continue
				}
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, p := range s.Points {
					lo, hi = math.Min(lo, p.V), math.Max(hi, p.V)
				}
				rows = append(rows, []string{s.Metric, api.FormatMetric(s.Points[len(s.Points)-1].V, s.Unit), api.FormatMetric(lo, s.Unit), api.FormatMetric(hi, s.Unit), fmt.Sprint(len(s.Points))})
			}
			a.table([]string{"METRIC", "LATEST", "MIN", "MAX", "POINTS"}, rows)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&metrics, "metric", nil, "only this metric (repeatable)")
	cmd.Flags().DurationVar(&since, "since", time.Hour, "how far back")
	cmd.Flags().StringVar(&from, "from", "", "start (RFC 3339), instead of --since")
	cmd.Flags().StringVar(&to, "to", "", "end (RFC 3339)")
	cmd.Flags().BoolVar(&points, "points", false, "print every point instead of a summary")
	return cmd
}

// parseWidget reads "title=…,metric=…,resources=app/api+app/web".
func parseWidget(s string) (api.Widget, error) {
	var w api.Widget
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return w, usagef("--widget takes title=…,metric=…,resources=kind/name+kind/name")
		}
		switch strings.TrimSpace(k) {
		case "title":
			w.Title = v
		case "metric":
			w.Metric = v
		case "resources":
			w.Resources = strings.FieldsFunc(v, func(r rune) bool { return r == '+' || r == ';' })
		default:
			return w, usagef("--widget: unknown key %q (title, metric, resources)", k)
		}
	}
	return w, nil
}

func (a *app) dashboardCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindDashboard, "dashboard", []string{"dashboards"}, "Dashboards: named pages of graphs")
	spec := func(flags []string) (api.DashboardSpec, error) {
		var s api.DashboardSpec
		for _, f := range flags {
			w, err := parseWidget(f)
			if err != nil {
				return s, err
			}
			s.Widgets = append(s.Widgets, w)
		}
		return s, nil
	}
	var widgets []string
	var wait bool
	create := &cobra.Command{Use: "create NAME", Short: "Create a dashboard", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := spec(widgets)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindDashboard, args[0], s, wait)
		}}
	create.Flags().StringArrayVar(&widgets, "widget", nil, `a graph: "title=API rps,metric=requests,resources=app/api" (repeatable, in order)`)
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	var uwidgets []string
	var uwait bool
	update := &cobra.Command{Use: "update NAME", Short: "Replace a dashboard's widgets", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := spec(uwidgets)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindDashboard, args[0], map[string]any{"widgets": s.Widgets}, uwait)
		}}
	update.Flags().StringArrayVar(&uwidgets, "widget", nil, "the new widgets (repeatable, in order)")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update)
	return cmd
}

// alertFlags are an alert's condition and delivery settings.
type alertFlags struct {
	crashLoop, failed, runFailed, offline, resources, channels []string
	events                                                     []string
	metric                                                     string
	above, below                                               float64
	forDur, window, repeat                                     time.Duration
	restarts                                                   int
	disabled, enabled, wait                                    bool
}

func (f *alertFlags) bind(c *cobra.Command, update bool) {
	fl := c.Flags()
	fl.StringArrayVar(&f.crashLoop, "crash-loop", nil, "fire when this resource (kind/name) keeps restarting (repeatable)")
	fl.StringArrayVar(&f.failed, "failed", nil, "fire when this resource fails (repeatable)")
	fl.StringArrayVar(&f.runFailed, "run-failed", nil, "fire when a run of this job, backup or transcoder fails (repeatable)")
	fl.StringArrayVar(&f.offline, "machine-offline", nil, "fire when this machine goes offline for --for (default 1m; repeatable)")
	fl.StringVar(&f.metric, "metric", "", "fire on this metric of --resource")
	fl.StringArrayVar(&f.events, "event", nil, "send each event of this type on --resource as it happens (repeatable): "+strings.Join(api.NotifiableEvents, ", "))
	fl.StringArrayVar(&f.resources, "resource", nil, "a resource the metric is read from (kind/name or machine; repeatable)")
	fl.Float64Var(&f.above, "above", 0, "fire when the metric is above this")
	fl.Float64Var(&f.below, "below", 0, "fire when the metric is below this")
	fl.DurationVar(&f.forDur, "for", 0, "how long the metric must cross the line (default 5m)")
	fl.IntVar(&f.restarts, "restarts", 0, "restarts that make a crash loop (default 3)")
	fl.DurationVar(&f.window, "window", 0, "the window restarts are counted in (default 10m)")
	fl.StringArrayVar(&f.channels, "channel", nil, "a channel to notify (repeatable)")
	fl.DurationVar(&f.repeat, "repeat", 0, "remind this often while firing (default never)")
	fl.BoolVar(&f.disabled, "disabled", false, "turn the alert off")
	if update {
		fl.BoolVar(&f.enabled, "enabled", false, "turn the alert back on")
	}
	fl.BoolVar(&f.wait, "wait", false, "wait until ready")
}

// condition builds the condition from the flags; ok is false when none was given.
func (f *alertFlags) condition(changed func(string) bool) (api.AlertCondition, bool, error) {
	set := 0
	var c api.AlertCondition
	if len(f.crashLoop) > 0 {
		set++
		c = api.AlertCondition{Type: api.ConditionCrashLoop, Resources: f.crashLoop, Restarts: f.restarts, WindowMinutes: int(f.window / time.Minute)}
	}
	if len(f.failed) > 0 {
		set++
		c = api.AlertCondition{Type: api.ConditionFailed, Resources: f.failed}
	}
	if len(f.runFailed) > 0 {
		set++
		c = api.AlertCondition{Type: api.ConditionRunFailed, Resources: f.runFailed}
	}
	if len(f.offline) > 0 {
		set++
		c = api.AlertCondition{Type: api.ConditionMachineOffline, ForMinutes: int(f.forDur / time.Minute)}
		for _, m := range f.offline {
			c.Resources = append(c.Resources, api.MachineRefPrefix+strings.TrimPrefix(m, api.MachineRefPrefix))
		}
	}
	if f.metric != "" {
		set++
		c = api.AlertCondition{Type: api.ConditionMetric, Metric: f.metric, Resources: f.resources, ForMinutes: int(f.forDur / time.Minute)}
		switch {
		case changed("above") == changed("below"):
			return c, false, usagef("with --metric, pass one of --above or --below")
		case changed("above"):
			c.Operator, c.Threshold = api.OperatorAbove, &f.above
		default:
			c.Operator, c.Threshold = api.OperatorBelow, &f.below
		}
	}
	if len(f.events) > 0 {
		set++
		c = api.AlertCondition{Type: api.ConditionEvent, Events: f.events, Resources: f.resources}
	}
	if set > 1 {
		return c, false, usagef("use one of --crash-loop, --failed, --run-failed, --machine-offline, --metric or --event")
	}
	return c, set == 1, nil
}

func (a *app) alertCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindAlert, "alert", []string{"alerts"}, "Alerts: rules that fire and notify channels")
	var cf alertFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create an alert", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cond, ok, err := cf.condition(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			if !ok {
				return usagef("pass a condition: --crash-loop, --failed, --run-failed, --machine-offline, or --metric or --event with --resource")
			}
			s := api.AlertSpec{Condition: cond, Channels: cf.channels, RepeatMinutes: int(cf.repeat / time.Minute)}
			if cf.disabled {
				off := false
				s.Enabled = &off
			}
			return a.create(cmd.Context(), api.KindAlert, args[0], s, cf.wait)
		}}
	cf.bind(create, false)
	var uf alertFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change an alert (only what you pass; a new condition replaces the old)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			cond, ok, err := uf.condition(ch)
			if err != nil {
				return err
			}
			if ok {
				patch["condition"] = cond
			}
			if ch("channel") {
				patch["channels"] = uf.channels
			}
			if ch("repeat") {
				patch["repeatMinutes"] = int(uf.repeat / time.Minute)
			}
			if uf.disabled || uf.enabled {
				patch["enabled"] = uf.enabled
			}
			return a.update(cmd.Context(), api.KindAlert, args[0], patch, uf.wait)
		}}
	uf.bind(update, true)
	update.MarkFlagsMutuallyExclusive("enabled", "disabled")
	incidents := &cobra.Command{Use: "incidents NAME", Short: "List an alert's incidents, newest first", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindAlert, args[0])
			if err != nil {
				return err
			}
			list, err := c.Incidents(cmd.Context(), r.ID, 50)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.IncidentList{Items: list})
			}
			rows := [][]string{}
			for _, in := range list {
				ended := "firing"
				if in.ResolvedAt != nil {
					ended = in.ResolvedAt.Local().Format("2006-01-02 15:04")
				}
				var sent []string
				for _, n := range in.Notifications {
					sent = append(sent, n.Channel+":"+n.Event+"="+n.Status)
				}
				rows = append(rows, []string{in.StartedAt.Local().Format("2006-01-02 15:04"), ended, in.Subject, in.Message, strings.Join(sent, " ")})
			}
			a.table([]string{"STARTED", "RESOLVED", "RESOURCE", "MESSAGE", "NOTIFICATIONS"}, rows)
			return nil
		}}
	cmd.AddCommand(create, update, incidents, a.testCmd(api.KindAlert))
	return cmd
}

// testCmd runs the test action and prints each outcome; a failure exits 1.
func (a *app) testCmd(kind api.Kind) *cobra.Command {
	return &cobra.Command{Use: "test NAME", Short: "Send a test and show what happened", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), kind, args[0])
			if err != nil {
				return err
			}
			res, err := c.Test(cmd.Context(), r.ID)
			if err != nil {
				return err
			}
			if a.output == "json" {
				if err := a.json(res); err != nil {
					return err
				}
			} else {
				rows := [][]string{}
				for _, it := range res.Items {
					state := "ok"
					if !it.OK {
						state = "FAILED"
					}
					rows = append(rows, []string{it.Target, state, it.Message})
				}
				a.table([]string{"TARGET", "RESULT", "MESSAGE"}, rows)
			}
			if !res.OK {
				return fmt.Errorf("the test of %s %s failed", kind, args[0])
			}
			return nil
		}}
}

// channelFlags are a channel's type and settings.
type channelFlags struct {
	email, from, webhook, secret, slack, discord, telegram, chatID string
	to                                                             []string
	wait                                                           bool
}

func (f *channelFlags) bind(c *cobra.Command) {
	fl := c.Flags()
	fl.StringVar(&f.email, "email", "", "send through this email service of the project")
	fl.StringVar(&f.from, "from", "", "the sender of alert emails")
	fl.StringArrayVar(&f.to, "to", nil, "a recipient of alert emails (repeatable)")
	fl.StringVar(&f.webhook, "webhook", "", "POST JSON to this https:// URL")
	fl.StringVar(&f.secret, "secret", "", "a secret that signs webhook bodies (HMAC-SHA256)")
	fl.StringVar(&f.slack, "slack", "", "the secret holding a Slack incoming-webhook URL")
	fl.StringVar(&f.discord, "discord", "", "the secret holding a Discord webhook URL")
	fl.StringVar(&f.telegram, "telegram", "", "the secret holding a Telegram bot token")
	fl.StringVar(&f.chatID, "chat-id", "", "the Telegram chat ID")
	fl.BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *channelFlags) spec() (api.ChannelSpec, error) {
	var s api.ChannelSpec
	n := 0
	if f.email != "" {
		n++
		s = api.ChannelSpec{Type: api.ChannelEmail, Email: &api.ChannelEmailSettings{Service: f.email, From: f.from, To: f.to}}
	}
	if f.webhook != "" {
		n++
		s = api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: f.webhook, Secret: f.secret}}
	}
	for typ, secret := range map[string]string{api.ChannelSlack: f.slack, api.ChannelDiscord: f.discord, api.ChannelTelegram: f.telegram} {
		if secret != "" {
			n++
			s = api.ChannelSpec{Type: typ, Secret: secret}
		}
	}
	if s.Type == api.ChannelTelegram {
		s.ChatID = f.chatID
	}
	if n != 1 {
		return s, usagef("pass exactly one of --email, --webhook, --slack, --discord or --telegram")
	}
	return s, nil
}

func (a *app) channelCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindChannel, "channel", []string{"channels"}, "Notification channels: where alerts go")
	var cf channelFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create a channel", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := cf.spec()
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindChannel, args[0], s, cf.wait)
		}}
	cf.bind(create)
	var uf channelFlags
	update := &cobra.Command{Use: "update NAME", Short: "Replace a channel's settings (its type can't change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := uf.spec()
			if err != nil {
				return err
			}
			patch := map[string]any{"type": s.Type, "email": s.Email, "webhook": s.Webhook, "secret": s.Secret, "chatId": s.ChatID}
			return a.update(cmd.Context(), api.KindChannel, args[0], patch, uf.wait)
		}}
	uf.bind(update)
	deliveries := &cobra.Command{Use: "deliveries NAME", Short: "List a channel's last deliveries", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindChannel, args[0])
			if err != nil {
				return err
			}
			list, err := c.Notifications(cmd.Context(), r.ID, 50)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.NotificationList{Items: list})
			}
			rows := [][]string{}
			for _, n := range list {
				rows = append(rows, []string{n.CreatedAt.Local().Format("2006-01-02 15:04:05"), n.Event, n.Alert, n.Subject, n.Status, fmt.Sprint(n.Attempts), n.LastError})
			}
			a.table([]string{"TIME", "EVENT", "ALERT", "RESOURCE", "STATUS", "ATTEMPTS", "LAST ERROR"}, rows)
			return nil
		}}
	cmd.AddCommand(create, update, deliveries, a.testCmd(api.KindChannel))
	return cmd
}

// exportFlags are a log export's sources and destination.
type exportFlags struct {
	sources, labels                                     []string
	bucket, s3Endpoint, s3Bucket, s3Region, s3Prefix    string
	s3Creds, httpURL, otlp, loki, headersSecret, filter string
	flush                                               time.Duration
	wait                                                bool
}

func (f *exportFlags) bind(c *cobra.Command) {
	fl := c.Flags()
	fl.StringArrayVar(&f.sources, "source", nil, "a resource whose logs are sent (kind/name, repeatable; default every resource of the project)")
	fl.StringVar(&f.bucket, "bucket", "", "a bucket of the project, optionally /folder: gzip files of JSON lines")
	fl.StringVar(&f.s3Endpoint, "s3-endpoint", "", "an S3-compatible endpoint")
	fl.StringVar(&f.s3Bucket, "s3-bucket", "", "the S3 bucket")
	fl.StringVar(&f.s3Region, "s3-region", "", "the S3 region")
	fl.StringVar(&f.s3Prefix, "s3-prefix", "", "the folder in the S3 bucket")
	fl.StringVar(&f.s3Creds, "s3-credentials", "", `the secret holding {"accessKey","secretKey"}`)
	fl.StringVar(&f.httpURL, "http", "", "POST JSON lines to this URL")
	fl.StringVar(&f.otlp, "otlp", "", "an OpenTelemetry (OTLP/HTTP) endpoint")
	fl.StringVar(&f.loki, "loki", "", "a Grafana Loki URL")
	fl.StringArrayVar(&f.labels, "label", nil, "an extra Loki label NAME=VALUE (repeatable)")
	fl.StringVar(&f.headersSecret, "headers-secret", "", "a secret holding a JSON object of headers to send (tokens, API keys)")
	fl.StringVar(&f.filter, "filter", "", "send only lines matching this regular expression")
	fl.DurationVar(&f.flush, "flush", 0, "how often to send (default 10s, 60s for buckets)")
	fl.BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *exportFlags) destination() (*api.LogDestination, error) {
	var d api.LogDestination
	n := 0
	if f.bucket != "" {
		n++
		b, prefix, _ := strings.Cut(f.bucket, "/")
		d.Bucket = &api.BucketFolder{Bucket: b, Prefix: prefix}
	}
	if f.s3Endpoint != "" || f.s3Bucket != "" {
		n++
		d.S3 = &api.LogS3{S3Destination: api.S3Destination{Endpoint: f.s3Endpoint, Region: f.s3Region, Bucket: f.s3Bucket, CredentialsSecret: f.s3Creds}, Prefix: f.s3Prefix}
	}
	if f.httpURL != "" {
		n++
		d.HTTP = &api.LogHTTP{URL: f.httpURL, HeadersSecret: f.headersSecret}
	}
	if f.otlp != "" {
		n++
		d.OTLP = &api.LogHTTP{Endpoint: f.otlp, HeadersSecret: f.headersSecret}
	}
	if f.loki != "" {
		n++
		labels, err := parseAttrs(f.labels)
		if err != nil {
			return nil, err
		}
		d.Loki = &api.LogHTTP{URL: f.loki, HeadersSecret: f.headersSecret, Labels: labels}
	}
	switch n {
	case 0:
		return nil, nil
	case 1:
		return &d, nil
	}
	return nil, usagef("pass one destination: --bucket, --s3-…, --http, --otlp or --loki")
}

func (a *app) logexportCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindLogExport, "logexport", []string{"logexports"}, "Log exports: app and service logs sent somewhere else")
	var cf exportFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create a log export", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := cf.destination()
			if err != nil {
				return err
			}
			if d == nil {
				return usagef("pass a destination: --bucket, --s3-…, --http, --otlp or --loki")
			}
			return a.create(cmd.Context(), api.KindLogExport, args[0], api.LogExportSpec{Sources: cf.sources, Filter: cf.filter,
				FlushSeconds: int(cf.flush / time.Second), Destination: *d}, cf.wait)
		}}
	cf.bind(create)
	var uf exportFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change a log export (only what you pass; a destination replaces the old one)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			d, err := uf.destination()
			if err != nil {
				return err
			}
			if d != nil {
				patch["destination"] = d
			}
			if ch("source") {
				patch["sources"] = uf.sources
			}
			if ch("filter") {
				patch["filter"] = uf.filter
			}
			if ch("flush") {
				patch["flushSeconds"] = int(uf.flush / time.Second)
			}
			return a.update(cmd.Context(), api.KindLogExport, args[0], patch, uf.wait)
		}}
	uf.bind(update)
	cmd.AddCommand(create, update)
	return cmd
}

// storeFlags are a secret store's provider settings.
type storeFlags struct {
	provider, address, mount, namespace, tokenSecret, connectURL, vault  string
	region, credentials, endpoint, siteURL, projectID, environment, path string
	refresh                                                              time.Duration
	wait                                                                 bool
}

func (f *storeFlags) bind(c *cobra.Command) {
	fl := c.Flags()
	fl.StringVar(&f.provider, "provider", "", "vault, onepassword, aws, doppler or infisical")
	fl.StringVar(&f.address, "address", "", "vault: the server's address")
	fl.StringVar(&f.mount, "mount", "", "vault: the KV v2 mount (default secret)")
	fl.StringVar(&f.namespace, "namespace", "", "vault: the namespace")
	fl.StringVar(&f.tokenSecret, "token-secret", "", "vault, onepassword, doppler: the secret holding the token")
	fl.StringVar(&f.connectURL, "connect-url", "", "onepassword: the Connect server's URL")
	fl.StringVar(&f.vault, "vault", "", "onepassword: the vault's name or ID")
	fl.StringVar(&f.region, "region", "", "aws: the region, like eu-west-1")
	fl.StringVar(&f.credentials, "credentials", "", `aws: the secret holding {"accessKey","secretKey"}; infisical: {"clientId","clientSecret"}`)
	fl.StringVar(&f.endpoint, "endpoint", "", "aws, doppler: another API endpoint")
	fl.StringVar(&f.siteURL, "site-url", "", "infisical: the site (default https://app.infisical.com)")
	fl.StringVar(&f.projectID, "project-id", "", "infisical: the project ID")
	fl.StringVar(&f.environment, "environment", "", "infisical: the environment slug")
	fl.StringVar(&f.path, "path", "", "infisical: the secret path (default /)")
	fl.DurationVar(&f.refresh, "refresh", 0, "how often values are read again (default 5m)")
	fl.BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *storeFlags) spec(provider string) api.SecretStoreSpec {
	s := api.SecretStoreSpec{Provider: provider, RefreshSeconds: int(f.refresh / time.Second)}
	switch provider {
	case api.ProviderVault:
		s.Vault = &api.VaultStore{Address: f.address, Mount: f.mount, Namespace: f.namespace, TokenSecret: f.tokenSecret}
	case api.ProviderOnePassword:
		s.OnePassword = &api.OnePasswordStore{ConnectURL: f.connectURL, Vault: f.vault, TokenSecret: f.tokenSecret}
	case api.ProviderAWS:
		s.AWS = &api.AWSStore{Region: f.region, CredentialsSecret: f.credentials, Endpoint: f.endpoint}
	case api.ProviderDoppler:
		s.Doppler = &api.DopplerStore{TokenSecret: f.tokenSecret, Endpoint: f.endpoint}
	case api.ProviderInfisical:
		s.Infisical = &api.InfisicalStore{SiteURL: f.siteURL, ProjectID: f.projectID, Environment: f.environment, Path: f.path, CredentialsSecret: f.credentials}
	}
	return s
}

func (a *app) secretstoreCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindSecretStore, "secretstore", []string{"secretstores"}, "Secret stores: Vault, 1Password, AWS Secrets Manager, Doppler, Infisical")
	var cf storeFlags
	create := &cobra.Command{Use: "create NAME", Short: "Connect a secret store (Ready means its credentials work)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cf.provider == "" {
				return usagef("pass --provider: vault, onepassword, aws, doppler or infisical")
			}
			return a.create(cmd.Context(), api.KindSecretStore, args[0], cf.spec(cf.provider), cf.wait)
		}}
	cf.bind(create)
	var uf storeFlags
	update := &cobra.Command{Use: "update NAME", Short: "Replace a store's settings (its provider can't change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindSecretStore, args[0])
			if err != nil {
				return err
			}
			var cur api.SecretStoreSpec
			if err := json.Unmarshal(r.Spec, &cur); err != nil {
				return err
			}
			s := uf.spec(cur.Provider)
			patch := map[string]any{cur.Provider: providerBlock(s)}
			if cmd.Flags().Changed("refresh") {
				patch["refreshSeconds"] = s.RefreshSeconds
			}
			gen := r.Generation
			r, err = c.UpdateResource(cmd.Context(), r.ID, patch, &gen)
			if err != nil {
				return err
			}
			return a.finish(cmd.Context(), c, r, uf.wait)
		}}
	uf.bind(update)
	cmd.AddCommand(create, update, a.testCmd(api.KindSecretStore))
	return cmd
}

func providerBlock(s api.SecretStoreSpec) any {
	switch {
	case s.Vault != nil:
		return s.Vault
	case s.OnePassword != nil:
		return s.OnePassword
	case s.AWS != nil:
		return s.AWS
	case s.Doppler != nil:
		return s.Doppler
	}
	return s.Infisical
}
