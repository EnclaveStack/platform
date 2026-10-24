package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// ─── enclave_domain (TF-11) ──────────────────────────────────────────────

type domainModel struct {
	ID            types.String           `tfsdk:"id"`
	Name          types.String           `tfsdk:"name"`
	Project       types.String           `tfsdk:"project"`
	Hostname      types.String           `tfsdk:"hostname"`
	App           types.String           `tfsdk:"app"`
	Port          types.Int64            `tfsdk:"port"`
	TLS           types.String           `tfsdk:"tls"`
	RedirectHTTP  types.Bool             `tfsdk:"redirect_http"`
	URL           types.String           `tfsdk:"url"`
	CertState     types.String           `tfsdk:"certificate_state"`
	CertExpiresAt types.String           `tfsdk:"certificate_expires_at"`
	CertIssuer    types.String           `tfsdk:"certificate_issuer"`
	Zone          types.String           `tfsdk:"zone"`
	Tunnel        types.String           `tfsdk:"tunnel"`
	Gate          *gateModel             `tfsdk:"gate"`
	Access        *cloudflareAccessModel `tfsdk:"cloudflare_access"`
	Public        types.Bool             `tfsdk:"public"`
	DNSRecord     types.Object           `tfsdk:"dns_record"`
	Timeouts      timeouts.Value         `tfsdk:"timeouts"`
}

type domainResource struct{ base }

func newDomain() resource.Resource { return &domainResource{base{kind: api.KindDomain}} }

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *domainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *domainResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A hostname served over HTTPS and routed to an app. Apply waits for the certificate.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("domain"),
			"project": projectAttr(),
			"hostname": schema.StringAttribute{Required: true, Description: "Hostname, e.g. api.example.com or web.localhost. Changing it replaces the domain.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"app": schema.StringAttribute{Required: true, Description: "Name of the app to route to."},
			"port": schema.Int64Attribute{Optional: true, Description: "App port (default: the app's port).",
				Validators: []validator.Int64{int64validator.Between(api.PortMin, api.PortMax)}},
			"tls": schema.StringAttribute{Optional: true, Computed: true,
				Description:   "auto (Let's Encrypt), internal (EnclaveStack CA) or off. Default: internal for .localhost names, else auto.",
				Validators:    []validator.String{stringvalidator.OneOf(api.TLSModes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"redirect_http": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Redirect plain HTTP to HTTPS."},
			"url":                    schema.StringAttribute{Computed: true, Description: "Where the app is served."},
			"certificate_state":      schema.StringAttribute{Computed: true, Description: "issued, pending, failed or none."},
			"certificate_expires_at": schema.StringAttribute{Computed: true, Description: "When the current certificate expires (RFC 3339)."},
			"certificate_issuer":     schema.StringAttribute{Computed: true, Description: "Who issued the current certificate."},
			"zone": schema.StringAttribute{Optional: true,
				Description: "Zone to get the certificate through (ACME DNS-01): no inbound port needed, and the hostname may be a wildcard like *.example.com. " +
					"On a public domain, the zone EnclaveStack writes its DNS record in (required through a cloudflare tunnel)."},
			"tunnel": schema.StringAttribute{Optional: true,
				Description: "Make it public through this cloudflare or relay tunnel (L2-014). Unset: served on this machine only."},
			"gate":              gateAttr("a public domain"),
			"cloudflare_access": cloudflareAccessAttr(),
			"public":            schema.BoolAttribute{Computed: true, Description: "Whether it is served to the internet through a tunnel."},
			"dns_record":        dnsRecordAttr(),
			"timeouts":          timeoutsAttr(ctx),
		},
	}
}

func (m *domainModel) spec(ctx context.Context) (api.DomainSpec, diag.Diagnostics) {
	redirect := m.RedirectHTTP.ValueBool()
	if m.RedirectHTTP.IsNull() || m.RedirectHTTP.IsUnknown() {
		redirect = true
	}
	s := api.DomainSpec{Hostname: m.Hostname.ValueString(), App: m.App.ValueString(), Port: int(m.Port.ValueInt64()),
		TLS: m.TLS.ValueString(), RedirectHTTP: &redirect, Zone: m.Zone.ValueString(), Tunnel: m.Tunnel.ValueString()}
	gate, diags := m.Gate.gate(ctx)
	access, d := m.Access.access(ctx)
	s.Gate, s.CloudflareAccess = gate, access
	diags.Append(d...)
	return s, diags
}

func (m *domainModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.DomainSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Hostname, m.App = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Hostname), types.StringValue(s.App)
	m.Port = types.Int64Null()
	if s.Port > 0 {
		m.Port = types.Int64Value(int64(s.Port))
	}
	m.TLS = types.StringValue(s.TLS)
	m.RedirectHTTP = types.BoolValue(s.RedirectHTTP == nil || *s.RedirectHTTP)
	m.URL, m.CertState = outString(r, "url"), outString(r, "certificateState")
	m.CertExpiresAt, m.CertIssuer = outString(r, "certificateExpiresAt"), outString(r, "certificateIssuer")
	m.Zone, m.Tunnel = strOrNull(s.Zone), strOrNull(s.Tunnel)
	m.Gate, m.Access = gateModelOf(s.Gate, m.Gate), cloudflareAccessModelOf(s.CloudflareAccess, m.Access)
	m.Public, m.DNSRecord = types.BoolValue(s.IsPublic()), dnsRecordOf(r)
}

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainModel
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
		resp.Diagnostics.Append(apiDiag("Create domain", err))
	}
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read domain", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	s, d := plan.spec(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{"app": s.App, "port": nilIfZero(s.Port), "redirectHttp": *s.RedirectHTTP, "zone": nilIfEmptyString(s.Zone),
		"tunnel": nilIfEmptyString(s.Tunnel), "gate": s.Gate, "cloudflareAccess": s.CloudflareAccess} // nil encodes as null: cleared
	if s.TLS != "" {
		patch["tls"] = s.TLS
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update domain", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete domain", err))
	}
}

