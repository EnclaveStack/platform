package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// v0.6 commands (L2-008, L2-002 CLI-29 – CLI-36): database engines, queues, topics, search, transcoders,
// CDNs and email.

// parseSize reads a byte count: 262144, 256KiB, 1MiB.
func parseSize(s string) (int, error) {
	units := []struct {
		suffix string
		mult   int
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"KB", 1000}, {"MB", 1000 * 1000}, {"B", 1}}
	mult := 1
	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = rest, u.mult
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, usagef("give a size like 262144, 256KiB or 1MiB")
	}
	return n * mult, nil
}

// wholeHours turns a duration flag into hours (retention is set in hours).
func wholeHours(flag string, d time.Duration) (int, error) {
	if d%time.Hour != 0 {
		return 0, usagef("--%s takes whole hours, like 24h or 336h", flag)
	}
	return int(d / time.Hour), nil
}

func wholeSeconds(flag string, d time.Duration) (int, error) {
	if d%time.Second != 0 {
		return 0, usagef("--%s takes whole seconds, like 30s or 5m", flag)
	}
	return int(d / time.Second), nil
}

// parseAttrs reads --attr K=V values.
func parseAttrs(list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range list {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, usagef("--attr needs NAME=VALUE; got %q", kv)
		}
		out[k] = v
	}
	return out, nil
}

// readBody takes a message body from the argument, or standard input for "-" or no argument.
func (a *app) readBody(args []string) (string, error) {
	if len(args) > 1 && args[1] != "-" {
		return args[1], nil
	}
	b, err := io.ReadAll(a.reader)
	return string(b), err
}

// resolve finds a resource of kind by name with a fresh client.
func (a *app) resolve(ctx context.Context, kind api.Kind, name string) (*client.Client, api.Resource, error) {
	c, err := a.client()
	if err != nil {
		return nil, api.Resource{}, err
	}
	r, err := a.find(ctx, c, kind, name)
	return c, r, err
}

// ─── queues (CLI-30) ───────────────────────────────────────────────────────

type queueFlags struct {
	visibility, retention time.Duration
	size, dlq             string
	maxReceives           int
	wait                  bool
}

func (f *queueFlags) bind(cmd *cobra.Command) {
	cmd.Flags().DurationVar(&f.visibility, "visibility-timeout", 0, "how long a received message stays hidden (1s–12h, default 30s)")
	cmd.Flags().DurationVar(&f.retention, "retention", 0, "how long a message is kept (1h–336h, default 96h)")
	cmd.Flags().StringVar(&f.size, "max-message-size", "", "largest body (1KiB–1MiB, default 256KiB)")
	cmd.Flags().IntVar(&f.maxReceives, "max-receives", 0, "receives before a message is dead-lettered (0: no limit)")
	cmd.Flags().StringVar(&f.dlq, "dead-letter-queue", "", `queue that takes messages past --max-receives ("" for none)`)
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
}

// patch returns the changed settings as a spec patch (create uses every one).
func (f *queueFlags) patch(changed func(string) bool) (map[string]any, error) {
	p := map[string]any{}
	if changed("visibility-timeout") {
		s, err := wholeSeconds("visibility-timeout", f.visibility)
		if err != nil {
			return nil, err
		}
		p["visibilityTimeoutSeconds"] = s
	}
	if changed("retention") {
		h, err := wholeHours("retention", f.retention)
		if err != nil {
			return nil, err
		}
		p["retentionHours"] = h
	}
	if changed("max-message-size") {
		n, err := parseSize(f.size)
		if err != nil {
			return nil, err
		}
		p["maxMessageBytes"] = n
	}
	if changed("max-receives") {
		p["maxReceives"] = f.maxReceives
	}
	if changed("dead-letter-queue") {
		p["deadLetterQueue"] = nilIfEmpty(f.dlq)
	}
	return p, nil
}

func (a *app) queueCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindQueue, "queue", []string{"queues"}, "Queues: messages that wait for a worker (SQS-like)")
	var cf queueFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create a queue", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := cf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindQueue, args[0], p, cf.wait)
		}}
	cf.bind(create)
	var uf queueFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change a queue's settings (only the flags you pass; messages are kept)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := uf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindQueue, args[0], p, uf.wait)
		}}
	uf.bind(update)
	cmd.AddCommand(create, update, a.sendCmd(api.KindQueue, "send"), a.receiveCmd(), a.deleteMessageCmd(), a.peekCmd(), a.purgeCmd(api.KindQueue, "every message"))
	return cmd
}

