package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

type dataBase struct{ c *client.Client }

func (d *dataBase) configure(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	d.c = c
}

// ─── enclave_machine ─────────────────────────────────────────────────────

type machineData struct{ dataBase }

type machineModel struct {
	Name                types.String `tfsdk:"name"`
	Hostname            types.String `tfsdk:"hostname"`
	Version             types.String `tfsdk:"version"`
	EngineDriver        types.String `tfsdk:"engine_driver"`
	EngineAvailable     types.Bool   `tfsdk:"engine_available"`
	MemoryTotalBytes    types.Int64  `tfsdk:"memory_total_bytes"`
	MemoryReservedBytes types.Int64  `tfsdk:"memory_reserved_bytes"`
}

func newMachineData() datasource.DataSource { return &machineData{} }

func (d *machineData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine"
}

func (d *machineData) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req, resp)
}

func (d *machineData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The EnclaveStack machine this provider talks to.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Computed: true}, "hostname": schema.StringAttribute{Computed: true},
			"version": schema.StringAttribute{Computed: true}, "engine_driver": schema.StringAttribute{Computed: true},
			"engine_available": schema.BoolAttribute{Computed: true}, "memory_total_bytes": schema.Int64Attribute{Computed: true},
			"memory_reserved_bytes": schema.Int64Attribute{Computed: true},
		},
	}
}

func (d *machineData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	m, err := d.c.Machine(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read machine", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &machineModel{
		Name: types.StringValue(m.Name), Hostname: types.StringValue(m.Hostname), Version: types.StringValue(m.Version),
		EngineDriver: types.StringValue(m.Engine.Driver), EngineAvailable: types.BoolValue(m.Engine.Available),
		MemoryTotalBytes: types.Int64Value(m.MemoryTotalBytes), MemoryReservedBytes: types.Int64Value(m.MemoryReservedBytes),
	})...)
}

// ─── enclave_secret_value ────────────────────────────────────────────────

// secretValueData is the only way to read a secret's value in OpenTofu (TF-06). Each read is audited
// on the server as a secret_revealed event.
type secretValueData struct{ dataBase }

type secretValueModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	Value   types.String `tfsdk:"value"`
}

func newSecretValueData() datasource.DataSource { return &secretValueData{} }

func (d *secretValueData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret_value"
}

func (d *secretValueData) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req, resp)
}

func (d *secretValueData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a secret's value, for example a database's managed password. Every read is recorded on the server.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true},
			"name":    schema.StringAttribute{Required: true, Description: "Secret name."},
			"project": schema.StringAttribute{Optional: true, Computed: true, Description: "The secret's project; defaults to the provider's."},
			"value":   schema.StringAttribute{Computed: true, Sensitive: true},
		},
	}
}

func (d *secretValueData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m secretValueModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := d.c
	if p := m.Project.ValueString(); p != "" {
		c = c.InProject(p)
	}
	sec, err := c.FindResource(ctx, api.KindSecret, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read secret", err))
		return
	}
	v, err := d.c.RevealSecret(ctx, sec.ID)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read secret value", err))
		return
	}
	m.ID, m.Project, m.Value = types.StringValue(sec.ID), types.StringValue(sec.Project), types.StringValue(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
