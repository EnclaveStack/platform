package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.6 resources (L2-003 TF-27 – TF-33): queue, topic, search, transcoder, cdn and email. They use the
// shared kind implementation; unset optional settings stay null in state while the server applies its
// defaults (defaultKeep).

func optInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Optional: true, Description: desc}
}

func optString(desc string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Description: desc}
}

func intOrNil(v types.Int64) any {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueInt64()
}

func strOrNil(v types.String) any {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	return v.ValueString()
}

// brokerOutputs are the connection attributes queues and topics share.
func brokerAttrs(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	attrs["broker_url"] = stableString("The broker on this machine, nats://127.0.0.1:<port>.")
	attrs["internal_broker_url"] = stableString("The broker inside EnclaveStack's networks, nats://es-system-broker:4222.")
	attrs["subject"] = stableString("The subject apps publish to.")
	attrs["username"] = stableString("The broker user apps sign in as.")
	attrs["password_secret"] = stableString("Name of the managed secret holding the user's password.")
	return attrs
}

// ─── enclave_queue (TF-28) ───────────────────────────────────────────────

type queueModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Project           types.String   `tfsdk:"project"`
	VisibilityTimeout types.Int64    `tfsdk:"visibility_timeout_seconds"`
	RetentionHours    types.Int64    `tfsdk:"retention_hours"`
	MaxMessageBytes   types.Int64    `tfsdk:"max_message_bytes"`
	MaxReceives       types.Int64    `tfsdk:"max_receives"`
	DeadLetterQueue   types.String   `tfsdk:"dead_letter_queue"`
	BrokerURL         types.String   `tfsdk:"broker_url"`
	InternalURL       types.String   `tfsdk:"internal_broker_url"`
	Subject           types.String   `tfsdk:"subject"`
	Username          types.String   `tfsdk:"username"`
	PasswordSecret    types.String   `tfsdk:"password_secret"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (m *queueModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *queueModel) limits() timeouts.Value            { return m.Timeouts }

func (m *queueModel) patch() map[string]any {
	return map[string]any{"visibilityTimeoutSeconds": intOrNil(m.VisibilityTimeout), "retentionHours": intOrNil(m.RetentionHours),
		"maxMessageBytes": intOrNil(m.MaxMessageBytes), "maxReceives": intOrNil(m.MaxReceives), "deadLetterQueue": strOrNil(m.DeadLetterQueue)}
}

func (m *queueModel) spec() any { return m.patch() }

func (m *queueModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.QueueSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.VisibilityTimeout = defaultKeep(m.VisibilityTimeout, s.VisibilityTimeoutSeconds, api.QueueVisibilityDefault)
	m.RetentionHours = defaultKeep(m.RetentionHours, s.RetentionHours, api.QueueRetentionDefault)
	m.MaxMessageBytes = defaultKeep(m.MaxMessageBytes, s.MaxMessageBytes, api.MessageBytesDefault)
	m.MaxReceives = defaultKeep(m.MaxReceives, s.MaxReceives, 0)
	m.DeadLetterQueue = strOrNull(s.DeadLetterQueue)
	m.BrokerURL, m.InternalURL, m.Subject = outString(r, "url"), outString(r, "internalUrl"), outString(r, "subject")
	m.Username, m.PasswordSecret = outString(r, "username"), outString(r, "passwordSecret")
}

func newQueue() resource.Resource {
	return &kindResource[queueModel, *queueModel]{base: base{kind: api.KindQueue}, suffix: "_queue", label: "queue",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A queue of messages on the broker (SQS-like). Every setting changes in place; messages are kept.",
				Attributes: brokerAttrs(map[string]schema.Attribute{
					"id":                         idAttr(),
					"name":                       nameAttr("queue"),
					"project":                    projectAttr(),
					"visibility_timeout_seconds": optInt("How long a received message stays hidden, 1–43200 (default 30)."),
					"retention_hours":            optInt("How long a message is kept, 1–336 (default 96)."),
					"max_message_bytes":          optInt("Largest body, 1024–1048576 (default 262144)."),
					"max_receives":               optInt("Receives before a message is dead-lettered, 0–1000 (default 0: no limit)."),
					"dead_letter_queue":          optString("Queue that takes messages past max_receives."),
					"timeouts":                   timeoutsAttr(ctx),
				}),
			}
		}}
}

// ─── enclave_topic (TF-29) ───────────────────────────────────────────────

type subscriptionModel struct {
	Queue  types.String `tfsdk:"queue"`
	URL    types.String `tfsdk:"url"`
	Secret types.String `tfsdk:"secret"`
}

type topicModel struct {
	ID             types.String        `tfsdk:"id"`
	Name           types.String        `tfsdk:"name"`
	Project        types.String        `tfsdk:"project"`
	RetentionHours types.Int64         `tfsdk:"retention_hours"`
	Subscriptions  []subscriptionModel `tfsdk:"subscriptions"`
	BrokerURL      types.String        `tfsdk:"broker_url"`
	InternalURL    types.String        `tfsdk:"internal_broker_url"`
	Subject        types.String        `tfsdk:"subject"`
	Username       types.String        `tfsdk:"username"`
	PasswordSecret types.String        `tfsdk:"password_secret"`
	Timeouts       timeouts.Value      `tfsdk:"timeouts"`
}

func (m *topicModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *topicModel) limits() timeouts.Value            { return m.Timeouts }

func (m *topicModel) patch() map[string]any {
	subs := []api.Subscription{}
	for _, s := range m.Subscriptions {
		subs = append(subs, api.Subscription{Queue: s.Queue.ValueString(), URL: s.URL.ValueString(), Secret: s.Secret.ValueString()})
	}
	return map[string]any{"retentionHours": intOrNil(m.RetentionHours), "subscriptions": subs}
}

func (m *topicModel) spec() any { return m.patch() }

func (m *topicModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.TopicSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.RetentionHours = defaultKeep(m.RetentionHours, s.RetentionHours, api.TopicRetentionDefault)
	if m.Subscriptions != nil || len(s.Subscriptions) > 0 {
		m.Subscriptions = []subscriptionModel{}
		for _, sub := range s.Subscriptions {
			m.Subscriptions = append(m.Subscriptions, subscriptionModel{Queue: strOrNull(sub.Queue), URL: strOrNull(sub.URL), Secret: strOrNull(sub.Secret)})
		}
	}
	m.BrokerURL, m.InternalURL, m.Subject = outString(r, "url"), outString(r, "internalUrl"), outString(r, "subject")
	m.Username, m.PasswordSecret = outString(r, "username"), outString(r, "passwordSecret")
}

func newTopic() resource.Resource {
	return &kindResource[topicModel, *topicModel]{base: base{kind: api.KindTopic}, suffix: "_topic", label: "topic",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A topic that delivers each message to every subscription: queues and webhooks (SNS-like). Changes apply in place.",
				Attributes: brokerAttrs(map[string]schema.Attribute{
					"id":              idAttr(),
					"name":            nameAttr("topic"),
					"project":         projectAttr(),
					"retention_hours": optInt("How long published messages are kept for retries, 1–336 (default 24)."),
					"subscriptions": schema.ListNestedAttribute{Optional: true, Description: "Up to 20 subscriptions: exactly one of queue or url each.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"queue":  optString("A queue's name."),
							"url":    optString("A webhook: https://, or http:// to localhost."),
							"secret": optString("A secret whose value signs webhook bodies (X-Enclave-Signature)."),
						}}},
					"timeouts": timeoutsAttr(ctx),
				}),
			}
		}}
}

// ─── enclave_search (TF-30) ──────────────────────────────────────────────

type searchModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Version        types.String   `tfsdk:"version"`
	MemoryMiB      types.Int64    `tfsdk:"memory_mib"`
	Network        types.String   `tfsdk:"network"`
	Subnet         types.String   `tfsdk:"subnet"`
	URL            types.String   `tfsdk:"url"`
	InternalURL    types.String   `tfsdk:"internal_url"`
	Username       types.String   `tfsdk:"username"`
	PasswordSecret types.String   `tfsdk:"password_secret"`
	PrivateDNS     types.String   `tfsdk:"private_dns"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (m *searchModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *searchModel) limits() timeouts.Value            { return m.Timeouts }