// sendCmd sends to a queue or publishes to a topic.
func (a *app) sendCmd(kind api.Kind, use string) *cobra.Command {
	var attrs []string
	var dedup string
	cmd := &cobra.Command{Use: use + " NAME [BODY|-]", Short: "Send a message (the body from the argument or standard input)", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := parseAttrs(attrs)
			if err != nil {
				return err
			}
			body, err := a.readBody(args)
			if err != nil {
				return err
			}
			c, r, err := a.resolve(cmd.Context(), kind, args[0])
			if err != nil {
				return err
			}
			sent, err := c.SendMessage(cmd.Context(), r.ID, api.SendMessage{Body: body, Attributes: at, DeduplicationID: dedup})
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(sent)
			}
			if sent.Duplicate {
				fmt.Fprintf(a.io.Out, "Already sent as message %s (same deduplication ID); not sent again.\n", sent.ID)
				return nil
			}
			fmt.Fprintf(a.io.Out, "Message %s sent.\n", sent.ID)
			return nil
		}}
	cmd.Flags().StringArrayVar(&attrs, "attr", nil, "attribute NAME=VALUE (repeatable, up to 10)")
	if kind == api.KindQueue {
		cmd.Flags().StringVar(&dedup, "dedup-id", "", "deduplication ID: a repeat within 2 minutes isn't sent again")
	}
	return cmd
}

func (a *app) receiveCmd() *cobra.Command {
	var max int
	var wait time.Duration
	var del bool
	cmd := &cobra.Command{Use: "receive NAME", Short: "Receive messages: one JSON object per line, with its receipt", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secs, err := wholeSeconds("wait", wait)
			if err != nil {
				return err
			}
			c, r, err := a.resolve(cmd.Context(), api.KindQueue, args[0])
			if err != nil {
				return err
			}
			got, err := c.ReceiveMessages(cmd.Context(), r.ID, api.ReceiveMessages{Max: max, WaitSeconds: secs})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(a.io.Out)
			enc.SetEscapeHTML(false)
			var receipts []string
			for _, m := range got {
				if err := enc.Encode(m); err != nil {
					return err
				}
				receipts = append(receipts, m.Receipt)
			}
			if len(got) == 0 {
				fmt.Fprintln(a.io.Err, "No message.")
			}
			if del && len(receipts) > 0 {
				res, err := c.DeleteMessages(cmd.Context(), r.ID, receipts)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.io.Err, "Deleted %d.\n", res.Deleted)
			}
			return nil
		}}
	cmd.Flags().IntVar(&max, "max", 1, "how many messages at most (1–10)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait up to this long for a message (0–20s)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete what was received")
	return cmd
}

func (a *app) deleteMessageCmd() *cobra.Command {
	return &cobra.Command{Use: "delete-message NAME RECEIPT...", Short: "Delete received messages by receipt", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindQueue, args[0])
			if err != nil {
				return err
			}
			res, err := c.DeleteMessages(cmd.Context(), r.ID, args[1:])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(res)
			}
			fmt.Fprintf(a.io.Out, "Deleted %d.\n", res.Deleted)
			for _, f := range res.Failed {
				fmt.Fprintf(a.io.Err, "%s: %s\n", f.Receipt, f.Message)
			}
			if len(res.Failed) > 0 {
				return fmt.Errorf("%d receipt(s) deleted nothing", len(res.Failed))
			}
			return nil
		}}
}

func (a *app) peekCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "peek NAME", Short: "Show a queue's counts and oldest messages without receiving them", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindQueue, args[0])
			if err != nil {
				return err
			}
			list, err := c.Messages(cmd.Context(), r.ID, limit)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(list)
			}
			fmt.Fprintf(a.io.Out, "%d waiting, %d in flight, %d dead-lettered\n", list.Stats.Messages, list.Stats.InFlight, list.Stats.DeadLettered)
			rows := [][]string{}
			for _, m := range list.Items {
				rows = append(rows, []string{m.ID, m.SentAt.Local().Format("2006-01-02 15:04:05"), oneLine(m.Body, 60)})
			}
			a.table([]string{"ID", "SENT", "BODY"}, rows)
			return nil
		}}
	cmd.Flags().IntVar(&limit, "limit", api.PeekLimitDefault, "how many messages (1–50)")
	return cmd
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// purgeCmd empties a queue or a CDN's cache, after asking.
func (a *app) purgeCmd(kind api.Kind, what string) *cobra.Command {
	var yes, wait bool
	cmd := &cobra.Command{Use: "purge NAME", Short: "Delete " + what, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.confirm(fmt.Sprintf("Delete %s of %s %s?", what, kind, args[0]), yes); err != nil {
				return err
			}
			return a.action(cmd.Context(), kind, args[0], api.ActionPurge, wait)
		}}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until done")
	return cmd
}

