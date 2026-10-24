package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// Per-app bucket access (L2-003 TF-48, L2-011): bucket_access on enclave_app and enclave_job, and the
// name of the secret holding the keys.

type bucketGrantModel struct {
	Bucket types.String `tfsdk:"bucket"`
	Access types.String `tfsdk:"access"`
}

func bucketAccessAttr(what string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Optional: true,
		Description: "Buckets " + what + " reaches with keys of its own, given as AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION, " +
			"AWS_ENDPOINT_URL_S3 and AWS_ENDPOINT_URL. Use path-style addressing in S3 clients.",
		Validators: []validator.List{listvalidator.SizeAtMost(api.BucketAccessMax)},
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"bucket": schema.StringAttribute{Required: true, Description: "A bucket of the same project."},
			"access": schema.StringAttribute{Required: true, Description: "read (list and read objects) or readwrite (also write and delete them).",
				Validators: []validator.String{stringvalidator.OneOf(api.BucketAccessLevels...)}},
		}}}
}

// bucketCredentialsAttr is known at plan time: the secret's name while there is access, null without.
func bucketCredentialsAttr() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: "The secret holding the keys ({\"accessKey\", \"secretKey\"}), while there is bucket access.",
		PlanModifiers: []planmodifier.String{credentialsSecretPlan{}}}
}

type credentialsSecretPlan struct{}

func (credentialsSecretPlan) Description(context.Context) string {
	return "<name>-bucket-credentials while bucket_access has entries"
}

func (p credentialsSecretPlan) MarkdownDescription(ctx context.Context) string {
	return p.Description(ctx)
}

func (credentialsSecretPlan) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	var name types.String
	var grants types.List
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("bucket_access"), &grants)...)
	switch {
	case resp.Diagnostics.HasError(), name.IsUnknown(), grants.IsUnknown():
		return
	case grants.IsNull() || len(grants.Elements()) == 0:
		resp.PlanValue = types.StringNull()
	default:
		resp.PlanValue = types.StringValue(api.BucketCredentialsSecretFor(name.ValueString()))
	}
}

func grantsOf(ms []bucketGrantModel) []api.BucketGrant {
	var out []api.BucketGrant
	for _, m := range ms {
		out = append(out, api.BucketGrant{Bucket: m.Bucket.ValueString(), Access: m.Access.ValueString()})
	}
	return out
}

func grantModels(gs []api.BucketGrant) []bucketGrantModel {
	if len(gs) == 0 {
		return nil
	}
	out := make([]bucketGrantModel, len(gs))
	for i, g := range gs {
		out[i] = bucketGrantModel{Bucket: types.StringValue(g.Bucket), Access: types.StringValue(g.Access)}
	}
	return out
}

// nilIfNoGrants sends null, which clears the access, when the configuration has none.
func nilIfNoGrants(gs []api.BucketGrant) any {
	if len(gs) == 0 {
		return nil
	}
	return gs
}

// credentialsSecretOf reads the output the server sets while there is access.
func credentialsSecretOf(r api.Resource) types.String {
	if v, ok := r.Outputs[api.OutputBucketCredentialsSecret].(string); ok && v != "" {
		return types.StringValue(v)
	}
	return types.StringNull()
}
