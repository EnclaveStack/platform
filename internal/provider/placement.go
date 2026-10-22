package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

// movedPlacement reports whether a plan changes the network or subnet.
func movedPlacement(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	for _, attr := range []string{"network", "subnet"} {
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