// ─── topics (CLI-31) ───────────────────────────────────────────────────────

// parseSubscriptions reads --queue NAME and --webhook URL[,secret=NAME].
func parseSubscriptions(queues, hooks []string) ([]api.Subscription, error) {
	subs := []api.Subscription{}
	for _, q := range queues {
		subs = append(subs, api.Subscription{Queue: q})
	}
	for _, h := range hooks {
		url, opt, _ := strings.Cut(h, ",")
		sub := api.Subscription{URL: url}
		if opt != "" {
			secret, ok := strings.CutPrefix(opt, "secret=")
			if !ok {
				return nil, usagef("--webhook takes URL or URL,secret=NAME; got %q", h)
			}
			sub.Secret = secret
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

func (a *app) topicCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindTopic, "topic", []string{"topics"}, "Topics: messages delivered to every subscription (SNS-like)")
	flags := func(c *cobra.Command, retention *time.Duration, queues, hooks *[]string, wait *bool) {
		c.Flags().DurationVar(retention, "retention", 0, "how long published messages are kept for retries (1h–336h, default 24h)")
		c.Flags().StringArrayVar(queues, "queue", nil, "subscribe a queue (repeatable)")
		c.Flags().StringArrayVar(hooks, "webhook", nil, "subscribe a webhook: URL or URL,secret=NAME to sign (repeatable)")
		c.Flags().BoolVar(wait, "wait", false, "wait until ready")
	}
	build := func(changed func(string) bool, retention time.Duration, queues, hooks []string) (map[string]any, error) {
		p := map[string]any{}
		if changed("retention") {
			h, err := wholeHours("retention", retention)
			if err != nil {
				return nil, err
			}
			p["retentionHours"] = h
		}
		if changed("queue") || changed("webhook") {
			subs, err := parseSubscriptions(queues, hooks)
			if err != nil {
				return nil, err
			}
			p["subscriptions"] = subs
		}
		return p, nil
	}
	var cRet time.Duration
	var cQueues, cHooks []string
	var cWait bool
	create := &cobra.Command{Use: "create NAME", Short: "Create a topic", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := build(cmd.Flags().Changed, cRet, cQueues, cHooks)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindTopic, args[0], p, cWait)
		}}
	flags(create, &cRet, &cQueues, &cHooks, &cWait)
	var uRet time.Duration
	var uQueues, uHooks []string
	var uWait bool
	update := &cobra.Command{Use: "update NAME", Short: "Change a topic (--queue and --webhook together replace its subscriptions)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := build(cmd.Flags().Changed, uRet, uQueues, uHooks)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindTopic, args[0], p, uWait)
		}}
	flags(update, &uRet, &uQueues, &uHooks, &uWait)
	deliveries := &cobra.Command{Use: "deliveries NAME", Short: "Show each subscription's deliveries", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindTopic, args[0])
			if err != nil {
				return err
			}
			d, err := c.Deliveries(cmd.Context(), r.ID)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(d)
			}
			fmt.Fprintf(a.io.Out, "%d published\n", d.Published)
			rows := [][]string{}
			for _, s := range d.Items {
				rows = append(rows, []string{s.Target, strconv.FormatInt(s.Delivered, 10), strconv.FormatInt(s.Pending, 10), strconv.FormatInt(s.Failed, 10), s.LastError})
			}
			a.table([]string{"SUBSCRIPTION", "DELIVERED", "PENDING", "GAVE UP", "LAST ERROR"}, rows)
			return nil
		}}
	cmd.AddCommand(create, update, a.sendCmd(api.KindTopic, "publish"), deliveries)
	return cmd
}