func (r *domainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// ─── enclave_machine_settings (TF-11) ────────────────────────────────────

// machineSettingsID is the fixed ID of the singleton settings resource.
const machineSettingsID = "machine"

type inviteEmailModel struct {
	Project types.String `tfsdk:"project"`
	Service types.String `tfsdk:"service"`
	From    types.String `tfsdk:"from"`
}

type machineSettingsModel struct {
	ID            types.String      `tfsdk:"id"`
	PublicAddress types.String      `tfsdk:"public_address"`
	AcmeEmail     types.String      `tfsdk:"acme_email"`
	InviteEmail   *inviteEmailModel `tfsdk:"invite_email"`
}

type machineSettingsResource struct{ base }

func newMachineSettings() resource.Resource { return &machineSettingsResource{} }

func (r *machineSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_settings"
}

func (r *machineSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *machineSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Machine-wide settings for domains and invites. One per machine; destroying it clears them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"public_address": schema.StringAttribute{Optional: true,
				Description: "IP or hostname that domain DNS records point to; shown in DNS instructions. Never auto-detected."},
			"acme_email": schema.StringAttribute{Optional: true, Description: "Email for the Let's Encrypt account (default: the admin's)."},
			"invite_email": schema.SingleNestedAttribute{Optional: true, Description: "Email invite and reset links through an email service (IAM-04); unset, links are only shown.",
				Attributes: map[string]schema.Attribute{
					"project": schema.StringAttribute{Optional: true, Computed: true, Description: "The email service's project (default: default).",
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
					"service": schema.StringAttribute{Required: true, Description: "The email service that sends them."},
					"from":    schema.StringAttribute{Required: true, Description: "The sender, like enclave@example.com."},
				}},
		},
	}
}

func (m *machineSettingsModel) request() api.MachineSettings {
	var out api.MachineSettings
	if !m.PublicAddress.IsNull() {
		v := m.PublicAddress.ValueString()
		out.PublicAddress = &v
	}
	if !m.AcmeEmail.IsNull() {
		v := m.AcmeEmail.ValueString()
		out.AcmeEmail = &v
	}
	// The resource owns the setting: no block turns emailed invites off.
	if ie := m.InviteEmail; ie != nil {
		out.InviteEmail = &api.InviteEmail{Project: ie.Project.ValueString(), Service: ie.Service.ValueString(), From: ie.From.ValueString()}
	} else {
		out.ClearInviteEmail = true
	}
	return out
}

func (m *machineSettingsModel) fill(mc api.Machine) {
	m.ID = types.StringValue(machineSettingsID)
	m.PublicAddress, m.AcmeEmail = types.StringPointerValue(mc.PublicAddress), types.StringPointerValue(mc.AcmeEmail)
	m.InviteEmail = nil
	if ie := mc.InviteEmail; ie != nil {
		m.InviteEmail = &inviteEmailModel{Project: types.StringValue(ie.Project), Service: types.StringValue(ie.Service), From: types.StringValue(ie.From)}
	}
}

func (r *machineSettingsResource) apply(ctx context.Context, m *machineSettingsModel) error {
	mc, err := r.c.UpdateMachine(ctx, m.request())
	if err != nil {
		return err
	}
	m.fill(mc)
	return nil
}

func (r *machineSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		resp.Diagnostics.Append(apiDiag("Set machine settings", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *machineSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	mc, err := r.c.Machine(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read machine settings", err))
		return
	}
	state.fill(mc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *machineSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan machineSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		resp.Diagnostics.Append(apiDiag("Set machine settings", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *machineSettingsResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	if _, err := r.c.UpdateMachine(ctx, api.MachineSettings{ClearInviteEmail: true}); err != nil {
		resp.Diagnostics.Append(apiDiag("Clear machine settings", err))
	}
}

func (r *machineSettingsResource) ImportState(ctx context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), machineSettingsID)...)
}

// ─── enclave_ca (data source, TF-11) ─────────────────────────────────────

type caData struct{ dataBase }

type caModel struct {
	PEM types.String `tfsdk:"pem"`
}

func newCAData() datasource.DataSource { return &caData{} }

func (d *caData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ca"
}

func (d *caData) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req, resp)
}

func (d *caData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "The root certificate of the machine's internal CA, which signs internal-TLS domains.",
		Attributes:  map[string]dschema.Attribute{"pem": dschema.StringAttribute{Computed: true, Description: "PEM-encoded root certificate."}},
	}
}

func (d *caData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	pem, err := d.c.RootCA(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read the root certificate", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &caModel{PEM: types.StringValue(string(pem))})...)
}
