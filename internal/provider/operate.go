package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// v0.7 resources (L2-003 TF-37 – TF-40): dashboard, alert, channel, log export and secret store, on the
// shared implementation of networking.go; and the enclave_metrics data source.

// ─── enclave_dashboard (TF-37) ───────────────────────────────────────────

type widgetModel struct {
	Title     types.String   `tfsdk:"title"`
	Metric    types.String   `tfsdk:"metric"`
	Resources []types.String `tfsdk:"resources"`
}

type dashboardModel struct {
	ID       types.String   `tfsdk:"id"`
	Name     types.String   `tfsdk:"name"`
	Project  types.String   `tfsdk:"project"`
	Widgets  []widgetModel  `tfsdk:"widgets"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (m *dashboardModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *dashboardModel) limits() timeouts.Value            { return m.Timeouts }

func (m *dashboardModel) widgets() []api.Widget {
	out := make([]api.Widget, 0, len(m.Widgets))
	for _, w := range m.Widgets {
		out = append(out, api.Widget{Title: w.Title.ValueString(), Metric: w.Metric.ValueString(), Resources: toStrings(w.Resources)})
	}
	return out
}

func (m *dashboardModel) spec() any             { return api.DashboardSpec{Widgets: m.widgets()} }
func (m *dashboardModel) patch() map[string]any { return map[string]any{"widgets": m.widgets()} }

func (m *dashboardModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.DashboardSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.Widgets = nil
	for _, w := range s.Widgets {
		m.Widgets = append(m.Widgets, widgetModel{Title: types.StringValue(w.Title), Metric: types.StringValue(w.Metric), Resources: tfStrings(w.Resources)})
	}
}

func newDashboard() resource.Resource {
	return &kindResource[dashboardModel, *dashboardModel]{base: base{kind: api.KindDashboard}, suffix: "_dashboard", label: "dashboard",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A named page of graphs in the console's Monitoring section.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("dashboard"),
					"project": projectAttr(),
					"widgets": schema.ListNestedAttribute{Required: true, Description: "1–24 graphs, in order.", NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"title":     schema.StringAttribute{Required: true, Description: "The graph's title."},
							"metric":    schema.StringAttribute{Required: true, Description: "A metric, like cpu, memory, requests or queueDepth."},
							"resources": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: `1–10 resources as "kind/name", or "machine".`},
						}}},
					"timeouts": timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_alert (TF-38) ───────────────────────────────────────────────

type conditionModel struct {
	Type          types.String   `tfsdk:"type"`
	Resources     []types.String `tfsdk:"resources"`
	Metric        types.String   `tfsdk:"metric"`
	Operator      types.String   `tfsdk:"operator"`
	Threshold     types.Float64  `tfsdk:"threshold"`
	ForMinutes    types.Int64    `tfsdk:"for_minutes"`
	Restarts      types.Int64    `tfsdk:"restarts"`
	WindowMinutes types.Int64    `tfsdk:"window_minutes"`
}

type alertModel struct {
	ID            types.String    `tfsdk:"id"`
	Name          types.String    `tfsdk:"name"`
	Project       types.String    `tfsdk:"project"`
	Condition     *conditionModel `tfsdk:"condition"`
	Channels      []types.String  `tfsdk:"channels"`
	RepeatMinutes types.Int64     `tfsdk:"repeat_minutes"`
	Enabled       types.Bool      `tfsdk:"enabled"`
	State         types.String    `tfsdk:"state"`
	Timeouts      timeouts.Value  `tfsdk:"timeouts"`
}

func (m *alertModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *alertModel) limits() timeouts.Value            { return m.Timeouts }

func (m *alertModel) condition() api.AlertCondition {
	c := m.Condition
	if c == nil {
		return api.AlertCondition{}
	}
	out := api.AlertCondition{Type: c.Type.ValueString(), Resources: toStrings(c.Resources), Metric: c.Metric.ValueString(), Operator: c.Operator.ValueString(),
		ForMinutes: int(c.ForMinutes.ValueInt64()), Restarts: int(c.Restarts.ValueInt64()), WindowMinutes: int(c.WindowMinutes.ValueInt64())}
	if !c.Threshold.IsNull() && !c.Threshold.IsUnknown() {
		t := c.Threshold.ValueFloat64()
		out.Threshold = &t
	}
	return out
}

func (m *alertModel) patch() map[string]any {
	p := map[string]any{"condition": m.condition(), "channels": toStrings(m.Channels), "repeatMinutes": m.RepeatMinutes.ValueInt64(), "enabled": nil}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		p["enabled"] = m.Enabled.ValueBool()
	}
	return p
}

func (m *alertModel) spec() any { return m.patch() }

func (m *alertModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.AlertSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	prior := m.Condition
	c := s.Condition
	m.Condition = &conditionModel{Type: types.StringValue(c.Type), Resources: tfStrings(c.Resources), Metric: strOrNull(c.Metric), Operator: strOrNull(c.Operator),
		Threshold: types.Float64Null(), ForMinutes: intKeepOrNull(prior, func(p *conditionModel) types.Int64 { return p.ForMinutes }, c.ForMinutes, c.Type == api.ConditionMetric, api.AlertForMinutesDefault),
		Restarts:      intKeepOrNull(prior, func(p *conditionModel) types.Int64 { return p.Restarts }, c.Restarts, c.Type == api.ConditionCrashLoop, api.AlertRestartsDefault),
		WindowMinutes: intKeepOrNull(prior, func(p *conditionModel) types.Int64 { return p.WindowMinutes }, c.WindowMinutes, c.Type == api.ConditionCrashLoop, api.AlertWindowDefault)}
	if c.Threshold != nil {
		m.Condition.Threshold = types.Float64Value(*c.Threshold)
	}
	m.Channels = tfStrings(s.Channels)
	m.RepeatMinutes = types.Int64Value(int64(s.RepeatMinutes))
	if !m.Enabled.IsNull() || s.Enabled != nil {
		m.Enabled = types.BoolValue(s.IsEnabled())
	}
	m.State = outString(r, "state")
}

// intKeepOrNull keeps a defaulted number null when configuration left it out (no diff after apply).
func intKeepOrNull(prior *conditionModel, get func(*conditionModel) types.Int64, v int, applies bool, def int) types.Int64 {
	if !applies {
		return types.Int64Null()
	}
	if (prior == nil || get(prior).IsNull()) && v == def {
		return types.Int64Null()
	}
	return types.Int64Value(int64(v))
}

func newAlert() resource.Resource {
	return &kindResource[alertModel, *alertModel]{base: base{kind: api.KindAlert}, suffix: "_alert", label: "alert",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A rule that fires when its condition is met and notifies its channels, and again when it resolves.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("alert"),
					"project": projectAttr(),
					"condition": schema.SingleNestedAttribute{Required: true, Description: "What the alert watches.", Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{Required: true, Description: "metric, crash_loop, failed or run_failed.",
							Validators: []validator.String{stringvalidator.OneOf(api.ConditionTypes...)}},
						"resources":      schema.ListAttribute{Required: true, ElementType: types.StringType, Description: `1–10 resources as "kind/name" ("machine" for a machine metric).`},
						"metric":         optString("metric: the metric, like cpu or percentFull."),
						"operator":       schema.StringAttribute{Optional: true, Description: "metric: above or below.", Validators: []validator.String{stringvalidator.OneOf(api.OperatorAbove, api.OperatorBelow)}},
						"threshold":      schema.Float64Attribute{Optional: true, Description: "metric: the line to cross."},
						"for_minutes":    optInt("metric: minutes the line must be crossed (default 5)."),
						"restarts":       optInt("crash_loop: restarts that make a crash loop (default 3)."),
						"window_minutes": optInt("crash_loop: the window restarts are counted in (default 10)."),
					}},
					"channels":       schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Channels to notify."},
					"repeat_minutes": schema.Int64Attribute{Optional: true, Computed: true, Description: "Remind this often while firing; 0 (default) never."},
					"enabled":        schema.BoolAttribute{Optional: true, Description: "false turns the alert off (default on)."},
					"state":          schema.StringAttribute{Computed: true, Description: "ok, firing, no_data or disabled (as last evaluated)."},
					"timeouts":       timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_channel (TF-38) ─────────────────────────────────────────────

type channelEmailModel struct {
	Service types.String   `tfsdk:"service"`
	From    types.String   `tfsdk:"from"`
	To      []types.String `tfsdk:"to"`
}

type channelWebhookModel struct {
	URL    types.String `tfsdk:"url"`
	Secret types.String `tfsdk:"secret"`
}

type channelModel struct {
	ID       types.String         `tfsdk:"id"`
	Name     types.String         `tfsdk:"name"`
	Project  types.String         `tfsdk:"project"`
	Type     types.String         `tfsdk:"type"`
	Email    *channelEmailModel   `tfsdk:"email"`
	Webhook  *channelWebhookModel `tfsdk:"webhook"`
	Secret   types.String         `tfsdk:"secret"`
	ChatID   types.String         `tfsdk:"chat_id"`
	Timeouts timeouts.Value       `tfsdk:"timeouts"`
}

func (m *channelModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *channelModel) limits() timeouts.Value            { return m.Timeouts }

func (m *channelModel) channelSpec() api.ChannelSpec {
	s := api.ChannelSpec{Type: m.Type.ValueString(), Secret: m.Secret.ValueString(), ChatID: m.ChatID.ValueString()}
	if m.Email != nil {
		s.Email = &api.ChannelEmailSettings{Service: m.Email.Service.ValueString(), From: m.Email.From.ValueString(), To: toStrings(m.Email.To)}
	}
	if m.Webhook != nil {
		s.Webhook = &api.WebhookTarget{URL: m.Webhook.URL.ValueString(), Secret: m.Webhook.Secret.ValueString()}
	}
	return s
}

func (m *channelModel) spec() any { return m.channelSpec() }

func (m *channelModel) patch() map[string]any {
	s := m.channelSpec()
	return map[string]any{"email": s.Email, "webhook": s.Webhook, "secret": strOrNil(m.Secret), "chatId": strOrNil(m.ChatID)}
}

func (m *channelModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.ChannelSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Type = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Type)
	m.Email, m.Webhook = nil, nil
	if e := s.Email; e != nil {
		m.Email = &channelEmailModel{Service: types.StringValue(e.Service), From: types.StringValue(e.From), To: tfStrings(e.To)}
	}
	if w := s.Webhook; w != nil {
		m.Webhook = &channelWebhookModel{URL: types.StringValue(w.URL), Secret: strOrNull(w.Secret)}
	}
	m.Secret, m.ChatID = strOrNull(s.Secret), strOrNull(s.ChatID)
}

func newChannel() resource.Resource {
	return &kindResource[channelModel, *channelModel]{base: base{kind: api.KindChannel}, suffix: "_channel", label: "channel",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "Where alerts go: an email service, a webhook, Slack, Discord or Telegram.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("channel"),
					"project": projectAttr(),
					"type": schema.StringAttribute{Required: true, Description: "email, webhook, slack, discord or telegram. Changing it replaces the channel.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.OneOf(api.ChannelTypes...)}},
					"email": schema.SingleNestedAttribute{Optional: true, Description: "email: the service, sender and recipients.", Attributes: map[string]schema.Attribute{
						"service": schema.StringAttribute{Required: true, Description: "An email service of the project."},
						"from":    schema.StringAttribute{Required: true, Description: "The sender."},
						"to":      schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "1–20 recipients."},
					}},
					"webhook": schema.SingleNestedAttribute{Optional: true, Description: "webhook: where JSON is posted.", Attributes: map[string]schema.Attribute{
						"url":    schema.StringAttribute{Required: true, Description: "An https:// URL (http:// for localhost)."},
						"secret": optString("A secret that signs bodies (X-Enclave-Signature)."),
					}},
					"secret":   optString("slack, discord: the secret holding the webhook URL; telegram: the secret holding the bot token."),
					"chat_id":  optString("telegram: the chat ID."),
					"timeouts": timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_log_export (TF-39) ──────────────────────────────────────────

type logHTTPModel struct {
	URL           types.String `tfsdk:"url"`
	HeadersSecret types.String `tfsdk:"headers_secret"`
	Labels        types.Map    `tfsdk:"labels"`
}

type logS3Model struct {
	Endpoint          types.String `tfsdk:"endpoint"`
	Region            types.String `tfsdk:"region"`
	Bucket            types.String `tfsdk:"bucket"`
	Prefix            types.String `tfsdk:"prefix"`
	CredentialsSecret types.String `tfsdk:"credentials_secret"`
}

type logExportModel struct {
	ID           types.String   `tfsdk:"id"`
	Name         types.String   `tfsdk:"name"`
	Project      types.String   `tfsdk:"project"`
	Sources      []types.String `tfsdk:"sources"`
	Filter       types.String   `tfsdk:"filter"`
	FlushSeconds types.Int64    `tfsdk:"flush_seconds"`
	Bucket       *folderModel   `tfsdk:"bucket"`
	S3           *logS3Model    `tfsdk:"s3"`
	HTTP         *logHTTPModel  `tfsdk:"http"`
	OTLP         *logHTTPModel  `tfsdk:"otlp"`
	Loki         *logHTTPModel  `tfsdk:"loki"`
	LinesSent    types.Int64    `tfsdk:"lines_sent"`
	LastSentAt   types.String   `tfsdk:"last_sent_at"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (m *logExportModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *logExportModel) limits() timeouts.Value            { return m.Timeouts }

func logHTTP(h *logHTTPModel, endpoint bool) *api.LogHTTP {
	if h == nil {
		return nil
	}
	out := &api.LogHTTP{HeadersSecret: h.HeadersSecret.ValueString()}
	if endpoint {
		out.Endpoint = h.URL.ValueString()
	} else {
		out.URL = h.URL.ValueString()
	}
	if !h.Labels.IsNull() && !h.Labels.IsUnknown() {
		labels := map[string]string{}
		_ = h.Labels.ElementsAs(context.Background(), &labels, false)
		out.Labels = labels
	}
	return out
}

func (m *logExportModel) destination() api.LogDestination {
	d := api.LogDestination{HTTP: logHTTP(m.HTTP, false), OTLP: logHTTP(m.OTLP, true), Loki: logHTTP(m.Loki, false)}
	if m.Bucket != nil {
		f := folderOf(m.Bucket)
		d.Bucket = &f
	}
	if s := m.S3; s != nil {
		d.S3 = &api.LogS3{S3Destination: api.S3Destination{Endpoint: s.Endpoint.ValueString(), Region: s.Region.ValueString(), Bucket: s.Bucket.ValueString(),
			CredentialsSecret: s.CredentialsSecret.ValueString()}, Prefix: s.Prefix.ValueString()}
	}
	return d
}

func (m *logExportModel) patch() map[string]any {
	return map[string]any{"sources": nilIfNoStrings(m.Sources), "filter": strOrNil(m.Filter), "flushSeconds": intOrNil(m.FlushSeconds), "destination": m.destination()}
}

func (m *logExportModel) spec() any { return m.patch() }

func httpModel(prior *logHTTPModel, h *api.LogHTTP, endpoint bool) *logHTTPModel {
	if h == nil {
		return nil
	}
	u := h.URL
	if endpoint {
		u = h.Endpoint
	}
	out := &logHTTPModel{URL: types.StringValue(u), HeadersSecret: strOrNull(h.HeadersSecret), Labels: types.MapNull(types.StringType)}
	if len(h.Labels) > 0 || (prior != nil && !prior.Labels.IsNull()) {
		out.Labels, _ = types.MapValueFrom(context.Background(), types.StringType, h.Labels)
	}
	return out
}

func (m *logExportModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.LogExportSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.Sources = tfStrings(s.Sources)
	m.Filter = strOrNull(s.Filter)
	def := api.LogFlushDefault
	d := s.Destination
	if d.Bucket != nil || d.S3 != nil {
		def = api.LogFlushBucketDefault
	}
	m.FlushSeconds = defaultKeep(m.FlushSeconds, s.FlushSeconds, def)
	m.Bucket, m.S3 = nil, nil
	if d.Bucket != nil {
		m.Bucket = folderFill(m.Bucket, *d.Bucket, "")
	}
	if d.S3 != nil {
		m.S3 = &logS3Model{Endpoint: types.StringValue(d.S3.Endpoint), Region: strOrNull(d.S3.Region), Bucket: types.StringValue(d.S3.Bucket),
			Prefix: strOrNull(d.S3.Prefix), CredentialsSecret: types.StringValue(d.S3.CredentialsSecret)}
	}
	m.HTTP, m.OTLP, m.Loki = httpModel(m.HTTP, d.HTTP, false), httpModel(m.OTLP, d.OTLP, true), httpModel(m.Loki, d.Loki, false)
	m.LinesSent = outInt(r, "linesSent")
	m.LastSentAt = outString(r, "lastSentAt")
}

func logHTTPAttr(desc string, labels bool) schema.SingleNestedAttribute {
	attrs := map[string]schema.Attribute{
		"url":            schema.StringAttribute{Required: true, Description: "The endpoint's URL."},
		"headers_secret": optString("A secret holding a JSON object of headers to send (tokens, API keys)."),
		"labels":         schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Loki only: extra stream labels."},
	}
	if !labels {
		attrs["labels"] = schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Loki only; must stay unset here."}
	}
	return schema.SingleNestedAttribute{Optional: true, Description: desc, Attributes: attrs}
}

func newLogExport() resource.Resource {
	return &kindResource[logExportModel, *logExportModel]{base: base{kind: api.KindLogExport}, suffix: "_log_export", label: "log export",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "Sends the logs of a project's resources somewhere else, continuously. Set exactly one destination.",
				Attributes: map[string]schema.Attribute{
					"id":            idAttr(),
					"name":          nameAttr("log export"),
					"project":       projectAttr(),
					"sources":       listOfStrings(`Resources as "kind/name" (default every resource of the project with logs).`, false),
					"filter":        optString("Only lines matching this regular expression are sent."),
					"flush_seconds": optInt("How often batches go (5–300; default 10, or 60 for buckets)."),
					"bucket": schema.SingleNestedAttribute{Optional: true, Description: "A bucket of the project: gzip files of JSON lines.", Attributes: map[string]schema.Attribute{
						"bucket": schema.StringAttribute{Required: true, Description: "The bucket."},
						"prefix": schema.StringAttribute{Optional: true, Description: `A folder ending in "/".`},
					}},
					"s3": schema.SingleNestedAttribute{Optional: true, Description: "Any S3-compatible bucket.", Attributes: map[string]schema.Attribute{
						"endpoint":           schema.StringAttribute{Required: true, Description: "The endpoint, like https://s3.eu-west-1.amazonaws.com."},
						"region":             optString("The region."),
						"bucket":             schema.StringAttribute{Required: true, Description: "The bucket."},
						"prefix":             optString(`A folder ending in "/".`),
						"credentials_secret": schema.StringAttribute{Required: true, Description: `A secret holding {"accessKey","secretKey"}.`},
					}},
					"http":         logHTTPAttr("JSON lines POSTed to a URL.", false),
					"otlp":         logHTTPAttr("An OpenTelemetry (OTLP/HTTP) logs endpoint.", false),
					"loki":         logHTTPAttr("Grafana Loki's push API.", true),
					"lines_sent":   schema.Int64Attribute{Computed: true, Description: "Lines sent so far (refreshed on read)."},
					"last_sent_at": schema.StringAttribute{Computed: true, Description: "When a batch last went."},
					"timeouts":     timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_secret_store (TF-40) ────────────────────────────────────────

type storeModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	Project        types.String   `tfsdk:"project"`
	Provider       types.String   `tfsdk:"secret_provider"`
	RefreshSeconds types.Int64    `tfsdk:"refresh_seconds"`
	Settings       types.Map      `tfsdk:"settings"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (m *storeModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *storeModel) limits() timeouts.Value            { return m.Timeouts }

// storeSettingKeys maps the flat settings map to each provider's JSON fields.
var storeSettingKeys = map[string][]string{
	api.ProviderVault:       {"address", "mount", "namespace", "tokenSecret"},
	api.ProviderOnePassword: {"connectUrl", "vault", "tokenSecret"},
	api.ProviderAWS:         {"region", "credentialsSecret", "endpoint"},
	api.ProviderDoppler:     {"tokenSecret", "endpoint"},
	api.ProviderInfisical:   {"siteUrl", "projectId", "environment", "path", "credentialsSecret"},
}

// storeDefaults are the values the server fills in when a setting is left out.
var storeDefaults = map[string]string{"mount": api.VaultMountDefault, "siteUrl": api.InfisicalSiteDefault, "path": "/"}

func (m *storeModel) block() map[string]string {
	out := map[string]string{}
	if !m.Settings.IsNull() && !m.Settings.IsUnknown() {
		_ = m.Settings.ElementsAs(context.Background(), &out, false)
	}
	return out
}

func (m *storeModel) patch() map[string]any {
	return map[string]any{"provider": m.Provider.ValueString(), "refreshSeconds": intOrNil(m.RefreshSeconds), m.Provider.ValueString(): m.block()}
}

func (m *storeModel) spec() any { return m.patch() }

func (m *storeModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(r.Spec, &raw)
	var s api.SecretStoreSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Provider = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Provider)
	m.RefreshSeconds = defaultKeep(m.RefreshSeconds, s.RefreshSeconds, api.StoreRefreshDefault)
	var stored map[string]string
	_ = json.Unmarshal(raw[s.Provider], &stored)
	configured := m.block()
	kept := map[string]string{}
	for _, k := range storeSettingKeys[s.Provider] {
		v := stored[k]
		if v == "" {
			continue
		}
		_, set := configured[k]
		if set || (m.Settings.IsNull() && v != storeDefaults[k]) { // server defaults left out of configuration stay out of state
			kept[k] = v
		}
	}
	m.Settings, _ = types.MapValueFrom(context.Background(), types.StringType, kept)
}

func newSecretStore() resource.Resource {
	return &kindResource[storeModel, *storeModel]{base: base{kind: api.KindSecretStore}, suffix: "_secret_store", label: "secret store",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A connection to an outside secret manager. A secret with a source reads its value from it.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("secret store"),
					"project": projectAttr(),
					"secret_provider": schema.StringAttribute{Required: true, Description: "vault, onepassword, aws, doppler or infisical (\"provider\" is reserved by OpenTofu). Changing it replaces the store.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.OneOf(api.SecretStoreProviders...)}},
					"refresh_seconds": optInt("How often synced secrets are read again (60–86400; default 300)."),
					"settings": schema.MapAttribute{Required: true, ElementType: types.StringType,
						Description: fmt.Sprintf("The provider's settings, as the API names them: %v.", storeSettingKeys)},
					"timeouts": timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── data source enclave_metrics (TF-37) ─────────────────────────────────

type metricsDataModel struct {
	Resource types.String  `tfsdk:"resource"`
	Project  types.String  `tfsdk:"project"`
	Metric   types.String  `tfsdk:"metric"`
	Since    types.String  `tfsdk:"since"`
	Unit     types.String  `tfsdk:"unit"`
	Latest   types.Float64 `tfsdk:"latest"`
	Values   types.List    `tfsdk:"values"`
}

type metricsData struct{ c *client.Client }

func newMetricsData() datasource.DataSource { return &metricsData{} }

func (d *metricsData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_metrics"
}

func (d *metricsData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*client.Client); ok {
		d.c = c
	}
}

func (d *metricsData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "One metric of a resource or of the machine, as EnclaveStack sampled it.",
		Attributes: map[string]dschema.Attribute{
			"resource": dschema.StringAttribute{Required: true, Description: `"kind/name", or "machine".`},
			"project":  dschema.StringAttribute{Optional: true, Description: "The resource's project; defaults to the provider's."},
			"metric":   dschema.StringAttribute{Required: true, Description: "The metric, like cpu or requests."},
			"since":    dschema.StringAttribute{Optional: true, Description: "How far back, like 1h (default)."},
			"unit":     dschema.StringAttribute{Computed: true, Description: "The metric's unit."},
			"latest":   dschema.Float64Attribute{Computed: true, Description: "The newest value (null without data)."},
			"values":   dschema.ListAttribute{Computed: true, ElementType: types.Float64Type, Description: "The values, oldest first."},
		},
	}
}

func (d *metricsData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m metricsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	since := time.Hour
	if s := m.Since.ValueString(); s != "" {
		v, err := time.ParseDuration(s)
		if err != nil {
			resp.Diagnostics.AddError("Invalid since", "Use a duration like 30m or 6h.")
			return
		}
		since = v
	}
	c := d.c
	if p := m.Project.ValueString(); p != "" {
		c = c.InProject(p)
	}
	var data api.MetricData
	var err error
	if m.Resource.ValueString() == api.MachineRef {
		data, err = c.MachineMetrics(ctx, []string{m.Metric.ValueString()}, time.Now().Add(-since), time.Time{})
	} else {
		kind, name, ok := api.ParseRef(m.Resource.ValueString())
		if !ok {
			resp.Diagnostics.AddError("Invalid resource", `Use "kind/name", like "app/api", or "machine".`)
			return
		}
		r, ferr := c.FindResource(ctx, kind, name)
		if ferr != nil {
			resp.Diagnostics.Append(apiDiag("Read metrics", ferr))
			return
		}
		data, err = c.ResourceMetrics(ctx, r.ID, []string{m.Metric.ValueString()}, time.Now().Add(-since), time.Time{})
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read metrics", err))
		return
	}
	var values []float64
	m.Unit, m.Latest = types.StringNull(), types.Float64Null()
	if len(data.Items) > 0 {
		m.Unit = types.StringValue(data.Items[0].Unit)
		for _, p := range data.Items[0].Points {
			values = append(values, p.V)
		}
		if len(values) > 0 {
			m.Latest = types.Float64Value(values[len(values)-1])
		}
	}
	m.Values, _ = types.ListValueFrom(ctx, types.Float64Type, values)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