// ─── search (CLI-32) ───────────────────────────────────────────────────────

func (a *app) searchCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindSearch, "search", nil, "Search services: OpenSearch with a user and password")
	var version string
	var memory int
	var wait bool
	var cp placementFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create a search service", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindSearch, args[0], api.SearchSpec{Version: version, MemoryMiB: memory, Network: cp.network, Subnet: cp.subnet, Placed: cp.placed()}, wait)
		}}
	create.Flags().StringVar(&version, "version", api.SearchVersion3, "OpenSearch major version: 3 or 2 (can't change later)")
	create.Flags().IntVar(&memory, "memory", 0, "memory in MiB (1024–16384 in steps of 256, default 2048; half is the heap)")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	cp.bind(create)
	var umem int
	var uwait bool
	var up placementFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change memory or network (restarts it; indexes are kept)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]any{}
			if cmd.Flags().Changed("memory") {
				p["memoryMiB"] = umem
			}
			up.patch(cmd.Flags().Changed, p)
			return a.update(cmd.Context(), api.KindSearch, args[0], p, uwait)
		}}
	update.Flags().IntVar(&umem, "memory", 0, "memory in MiB")
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	up.bind(update)
	cmd.AddCommand(create, update, a.restartCmd(api.KindSearch))
	return cmd
}

func (a *app) restartCmd(kind api.Kind) *cobra.Command {
	var wait bool
	c := &cobra.Command{Use: "restart NAME", Short: "Restart a " + string(kind), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.action(cmd.Context(), kind, args[0], api.ActionRestart, wait)
		}}
	c.Flags().BoolVar(&wait, "wait", false, "wait until done")
	return c
}

// ─── transcoders (CLI-33) ──────────────────────────────────────────────────

// parseFolder reads BUCKET/FOLDER/ (the folder may be empty).
func parseFolder(flag, s string) (api.BucketFolder, error) {
	bucket, prefix, _ := strings.Cut(s, "/")
	if bucket == "" {
		return api.BucketFolder{}, usagef("--%s needs BUCKET/FOLDER/, like uploads/videos/", flag)
	}
	return api.BucketFolder{Bucket: bucket, Prefix: prefix}, nil
}

type transcoderFlags struct {
	input, output       string
	presets             []string
	noWatch, watch      bool
	timeout             time.Duration
	concurrency, memory int
	wait                bool
	outputs             transcodeOutputFlags
}

