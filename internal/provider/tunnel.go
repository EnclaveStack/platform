package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-003 TF-51 – TF-53, L2-014): tunnels, and the gate shared with public domains.

// gateModel is the gate on an app link or a public domain (GATE-01 – GATE-06).
type gateModel struct {
	Mode           types.String `tfsdk:"mode"`
	PasswordSecret types.String `tfsdk:"password_secret"`
	OpenPaths      types.List   `tfsdk:"open_paths"`
}

func gateAttr(what string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Optional: true,
		Description: "Who may open " + what + ". Unset: signin (people with access to the project).",
		Attributes: map[string]schema.Attribute{
			"mode": schema.StringAttribute{Required: true, Description: "signin, password (one shared username and password) or none (anyone on the internet).",
				Validators: []validator.String{stringvalidator.OneOf(api.GateModes...)}},
			"password_secret": schema.StringAttribute{Optional: true, Description: `With mode password: a secret holding {"username","password"}.`},
			"open_paths": schema.ListAttribute{Optional: true, ElementType: types.StringType,
				Description: "Path prefixes that skip the gate, like /webhooks/ (up to 10)."},
		}}
}

func (g *gateModel) gate(ctx context.Context) (*api.Gate, diag.Diagnostics) {
	if g == nil {
		return nil, nil
	}
	out := &api.Gate{Mode: g.Mode.ValueString(), PasswordSecret: g.PasswordSecret.ValueString()}
	var d diag.Diagnostics
	if !g.OpenPaths.IsNull() && !g.OpenPaths.IsUnknown() {
		d = g.OpenPaths.ElementsAs(ctx, &out.OpenPaths, false)
	}
	return out, d
}

func gateModelOf(g *api.Gate, prior *gateModel) *gateModel {
	if g == nil {
		return nil
	}
	var priorPaths types.List = types.ListNull(types.StringType)
	if prior != nil {
		priorPaths = prior.OpenPaths
	}
	return &gateModel{Mode: types.StringValue(g.Mode), PasswordSecret: strOrNull(g.PasswordSecret), OpenPaths: listOrNull(g.OpenPaths, priorPaths)}
}

// ─── enclave_tunnel (TF-51) ──────────────────────────────────────────────

