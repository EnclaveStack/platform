package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Machines (L2-003 TF-42, TF-47; L3-002 §11.1): adding one returns the command that joins its agent.

// ─── enclave_machine ─────────────────────────────────────────────────────

type machineResourceModel struct {
	ID          types.String            `tfsdk:"id"`
	Name        types.String            `tfsdk:"name"`
	Labels      map[string]types.String `tfsdk:"labels"`
	Schedulable types.Bool              `tfsdk:"schedulable"`
	State       types.String            `tfsdk:"state"`
	Address     types.String            `tfsdk:"address"`
	Role        types.String            `tfsdk:"role"`
	AgentPort   types.Int64             `tfsdk:"agent_port"`
	JoinCommand types.String            `tfsdk:"join_command"`
}

type machineResource struct{ base }

func newMachine() resource.Resource { return &machineResource{} }

func (r *machineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine"
}

func (r *machineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *machineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Another machine that runs resources. Creating it returns join_command: run it on that machine to join its agent. Destroying it is refused while anything runs there.",
		Attributes: map[string]schema.Attribute{
			"id": idAttr(),
			"name": schema.StringAttribute{Required: true, Description: "Machine name. Changing it replaces the machine.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{nameValidator{}}},
			"labels": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Labels resources can ask for with machine_labels (up to 10). Change in place."},
			"schedulable": schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{schedulableDefault{}},
				Description: "Whether new resources may be placed here (false keeps what runs there). Default true; always false on an edge machine."},
			"state": schema.StringAttribute{Computed: true, Description: "waiting (for its agent), online or offline (refreshed on read)."},
			"role": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.MachineWorker),
				Description:   "worker (default) or edge: a server with a public address that only relays visitors for relay tunnels (L2-014). Changing it replaces the machine.",
				Validators:    []validator.String{stringvalidator.OneOf(api.MachineRoles...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"address": schema.StringAttribute{Optional: true, Computed: true,
				Description:   "Where its agent is reached, once joined. An edge machine is added with it: its public IPv4 address or host name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIf(addressGiven, "", "")}},
			"agent_port": schema.Int64Attribute{Optional: true, Description: "Edge machines: where its agent waits (default 7071).",
				Validators: []validator.Int64{int64validator.Between(api.PortMin, api.PortMax)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"join_command": schema.StringAttribute{Computed: true, Sensitive: true,
				Description:   "The command that joins the machine's agent, valid for an hour and once; empty once joined.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func labelsOf(m map[string]types.String) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v.ValueString()
	}
	return out
}

// schedulableDefault plans schedulable as true when unset, or false for an edge machine (EDG-01).
type schedulableDefault struct{}

func (schedulableDefault) Description(context.Context) string {
	return "true unless the machine is an edge machine"
}
func (d schedulableDefault) MarkdownDescription(ctx context.Context) string {
	return d.Description(ctx)
}

func (schedulableDefault) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	var role types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("role"), &role)...)
	resp.PlanValue = types.BoolValue(role.ValueString() != api.MachineEdge)
}

// addressGiven replaces the machine when the address it was added with changes; a worker's address is the
// one its agent reports, so it never forces a replacement.
func addressGiven(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.ConfigValue.IsNull() && !req.StateValue.IsNull() && req.ConfigValue != req.StateValue
}

func (m *machineResourceModel) fill(s api.MachineSummary) {
	m.ID, m.Name, m.Schedulable, m.State = types.StringValue(s.ID), types.StringValue(s.Name), types.BoolValue(s.Schedulable), types.StringValue(s.State)
	m.Role = types.StringValue(s.Role)
	if s.Role == "" {
		m.Role = types.StringValue(api.MachineWorker)
	}
	m.Address = types.StringNull()
	if s.Address != nil {
		m.Address = types.StringValue(*s.Address)
	}
	if len(s.Labels) > 0 || m.Labels != nil {
		m.Labels = map[string]types.String{}
		for k, v := range s.Labels {
			m.Labels[k] = types.StringValue(v)
		}
	}
	if s.State != api.MachineWaiting {
		m.JoinCommand = types.StringValue("")
	}
}

func (r *machineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan machineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The server checks the role's fields: only an edge machine takes an address and an agent port.
	added, err := r.c.AddMachine(ctx, api.AddMachine{Name: plan.Name.ValueString(), Labels: labelsOf(plan.Labels), Role: plan.Role.ValueString(),
		Address: plan.Address.ValueString(), AgentPort: int(plan.AgentPort.ValueInt64())})
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Add machine", err))
		return
	}
	m := added.Machine
	if !plan.Schedulable.ValueBool() && m.Role != api.MachineEdge {
		no := false
		if m, err = r.c.UpdateMachineByName(ctx, m.Name, api.UpdateMachine{Schedulable: &no}); err != nil {
			resp.Diagnostics.Append(apiDiag("Update machine", err))
		}
	}
	plan.JoinCommand = types.StringValue(added.JoinCommand)
	plan.fill(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *machineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state machineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.c.GetMachineByName(ctx, state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // TF-04
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read machine", err))
		return
	}
	state.fill(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *machineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan machineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	upd := api.UpdateMachine{Labels: labelsOf(plan.Labels)}
	if plan.Role.ValueString() != api.MachineEdge { // an edge machine is never schedulable
		sched := plan.Schedulable.ValueBool()
		upd.Schedulable = &sched
	}
	m, err := r.c.UpdateMachineByName(ctx, plan.Name.ValueString(), upd)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update machine", err))
		return
	}
	plan.fill(m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *machineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state machineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveMachine(ctx, state.Name.ValueString(), false); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(apiDiag("Remove machine", err))
	}
}

// ImportState adopts a machine by name (TF-47); its join command isn't known.
func (r *machineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	m, err := r.c.GetMachineByName(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Import", err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), m.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), m.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("join_command"), "")...)
}

// ─── enclave_machines ────────────────────────────────────────────────────

type machinesData struct{ dataBase }

type machineItemModel struct {
	ID              types.String            `tfsdk:"id"`
	Name            types.String            `tfsdk:"name"`
	Main            types.Bool              `tfsdk:"main"`
	State           types.String            `tfsdk:"state"`
	Labels          map[string]types.String `tfsdk:"labels"`
	Schedulable     types.Bool              `tfsdk:"schedulable"`
	Address         types.String            `tfsdk:"address"`
	MemoryFreeBytes types.Int64             `tfsdk:"memory_free_bytes"`
	Resources       types.Int64             `tfsdk:"resources"`
}

type machinesModel struct {
	Machines []machineItemModel `tfsdk:"machines"`
}

func newMachinesData() datasource.DataSource { return &machinesData{} }

func (d *machinesData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machines"
}

func (d *machinesData) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req, resp)
}

func (d *machinesData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "Every machine: the main one first, with its state and free memory.",
		Attributes: map[string]dschema.Attribute{
			"machines": dschema.ListNestedAttribute{Computed: true, NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
				"id": dschema.StringAttribute{Computed: true}, "name": dschema.StringAttribute{Computed: true}, "main": dschema.BoolAttribute{Computed: true},
				"state": dschema.StringAttribute{Computed: true}, "labels": dschema.MapAttribute{Computed: true, ElementType: types.StringType},
				"schedulable": dschema.BoolAttribute{Computed: true}, "address": dschema.StringAttribute{Computed: true},
				"memory_free_bytes": dschema.Int64Attribute{Computed: true, Description: "Memory not reserved by what runs there."},
				"resources":         dschema.Int64Attribute{Computed: true, Description: "Resources that run there."},
			}}},
		},
	}
}

func (d *machinesData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	list, err := d.c.ListMachines(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("List machines", err))
		return
	}
	out := machinesModel{Machines: []machineItemModel{}}
	for _, m := range list {
		item := machineItemModel{ID: types.StringValue(m.ID), Name: types.StringValue(m.Name), Main: types.BoolValue(m.Main), State: types.StringValue(m.State),
			Labels: map[string]types.String{}, Schedulable: types.BoolValue(m.Schedulable), Address: types.StringNull(),
			MemoryFreeBytes: types.Int64Value(max(m.MemoryTotalBytes-m.MemoryReservedBytes, 0)), Resources: types.Int64Value(int64(m.Resources))}
		for k, v := range m.Labels {
			item.Labels[k] = types.StringValue(v)
		}
		if m.Address != nil {
			item.Address = types.StringValue(*m.Address)
		}
		out.Machines = append(out.Machines, item)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