func (f *transcoderFlags) bind(cmd *cobra.Command) {
	f.outputs.bind(cmd)
	cmd.Flags().StringVar(&f.input, "input", "", "where media arrives: BUCKET/FOLDER/")
	cmd.Flags().StringVar(&f.output, "output", "", "where results go: BUCKET/FOLDER/ (default folder transcoded/); {name} and {meta:TAG} take the file's name and the uploader's tags, like media/processed/{meta:video-id}/")
	cmd.Flags().StringArrayVar(&f.presets, "preset", nil, "preset (repeatable; on update the list replaces the old one, \"\" for none): "+strings.Join(api.Presets, ", "))
	cmd.Flags().BoolVar(&f.noWatch, "no-watch", false, "don't convert new files automatically (only by transcoder run)")
	cmd.Flags().BoolVar(&f.watch, "watch", false, "convert new files automatically (the default)")
	cmd.Flags().DurationVar(&f.timeout, "run-timeout", 0, "longest one file may take (1m–24h, default 1h)")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 0, "files converted at once (1–4, default 1)")
	cmd.Flags().IntVar(&f.memory, "memory", 0, "memory per file in MiB (256–8192, default 1024)")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *transcoderFlags) patch(changed func(string) bool, current *api.TranscoderSpec) (map[string]any, error) {
	p := map[string]any{}
	for _, fl := range []struct {
		name string
		val  string
	}{{"input", f.input}, {"output", f.output}} {
		if changed(fl.name) {
			folder, err := parseFolder(fl.name, fl.val)
			if err != nil {
				return nil, err
			}
			p[fl.name] = folder
		}
	}
	if changed("preset") {
		if len(f.presets) == 1 && f.presets[0] == "" {
			p["presets"] = nil
		} else {
			p["presets"] = f.presets
		}
	}
	if f.noWatch && f.watch {
		return nil, usagef("use either --watch or --no-watch")
	}
	if changed("no-watch") || changed("watch") {
		p["watch"] = !f.noWatch
	}
	if changed("run-timeout") {
		s, err := wholeSeconds("run-timeout", f.timeout)
		if err != nil {
			return nil, err
		}
		p["timeoutSeconds"] = s
	}
	if changed("concurrency") {
		p["concurrency"] = f.concurrency
	}
	if changed("memory") {
		p["memoryMiB"] = f.memory
	}
	if err := f.outputs.patch(p, changed, current); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *app) transcoderCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindTranscoder, "transcoder", []string{"transcoders"}, "Transcoders: media dropped in a bucket folder, converted with FFmpeg")
	var cf transcoderFlags
	create := &cobra.Command{Use: "create NAME --input BUCKET/FOLDER/ --output BUCKET/FOLDER/ (--preset P | --rendition R | --hls-rung R)...", Short: "Create a transcoder", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := cf.patch(cmd.Flags().Changed, nil)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindTranscoder, args[0], p, cf.wait)
		}}
	cf.bind(create)
	_ = create.MarkFlagRequired("input")
	_ = create.MarkFlagRequired("output")
	var uf transcoderFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change a transcoder (only the flags you pass; --preset, --rendition and --hls-rung replace their lists)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, r, err := a.resolve(cmd.Context(), api.KindTranscoder, args[0])
			if err != nil {
				return err
			}
			var current api.TranscoderSpec
			if err := json.Unmarshal(r.Spec, &current); err != nil {
				return err
			}
			p, err := uf.patch(cmd.Flags().Changed, &current)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindTranscoder, args[0], p, uf.wait)
		}}
	uf.bind(update)
	var wait bool
	run := &cobra.Command{Use: "run NAME KEY", Short: "Convert one file of the input folder now", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindTranscoder, args[0])
			if err != nil {
				return err
			}
			run, err := c.Transcode(cmd.Context(), r.ID, args[1])
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
			return a.waitRun(cmd.Context(), c, r, run.ID)
		}}
	run.Flags().BoolVar(&wait, "wait", false, "wait for the run to end")
	cmd.AddCommand(create, update, run, a.runsCmd(api.KindTranscoder))
	return cmd
}

// ─── CDN (CLI-34) ──────────────────────────────────────────────────────────

type cdnFlags struct {
	hostname, origin, tls, zone string
	ttl                         time.Duration
	size                        int
	wait                        bool
}

func (f *cdnFlags) bind(cmd *cobra.Command, create bool) {
	if create {
		cmd.Flags().StringVar(&f.hostname, "hostname", "", "hostname to serve, like cdn.example.com (can't change later)")
	}
	cmd.Flags().StringVar(&f.origin, "origin", "", "what to serve: app/NAME[:PORT] or bucket/NAME")
	cmd.Flags().DurationVar(&f.ttl, "default-ttl", 0, "cache time when the origin doesn't say (0s–8760h, default 1h)")
	cmd.Flags().IntVar(&f.size, "max-size", 0, "cache size in MiB (64–102400, default 1024)")
	cmd.Flags().StringVar(&f.tls, "tls", "", "auto, internal or off (as for domains)")
	cmd.Flags().StringVar(&f.zone, "zone", "", "get the certificate through this DNS zone")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
}

func (f *cdnFlags) patch(changed func(string) bool) (map[string]any, error) {
	p := map[string]any{}
	if changed("hostname") {
		p["hostname"] = f.hostname
	}
	if changed("origin") {
		origin, port, hasPort := strings.Cut(f.origin, ":")
		p["origin"] = origin
		p["originPort"] = nil
		if hasPort {
			n, err := strconv.Atoi(port)
			if err != nil {
				return nil, usagef("--origin takes app/NAME:PORT with a number; got %q", f.origin)
			}
			p["originPort"] = n
		}
	}
	if changed("default-ttl") {
		s, err := wholeSeconds("default-ttl", f.ttl)
		if err != nil {
			return nil, err
		}
		p["defaultTtlSeconds"] = s
	}
	if changed("max-size") {
		p["maxSizeMiB"] = f.size
	}
	if changed("tls") {
		p["tls"] = f.tls
	}
	if changed("zone") {
		p["zone"] = nilIfEmpty(f.zone)
	}
	return p, nil
}