type tunnelModel struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Project       types.String   `tfsdk:"project"`
	Provider      types.String   `tfsdk:"tunnel_provider"`
	Credentials   types.String   `tfsdk:"credentials_secret"`
	AccountID     types.String   `tfsdk:"account_id"`
	Machine       types.String   `tfsdk:"machine"`
	App           types.String   `tfsdk:"app"`
	Port          types.Int64    `tfsdk:"port"`
	Gate          *gateModel     `tfsdk:"gate"`
	Mode          types.String   `tfsdk:"mode"`
	Hostname      types.String   `tfsdk:"hostname"`
	Domain        types.String   `tfsdk:"domain"`
	Image         types.String   `tfsdk:"image"`
	Command       types.List     `tfsdk:"command"`
	Env           types.Map      `tfsdk:"env"`
	SecretEnv     types.Map      `tfsdk:"secret_env"`
	ConnectorURL  types.String   `tfsdk:"connector_url"`
	URL           types.String   `tfsdk:"url"`
	TunnelID      types.String   `tfsdk:"tunnel_id"`
	PublicAddress types.String   `tfsdk:"public_address"`
	GateMode      types.String   `tfsdk:"gate_mode"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

type tunnelResource struct{ base }

func newTunnel() resource.Resource { return &tunnelResource{base{kind: api.KindTunnel}} }

func (r *tunnelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tunnel"
}

func (r *tunnelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *tunnelResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A way in from the internet; nothing on the router is opened. App links (quick, tailscale, ngrok, custom) give one app an address; " +
			"domain tunnels (cloudflare, relay) serve the enclave_domain resources that name them. Apply waits until it is connected.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("tunnel"),
			"project": projectAttr(),
			"tunnel_provider": schema.StringAttribute{Required: true, Description: "quick, tailscale, ngrok, custom, cloudflare or relay (\"provider\" is reserved by OpenTofu). Changing it replaces the tunnel.",
				PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf(api.TunnelProviders...)}},
			"credentials_secret": schema.StringAttribute{Optional: true,
				Description: "cloudflare, tailscale, ngrok: the secret holding the API token, auth key or authtoken."},
			"account_id": schema.StringAttribute{Optional: true, Description: "cloudflare: the account ID (32 hex characters)."},
			"machine": schema.StringAttribute{Optional: true, Description: "relay: the edge machine it runs on. Changing it replaces the tunnel.",
				PlanModifiers: replace},
			"app": schema.StringAttribute{Optional: true, Description: "App links: the app to make public."},
			"port": schema.Int64Attribute{Optional: true, Description: "App links: the app's port (default: the app's).",
				Validators: []validator.Int64{int64validator.Between(api.PortMin, api.PortMax)}},
			"gate": gateAttr("an app link (unset on a private Tailscale link: its tailnet's devices, no gate)"),
			"mode": schema.StringAttribute{Optional: true, Computed: true, Description: "tailscale: private (the tailnet's devices, default) or public (Funnel).",
				Validators: []validator.String{stringvalidator.OneOf(api.TailscaleModes...)}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"hostname":       schema.StringAttribute{Optional: true, Description: "tailscale: the device's name (default <project>-<app>)."},
			"domain":         schema.StringAttribute{Optional: true, Description: "ngrok: a domain reserved in the account."},
			"image":          schema.StringAttribute{Optional: true, Description: "custom: the connector's image."},
			"command":        schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "custom: the connector's command; $ENCLAVE_UPSTREAM is where to send visitors."},
			"env":            schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "custom: environment variables."},
			"secret_env":     schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "custom: environment variables from secrets: variable name → secret name."},
			"connector_url":  schema.StringAttribute{Optional: true, Description: "custom: the https:// address the service gives (shown as url)."},
			"url":            schema.StringAttribute{Computed: true, Description: "App links: the public address (a quick link's changes when it restarts)."},
			"tunnel_id":      schema.StringAttribute{Computed: true, Description: "cloudflare: the tunnel's ID at Cloudflare."},
			"public_address": schema.StringAttribute{Computed: true, Description: "relay: the edge machine's address, where the domains' records point."},
			"gate_mode":      schema.StringAttribute{Computed: true, Description: "App links: the gate in force, with the default applied."},
			"timeouts":       timeoutsAttr(ctx),
		},
	}
}

func (m *tunnelModel) spec(ctx context.Context) (api.TunnelSpec, diag.Diagnostics) {
	s := api.TunnelSpec{Provider: m.Provider.ValueString(), CredentialsSecret: m.Credentials.ValueString(), AccountID: m.AccountID.ValueString(),
		Machine: m.Machine.ValueString(), App: m.App.ValueString(), Port: int(m.Port.ValueInt64()), Mode: m.Mode.ValueString(),
		Hostname: m.Hostname.ValueString(), Domain: m.Domain.ValueString(), Image: m.Image.ValueString(), URL: m.ConnectorURL.ValueString()}
	gate, diags := m.Gate.gate(ctx)
	s.Gate = gate
	if !m.Command.IsNull() && !m.Command.IsUnknown() {
		diags.Append(m.Command.ElementsAs(ctx, &s.Command, false)...)
	}
	if !m.Env.IsNull() && !m.Env.IsUnknown() {
		diags.Append(m.Env.ElementsAs(ctx, &s.Env, false)...)
	}
	if !m.SecretEnv.IsNull() && !m.SecretEnv.IsUnknown() {
		diags.Append(m.SecretEnv.ElementsAs(ctx, &s.SecretEnv, false)...)
	}
	return s, diags
}

func (m *tunnelModel) fill(r api.Resource) {
	var s api.TunnelSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Project, m.Provider = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(r.Project), types.StringValue(s.Provider)
	m.Credentials, m.AccountID, m.Machine, m.App = strOrNull(s.CredentialsSecret), strOrNull(s.AccountID), strOrNull(s.Machine), strOrNull(s.App)
	m.Port = types.Int64Null()
	if s.Port > 0 {
		m.Port = types.Int64Value(int64(s.Port))
	}
	m.Gate = gateModelOf(s.Gate, m.Gate)
	m.Mode = strOrNull(s.Mode)
	m.Hostname, m.Domain, m.Image, m.ConnectorURL = strOrNull(s.Hostname), strOrNull(s.Domain), strOrNull(s.Image), strOrNull(s.URL)
	m.Command = listOrNull(s.Command, m.Command)
	m.Env, m.SecretEnv = mapOrNull(s.Env, m.Env), mapOrNull(s.SecretEnv, m.SecretEnv)
	m.URL, m.TunnelID, m.PublicAddress, m.GateMode = outString(r, "url"), outString(r, "tunnelId"), outString(r, "publicAddress"), outString(r, "gate")
}

func (r *tunnelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tunnelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	spec, d := plan.spec(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), spec, timeout)
	if res.ID != "" {
		plan.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create tunnel", err))
	}
}

func (r *tunnelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tunnelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read tunnel", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tunnelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tunnelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	s, d := plan.spec(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Send every changeable field: the API replaces present fields and clears the ones set to null.
	patch := map[string]any{"credentialsSecret": nilIfEmptyString(s.CredentialsSecret), "accountId": nilIfEmptyString(s.AccountID),
		"app": nilIfEmptyString(s.App), "port": nilIfZero(s.Port), "gate": s.Gate, "hostname": nilIfEmptyString(s.Hostname),
		"domain": nilIfEmptyString(s.Domain), "image": nilIfEmptyString(s.Image), "command": nilIfEmpty(s.Command),
		"env": nilIfEmptyMap(s.Env), "secretEnv": nilIfEmptyMap(s.SecretEnv), "url": nilIfEmptyString(s.URL)}
	if s.Mode != "" {
		patch["mode"] = s.Mode
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update tunnel", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tunnelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tunnelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete tunnel", err))
	}
}

func (r *tunnelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// ─── public access on enclave_domain (TF-52) ─────────────────────────────

type cloudflareAccessModel struct {
	Emails       types.List `tfsdk:"emails"`
	EmailDomains types.List `tfsdk:"email_domains"`
}

func cloudflareAccessAttr() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Optional: true,
		Description: "Through a cloudflare tunnel: Cloudflare Access also checks visitors before the gate, letting in these addresses.",
		Attributes: map[string]schema.Attribute{
			"emails":        schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Email addresses let in (up to 50)."},
			"email_domains": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Email domains let in, like example.com (up to 10)."},
		}}
}

func (a *cloudflareAccessModel) access(ctx context.Context) (*api.CloudflareAccess, diag.Diagnostics) {
	if a == nil {
		return nil, nil
	}
	var out api.CloudflareAccess
	var d diag.Diagnostics
	if !a.Emails.IsNull() && !a.Emails.IsUnknown() {
		d.Append(a.Emails.ElementsAs(ctx, &out.Emails, false)...)
	}
	if !a.EmailDomains.IsNull() && !a.EmailDomains.IsUnknown() {
		d.Append(a.EmailDomains.ElementsAs(ctx, &out.EmailDomains, false)...)
	}
	return &out, d
}

func cloudflareAccessModelOf(a *api.CloudflareAccess, prior *cloudflareAccessModel) *cloudflareAccessModel {
	if a == nil {
		return nil
	}
	pe, pd := types.ListNull(types.StringType), types.ListNull(types.StringType)
	if prior != nil {
		pe, pd = prior.Emails, prior.EmailDomains
	}
	return &cloudflareAccessModel{Emails: listOrNull(a.Emails, pe), EmailDomains: listOrNull(a.EmailDomains, pd)}
}

var dnsRecordAttrTypes = map[string]attr.Type{"type": types.StringType, "name": types.StringType, "value": types.StringType, "state": types.StringType}

func dnsRecordAttr() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Computed: true,
		Description: "A public domain's DNS record: type, name, value and state (created, adopted, conflict or manual: add it at your DNS host).",
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{Computed: true}, "name": schema.StringAttribute{Computed: true},
			"value": schema.StringAttribute{Computed: true}, "state": schema.StringAttribute{Computed: true},
		}}
}

func dnsRecordOf(r api.Resource) types.Object {
	m, ok := r.Outputs["dnsRecord"].(map[string]any)
	if !ok {
		return types.ObjectNull(dnsRecordAttrTypes)
	}
	vals := map[string]attr.Value{}
	for k := range dnsRecordAttrTypes {
		s, _ := m[k].(string)
		vals[k] = types.StringValue(s)
	}
	return types.ObjectValueMust(dnsRecordAttrTypes, vals)
}
