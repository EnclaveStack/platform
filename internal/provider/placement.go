package provider

import (
	"context"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// Placement in a network (L2-003 TF-16, ADR-020): network and subnet change in place.

func placementNetworkAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Description: "Network to place it in (default: the default network). Changes in place."}
}

func placementSubnetAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Description: "Subnet of the network (default: its first)."}
}

// placementPatch sends the network and subnet when either changed; null clears them.
func placementPatch(planNet, stateNet, planSub, stateSub types.String) map[string]any {
	patch := map[string]any{}
	if !planNet.Equal(stateNet) || !planSub.Equal(stateSub) {
		patch["network"], patch["subnet"] = nilIfEmptyString(planNet.ValueString()), nilIfEmptyString(planSub.ValueString())
	}
	return patch
}

// movedPlacement reports whether a plan changes the network, subnet or machine.
func movedPlacement(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	for _, attr := range []string{"network", "subnet", "machine"} {
		var p, s types.String
		if plan.GetAttribute(ctx, path.Root(attr), &p).HasError() || state.GetAttribute(ctx, path.Root(attr), &s).HasError() {
			return true
		}
		if p.IsUnknown() || !p.Equal(s) {
			return true
		}
	}
	return false
}

// placed keeps a computed value from state unless the placement changes, when it is unknown until
// applied: a network without internet publishes no host port.
type placed struct{}

func (placed) Description(context.Context) string {
	return "known after apply when the network or subnet changes"
}

func (placed) MarkdownDescription(ctx context.Context) string { return placed{}.Description(ctx) }

func (placed) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || movedPlacement(ctx, req.Plan, req.State) {
		return // created or moved: known after apply
	}
	resp.PlanValue = req.StateValue
}

func (placed) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || movedPlacement(ctx, req.Plan, req.State) {
		return
	}
	resp.PlanValue = req.StateValue
}

func placedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc, PlanModifiers: []planmodifier.String{placed{}}}
}

func placedInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: desc, PlanModifiers: []planmodifier.Int64{placed{}}}
}

// Placement on a machine (L2-003 TF-43, ADR-042): machine is optional and computed, so the machine
// EnclaveStack chose reads back without a diff; changing it moves the resource in place.

// placementModel is embedded in every placed kind's model.
type placementModel struct {
	Machine       types.String            `tfsdk:"machine"`
	MachineLabels map[string]types.String `tfsdk:"machine_labels"`
}

func (m *placementModel) machineOf() *placementModel { return m }

// machineAware is a model with a machine.
type machineAware interface{ machineOf() *placementModel }

func machineAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true,
		Description:   "The machine to run on (default: EnclaveStack chooses, and this reads back its choice). Changing it moves the resource, with its data, in place; apply waits for the move.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}

func machineLabelsAttr() schema.MapAttribute {
	return schema.MapAttribute{Optional: true, ElementType: types.StringType,
		Description: "Labels the machine must have, when machine isn't set (key=value, all must match)."}
}

func (m *placementModel) placed() api.Placed {
	p := api.Placed{}
	if !m.Machine.IsUnknown() {
		p.Machine = m.Machine.ValueString()
	}
	for k, v := range m.MachineLabels {
		if p.MachineLabels == nil {
			p.MachineLabels = map[string]string{}
		}
		p.MachineLabels[k] = v.ValueString()
	}
	return p
}

// placedFields is the placement as spec fields, for kinds whose spec is a map.
func (m *placementModel) placedFields() map[string]any {
	out := map[string]any{}
	p := m.placed()
	if p.Machine != "" {
		out["machine"] = p.Machine
	}
	if len(p.MachineLabels) > 0 {
		out["machineLabels"] = p.MachineLabels
	}
	return out
}

// fillMachine reads back where the resource runs and the labels it asks for.
func (m *placementModel) fillMachine(r api.Resource, spec api.Placed) {
	m.Machine = outString(r, "machine")
	if m.Machine.IsNull() && spec.Machine != "" {
		m.Machine = types.StringValue(spec.Machine)
	}
	if len(spec.MachineLabels) == 0 && len(m.MachineLabels) == 0 {
		return // keep an empty map as configured
	}
	m.MachineLabels = map[string]types.String{}
	for k, v := range spec.MachineLabels {
		m.MachineLabels[k] = types.StringValue(v)
	}
}

// machinePatch moves the resource when the planned machine differs, and changes its labels.
func (m *placementModel) machinePatch(state *placementModel) map[string]any {
	patch := map[string]any{}
	if !m.Machine.IsUnknown() && !m.Machine.IsNull() && !m.Machine.Equal(state.Machine) {
		patch["machine"] = m.Machine.ValueString()
	}
	plan, prior := m.placed().MachineLabels, state.placed().MachineLabels
	if !maps.Equal(plan, prior) {
		if len(plan) == 0 {
			patch["machineLabels"] = nil
		} else {
			patch["machineLabels"] = plan
		}
	}
	return patch
}