func (a *app) cdnCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindCDN, "cdn", []string{"cdns"}, "CDN: a cache with its own hostname in front of an app or a bucket")
	var cf cdnFlags
	create := &cobra.Command{Use: "create NAME --hostname HOST --origin app/NAME|bucket/NAME", Short: "Create a CDN", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := cf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			if strings.HasPrefix(cf.origin, "bucket/") {
				fmt.Fprintln(a.io.Err, "Note: the bucket's files become readable by their key, as the CDN serves them publicly.")
			}
			return a.create(cmd.Context(), api.KindCDN, args[0], p, cf.wait)
		}}
	cf.bind(create, true)
	_ = create.MarkFlagRequired("hostname")
	_ = create.MarkFlagRequired("origin")
	var uf cdnFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change a CDN (only the flags you pass)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := uf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindCDN, args[0], p, uf.wait)
		}}
	uf.bind(update, false)
	cmd.AddCommand(create, update, a.purgeCmd(api.KindCDN, "everything cached"), a.restartCmd(api.KindCDN))
	return cmd
}

// ─── email (CLI-35) ────────────────────────────────────────────────────────

type emailFlags struct {
	relayHost, relayTLS, relayCreds, domain string
	relayPort, keep                         int
	capture, wait                           bool
	place                                   placementFlags
}

func (f *emailFlags) bind(cmd *cobra.Command, update bool) {
	cmd.Flags().StringVar(&f.relayHost, "relay-host", "", "relay through this SMTP host (your provider's)")
	cmd.Flags().IntVar(&f.relayPort, "relay-port", 0, "the relay's port (default 587)")
	cmd.Flags().StringVar(&f.relayTLS, "relay-tls", "", "starttls (default), tls or none")
	cmd.Flags().StringVar(&f.relayCreds, "relay-credentials", "", `secret holding {"username": "…", "password": "…"} for the relay`)
	cmd.Flags().StringVar(&f.domain, "domain", "", "the API sends only from this domain")
	cmd.Flags().IntVar(&f.keep, "keep", 0, "messages kept to look at (10–10000, default 500)")
	if update {
		cmd.Flags().BoolVar(&f.capture, "capture", false, "stop relaying: keep messages only")
	}
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
	f.place.bind(cmd)
}

func (f *emailFlags) patch(changed func(string) bool) (map[string]any, error) {
	p := map[string]any{}
	relay := changed("relay-host") || changed("relay-port") || changed("relay-tls") || changed("relay-credentials")
	switch {
	case f.capture && relay:
		return nil, usagef("--capture and the --relay-* flags exclude each other")
	case f.capture:
		p["mode"], p["relay"] = api.EmailCapture, nil
	case relay:
		r := map[string]any{"host": f.relayHost, "credentialsSecret": f.relayCreds}
		if f.relayPort != 0 {
			r["port"] = f.relayPort
		}
		if f.relayTLS != "" {
			r["tls"] = f.relayTLS
		}
		p["mode"], p["relay"] = api.EmailRelaying, r
	}
	if changed("domain") {
		p["domain"] = nilIfEmpty(f.domain)
	}
	if changed("keep") {
		p["keepMessages"] = f.keep
	}
	f.place.patch(changed, p)
	return p, nil
}

func (a *app) emailCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindEmail, "email", []string{"emails"}, "Email services: SMTP for apps; messages kept to look at, or relayed through your provider")
	var cf emailFlags
	create := &cobra.Command{Use: "create NAME", Short: "Create an email service (catches messages unless given a relay)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := cf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			return a.create(cmd.Context(), api.KindEmail, args[0], p, cf.wait)
		}}
	cf.bind(create, false)
	var uf emailFlags
	update := &cobra.Command{Use: "update NAME", Short: "Change an email service (the --relay-* flags together replace the relay)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := uf.patch(cmd.Flags().Changed)
			if err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindEmail, args[0], p, uf.wait)
		}}
	uf.bind(update, true)
	cmd.AddCommand(create, update, a.emailSendCmd(), a.emailMessagesCmd(), a.emailMessageCmd(), a.restartCmd(api.KindEmail))
	return cmd
}