func (m *searchModel) spec() any {
	return api.SearchSpec{Version: m.Version.ValueString(), MemoryMiB: int(m.MemoryMiB.ValueInt64()), Network: m.Network.ValueString(), Subnet: m.Subnet.ValueString(), Placed: m.placed()}
}

func (m *searchModel) patch() map[string]any {
	return map[string]any{"memoryMiB": intOrNil(m.MemoryMiB), "network": strOrNil(m.Network), "subnet": strOrNil(m.Subnet)}
}

func (m *searchModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.SearchSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Version = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Version)
	m.MemoryMiB = defaultKeep(m.MemoryMiB, s.MemoryMiB, api.SearchMemoryDefaultMiB)
	m.Network, m.Subnet = strOrNull(s.Network), strOrNull(s.Subnet)
	m.URL, m.InternalURL = outString(r, "url"), outString(r, "internalUrl")
	m.Username, m.PasswordSecret, m.PrivateDNS = outString(r, "username"), outString(r, "passwordSecret"), outString(r, "privateDns")
}

func newSearch() resource.Resource {
	return &kindResource[searchModel, *searchModel]{base: base{kind: api.KindSearch}, suffix: "_search", label: "search service",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "An OpenSearch service with an admin user (password in password_secret). Changing memory restarts it; indexes are kept.",
				Attributes: map[string]schema.Attribute{
					"id":             idAttr(),
					"name":           nameAttr("search service"),
					"project":        projectAttr(),
					"machine":        machineAttr(),
					"machine_labels": machineLabelsAttr(),
					"version": schema.StringAttribute{Optional: true, Computed: true, Description: "OpenSearch major version, 3 (default) or 2. Changing it replaces the service.",
						Validators:    []validator.String{stringvalidator.OneOf(api.SearchVersions...)},
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}},
					"memory_mib":      optInt("Memory in MiB, 1024–16384 in steps of 256 (default 2048); half is the heap."),
					"network":         placementNetworkAttr(),
					"subnet":          placementSubnetAttr(),
					"url":             placedString("REST API on this machine (none in a network without internet)."),
					"internal_url":    stableString("REST API inside its network."),
					"username":        stableString("The admin user."),
					"password_secret": stableString("Name of the managed secret holding the admin password."),
					"private_dns":     stableString("Name it answers to inside its network: <name>.search.internal."),
					"timeouts":        timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_transcoder (TF-31) ──────────────────────────────────────────

type folderModel struct {
	Bucket types.String `tfsdk:"bucket"`
	Prefix types.String `tfsdk:"prefix"`
}

type transcoderModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	Project        types.String   `tfsdk:"project"`
	Input          *folderModel   `tfsdk:"input"`
	Output         *folderModel   `tfsdk:"output"`
	Presets        []types.String `tfsdk:"presets"`
	Watch          types.Bool     `tfsdk:"watch"`
	TimeoutSeconds types.Int64    `tfsdk:"timeout_seconds"`
	Concurrency    types.Int64    `tfsdk:"concurrency"`
	MemoryMiB      types.Int64    `tfsdk:"memory_mib"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (m *transcoderModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *transcoderModel) limits() timeouts.Value            { return m.Timeouts }

func folderOf(f *folderModel) api.BucketFolder {
	if f == nil {
		return api.BucketFolder{}
	}
	return api.BucketFolder{Bucket: f.Bucket.ValueString(), Prefix: f.Prefix.ValueString()}
}

func (m *transcoderModel) patch() map[string]any {
	p := map[string]any{"input": folderOf(m.Input), "output": folderOf(m.Output), "presets": toStrings(m.Presets),
		"timeoutSeconds": intOrNil(m.TimeoutSeconds), "concurrency": intOrNil(m.Concurrency), "memoryMiB": intOrNil(m.MemoryMiB), "watch": nil}
	if !m.Watch.IsNull() && !m.Watch.IsUnknown() {
		p["watch"] = m.Watch.ValueBool()
	}
	return p
}

func (m *transcoderModel) spec() any { return m.patch() }

// folderFill keeps a prefix left unset in configuration null when the server used its default (an
// import has no configuration: the prefix is set unless it is the default).
func folderFill(prior *folderModel, f api.BucketFolder, def string) *folderModel {
	out := &folderModel{Bucket: types.StringValue(f.Bucket), Prefix: types.StringValue(f.Prefix)}
	if (prior == nil || prior.Prefix.IsNull()) && f.Prefix == def {
		out.Prefix = types.StringNull()
	}
	return out
}

func (m *transcoderModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.TranscoderSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.Input, m.Output = folderFill(m.Input, s.Input, ""), folderFill(m.Output, s.Output, api.TranscodeOutputDefault)
	m.Presets = tfStrings(s.Presets)
	if !m.Watch.IsNull() || s.Watch != nil {
		m.Watch = types.BoolValue(s.Watching())
	}
	m.TimeoutSeconds = defaultKeep(m.TimeoutSeconds, s.TimeoutSeconds, api.TranscodeTimeoutDefault)
	m.Concurrency = defaultKeep(m.Concurrency, s.Concurrency, api.ConcurrencyDefault)
	m.MemoryMiB = defaultKeep(m.MemoryMiB, s.MemoryMiB, api.TranscodeMemoryDefault)
}

// prefixPattern is a folder as the server stores it: "" or "a/b/" (normalised configuration never
// shows a change after apply).
var prefixPattern = regexp.MustCompile(`^$|^[^/].*/$`)

func folderAttr(desc string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Required: true, Description: desc, Attributes: map[string]schema.Attribute{
		"bucket": schema.StringAttribute{Required: true, Description: "A bucket's name."},
		"prefix": schema.StringAttribute{Optional: true, Description: `A folder ending in "/", like videos/ ("" for the whole bucket).`,
			Validators: []validator.String{stringvalidator.RegexMatches(prefixPattern, `must be "" or end with "/" and not start with one`)}},
	}}
}

func newTranscoder() resource.Resource {
	return &kindResource[transcoderModel, *transcoderModel]{base: base{kind: api.KindTranscoder}, suffix: "_transcoder", label: "transcoder",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "Converts media written to a bucket folder with FFmpeg presets. Changes apply in place; runs are not part of state.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("transcoder"),
					"project": projectAttr(),
					"input":   folderAttr("Where media arrives."),
					"output":  folderAttr(`Where results go (default prefix "transcoded/").`),
					"presets": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Presets: " + fmt.Sprint(api.Presets),
						Validators: []validator.List{}},
					"watch":           schema.BoolAttribute{Optional: true, Description: "Convert new files automatically (default true)."},
					"timeout_seconds": optInt("Longest one file may take, 60–86400 (default 3600)."),
					"concurrency":     optInt("Files converted at once, 1–4 (default 1)."),
					"memory_mib":      optInt("Memory per file in MiB, 256–8192 (default 1024)."),
					"timeouts":        timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_cdn (TF-32) ─────────────────────────────────────────────────

type cdnModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	Project          types.String   `tfsdk:"project"`
	Hostname         types.String   `tfsdk:"hostname"`
	Origin           types.String   `tfsdk:"origin"`
	OriginPort       types.Int64    `tfsdk:"origin_port"`
	DefaultTTL       types.Int64    `tfsdk:"default_ttl_seconds"`
	MaxSizeMiB       types.Int64    `tfsdk:"max_size_mib"`
	TLS              types.String   `tfsdk:"tls"`
	Zone             types.String   `tfsdk:"zone"`
	URL              types.String   `tfsdk:"url"`
	CertificateState types.String   `tfsdk:"certificate_state"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (m *cdnModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *cdnModel) limits() timeouts.Value            { return m.Timeouts }

func (m *cdnModel) patch() map[string]any {
	return map[string]any{"origin": m.Origin.ValueString(), "originPort": intOrNil(m.OriginPort), "defaultTtlSeconds": intOrNil(m.DefaultTTL),
		"maxSizeMiB": intOrNil(m.MaxSizeMiB), "tls": strOrNil(m.TLS), "zone": strOrNil(m.Zone)}
}

func (m *cdnModel) spec() any {
	p := m.patch()
	p["hostname"] = m.Hostname.ValueString()
	return p
}

func (m *cdnModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.CDNSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Hostname, m.Origin = types.StringValue(r.ID), types.StringValue(r.Name), foldKeep(m.Hostname, s.Hostname), types.StringValue(s.Origin)
	m.OriginPort = defaultKeep(m.OriginPort, s.OriginPort, 0)
	m.DefaultTTL = defaultKeep(m.DefaultTTL, s.TTL(), api.CDNTTLDefault)
	m.MaxSizeMiB = defaultKeep(m.MaxSizeMiB, s.MaxSizeMiB, api.CDNSizeDefaultMiB)
	m.TLS, m.Zone = types.StringValue(s.TLS), strOrNull(s.Zone)
	m.URL, m.CertificateState = outString(r, "url"), outString(r, "certificateState")
}

func newCDN() resource.Resource {
	return &kindResource[cdnModel, *cdnModel]{base: base{kind: api.KindCDN}, suffix: "_cdn", label: "CDN",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A cache with its own hostname in front of an app or a bucket. A bucket behind a CDN is readable by key.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("CDN"),
					"project": projectAttr(),
					"hostname": schema.StringAttribute{Required: true, Description: "Hostname to serve. Changing it replaces the CDN.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"origin":              schema.StringAttribute{Required: true, Description: "app/<name> or bucket/<name>."},
					"origin_port":         optInt("The app's port to fetch from (default the app's own)."),
					"default_ttl_seconds": optInt("Cache time when the origin doesn't say, 0–31536000 (default 3600)."),
					"max_size_mib":        optInt("Cache size in MiB, 64–102400 (default 1024)."),
					"tls": schema.StringAttribute{Optional: true, Computed: true, Description: "auto, internal or off, as for domains (default auto; internal for .localhost).",
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
					"zone":              optString("A DNS zone to get the certificate through (DNS-01)."),
					"url":               stableString("The CDN's URL."),
					"certificate_state": schema.StringAttribute{Computed: true, Description: "issued, pending or none."},
					"timeouts":          timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_email (TF-32) ───────────────────────────────────────────────

type relayModel struct {
	Host              types.String `tfsdk:"host"`
	Port              types.Int64  `tfsdk:"port"`
	TLS               types.String `tfsdk:"tls"`
	CredentialsSecret types.String `tfsdk:"credentials_secret"`
}

type emailModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Relay            *relayModel    `tfsdk:"relay"`
	Domain           types.String   `tfsdk:"domain"`
	KeepMessages     types.Int64    `tfsdk:"keep_messages"`
	Network          types.String   `tfsdk:"network"`
	Subnet           types.String   `tfsdk:"subnet"`
	Mode             types.String   `tfsdk:"mode"`
	SMTPHost         types.String   `tfsdk:"smtp_host"`
	SMTPPort         types.Int64    `tfsdk:"smtp_port"`
	InternalSMTPHost types.String   `tfsdk:"internal_smtp_host"`
	Username         types.String   `tfsdk:"username"`
	PasswordSecret   types.String   `tfsdk:"password_secret"`
	WebURL           types.String   `tfsdk:"web_url"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (m *emailModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *emailModel) limits() timeouts.Value            { return m.Timeouts }

func (m *emailModel) patch() map[string]any {
	p := map[string]any{"mode": api.EmailCapture, "relay": nil, "domain": strOrNil(m.Domain), "keepMessages": intOrNil(m.KeepMessages),
		"network": strOrNil(m.Network), "subnet": strOrNil(m.Subnet)}
	if r := m.Relay; r != nil {
		p["mode"] = api.EmailRelaying
		p["relay"] = map[string]any{"host": r.Host.ValueString(), "port": intOrNil(r.Port), "tls": strOrNil(r.TLS), "credentialsSecret": r.CredentialsSecret.ValueString()}
	}
	return p
}

func (m *emailModel) spec() any {
	p := m.patch()
	maps.Copy(p, m.placedFields())
	return p
}

func (m *emailModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.EmailSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Mode = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Mode)
	if s.Relay == nil {
		m.Relay = nil
	} else {
		prior := relayModel{Port: types.Int64Null(), TLS: types.StringNull()}
		if m.Relay != nil {
			prior = *m.Relay
		}
		tls := types.StringValue(s.Relay.TLS)
		if prior.TLS.IsNull() && s.Relay.TLS == api.RelaySTARTTLS {
			tls = types.StringNull()
		}
		m.Relay = &relayModel{Host: types.StringValue(s.Relay.Host), Port: defaultKeep(prior.Port, s.Relay.Port, api.RelayPortDefault), TLS: tls,
			CredentialsSecret: types.StringValue(s.Relay.CredentialsSecret)}
	}
	m.Domain = foldKeepOrNull(m.Domain, s.Domain)
	m.KeepMessages = defaultKeep(m.KeepMessages, s.KeepMessages, api.KeepMessagesDefault)
	m.Network, m.Subnet = strOrNull(s.Network), strOrNull(s.Subnet)
	m.SMTPHost, m.SMTPPort, m.InternalSMTPHost = outString(r, "host"), outInt(r, "port"), outString(r, "internalHost")
	m.Username, m.PasswordSecret, m.WebURL = outString(r, "username"), outString(r, "passwordSecret"), outString(r, "webUrl")
}

func foldKeepOrNull(prior types.String, v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return foldKeep(prior, v)
}

func newEmail() resource.Resource {
	return &kindResource[emailModel, *emailModel]{base: base{kind: api.KindEmail}, suffix: "_email", label: "email service",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "An SMTP service for apps. Without relay it keeps every message to look at; with relay it also delivers through your provider.",
				Attributes: map[string]schema.Attribute{
					"id":             idAttr(),
					"name":           nameAttr("email service"),
					"project":        projectAttr(),
					"machine":        machineAttr(),
					"machine_labels": machineLabelsAttr(),
					"relay": schema.SingleNestedAttribute{Optional: true, Description: "Deliver through this SMTP relay (your provider).",
						Attributes: map[string]schema.Attribute{
							"host":               schema.StringAttribute{Required: true, Description: "The relay's host."},
							"port":               optInt("The relay's port (default 587)."),
							"tls":                optString("starttls (default), tls or none."),
							"credentials_secret": schema.StringAttribute{Required: true, Description: `A secret holding {"username": "…", "password": "…"}.`},
						}},
					"domain":             optString("The API sends only from this domain."),
					"keep_messages":      optInt("Messages kept to look at, 10–10000 (default 500)."),
					"network":            placementNetworkAttr(),
					"subnet":             placementSubnetAttr(),
					"mode":               schema.StringAttribute{Computed: true, Description: "capture or relay."},
					"smtp_host":          placedString("SMTP host on this machine."),
					"smtp_port":          placedInt("SMTP port on this machine."),
					"internal_smtp_host": stableString("SMTP host inside its network (port 1025)."),
					"username":           stableString("SMTP user."),
					"password_secret":    stableString("Name of the managed secret holding the SMTP password."),
					"web_url":            placedString("The message viewer on this machine (same user and password)."),
					"timeouts":           timeoutsAttr(ctx),
				},
			}
		}}
}