func (a *app) emailSendCmd() *cobra.Command {
	var m api.SendEmail
	var textFile, htmlFile string
	cmd := &cobra.Command{Use: "send NAME --from A --to B --subject S (--text T | --text-file F) [--html-file F]", Short: "Send a message", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, f := range []struct {
				path string
				dst  *string
			}{{textFile, &m.Text}, {htmlFile, &m.HTML}} {
				if f.path == "" {
					continue
				}
				b, err := os.ReadFile(f.path)
				if err != nil {
					return usagef("%v", err)
				}
				*f.dst = string(b)
			}
			c, r, err := a.resolve(cmd.Context(), api.KindEmail, args[0])
			if err != nil {
				return err
			}
			sent, err := c.SendEmail(cmd.Context(), r.ID, m)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(sent)
			}
			fmt.Fprintf(a.io.Out, "Message %s sent.\n", sent.ID)
			return nil
		}}
	cmd.Flags().StringVar(&m.From, "from", "", "sender address")
	cmd.Flags().StringArrayVar(&m.To, "to", nil, "recipient (repeatable)")
	cmd.Flags().StringArrayVar(&m.Cc, "cc", nil, "copy recipient (repeatable)")
	cmd.Flags().StringArrayVar(&m.Bcc, "bcc", nil, "hidden recipient (repeatable)")
	cmd.Flags().StringVar(&m.ReplyTo, "reply-to", "", "reply-to address")
	cmd.Flags().StringVar(&m.Subject, "subject", "", "subject")
	cmd.Flags().StringVar(&m.Text, "text", "", "plain-text body")
	cmd.Flags().StringVar(&textFile, "text-file", "", "plain-text body from a file")
	cmd.Flags().StringVar(&htmlFile, "html-file", "", "HTML body from a file")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("subject")
	return cmd
}

func (a *app) emailMessagesCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "messages NAME", Short: "List kept messages, newest first", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindEmail, args[0])
			if err != nil {
				return err
			}
			list, err := c.Emails(cmd.Context(), r.ID, limit)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(list)
			}
			rows := [][]string{}
			for _, e := range list.Items {
				rows = append(rows, []string{e.ID, e.SentAt.Local().Format("2006-01-02 15:04:05"), e.From, strings.Join(e.To, ", "), oneLine(e.Subject, 50), e.Relay})
			}
			a.table([]string{"ID", "SENT", "FROM", "TO", "SUBJECT", "RELAY"}, rows)
			return nil
		}}
	cmd.Flags().IntVar(&limit, "limit", api.EmailListDefault, "how many (1–200)")
	return cmd
}

func (a *app) emailMessageCmd() *cobra.Command {
	return &cobra.Command{Use: "message NAME ID", Short: "Show one kept message", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindEmail, args[0])
			if err != nil {
				return err
			}
			e, err := c.Email(cmd.Context(), r.ID, args[1])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(e)
			}
			fmt.Fprintf(a.io.Out, "From: %s\nTo: %s\n", e.From, strings.Join(e.To, ", "))
			if len(e.Cc) > 0 {
				fmt.Fprintf(a.io.Out, "Cc: %s\n", strings.Join(e.Cc, ", "))
			}
			fmt.Fprintf(a.io.Out, "Subject: %s\nSent: %s\nRelay: %s\n\n%s\n", e.Subject, e.SentAt.Local().Format(time.RFC1123), e.Relay, e.Text)
			if e.Text == "" && e.HTML != "" {
				fmt.Fprintln(a.io.Out, "(HTML only; use -o json to see it)")
			}
			return nil
		}}
}

// connectionString is how a client reaches a database of an engine, without its password (CLI-29).
func connectionString(r api.Resource) string {
	host, _ := r.Outputs["host"].(string)
	port := fieldText(r.Outputs["port"])
	user, _ := r.Outputs["username"].(string)
	db, _ := r.Outputs["database"].(string)
	if host == "" {
		host, port = fieldText(r.Outputs["internalHost"]), fieldText(r.Outputs["internalPort"])
	}
	switch r.Outputs["engine"] {
	case api.EngineMySQL:
		return fmt.Sprintf("mysql://%s:<password>@%s:%s/%s", user, host, port, db)
	case api.EngineMongoDB:
		return fmt.Sprintf("mongodb://%s:<password>@%s:%s/%s?authSource=admin", user, host, port, db)
	case api.EngineClickHouse:
		return fmt.Sprintf("http://%s:<password>@%s:%s/?database=%s", user, host, port, db)
	}
	return fmt.Sprintf("postgres://%s:<password>@%s:%s/%s", user, host, port, db)
}
