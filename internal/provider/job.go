package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// ─── enclave_job (TF-13) ─────────────────────────────────────────────────

type jobModel struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Project       types.String   `tfsdk:"project"`
	Schedule      types.String   `tfsdk:"schedule"`
	TimeZone      types.String   `tfsdk:"time_zone"`
	App           types.String   `tfsdk:"app"`
	Image         types.String   `tfsdk:"image"`
	Command       types.List     `tfsdk:"command"`
	Env           types.Map      `tfsdk:"env"`
	SecretEnv     types.Map      `tfsdk:"secret_env"`
	Timeout       types.Int64    `tfsdk:"timeout_seconds"`
	Overlap       types.String   `tfsdk:"overlap"`
	MemoryMiB     types.Int64    `tfsdk:"memory_mib"`
	NextRunAt     types.String   `tfsdk:"next_run_at"`
	LastRunStatus types.String   `tfsdk:"last_run_status"`
	Network       types.String   `tfsdk:"network"`
	Subnet        types.String   `tfsdk:"subnet"`
	Volumes       []mountModel   `tfsdk:"volumes"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

type jobResource struct{ base }

func newJob() resource.Resource { return &jobResource{base{kind: api.KindJob}} }

func (r *jobResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_job"
}

func (r *jobResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *jobResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("app"), path.MatchRoot("image"))}
}

func (r *jobResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A command run on a schedule. Run history is not part of state; read it with the CLI or console.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("job"),
			"project": projectAttr(),
			"schedule": schema.StringAttribute{Required: true,
				Description: `5-field cron ("0 3 * * *") or @hourly, @daily, @weekly, @monthly.`},
			"time_zone": schema.StringAttribute{Optional: true, Description: "IANA time zone for the schedule (default: the machine's)."},
			"app":       schema.StringAttribute{Optional: true, Description: "Run with this app's image and environment. Exactly one of app or image."},
			"image":     schema.StringAttribute{Optional: true, Description: "Run this image. Exactly one of app or image."},
			"command":   schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command and arguments (default: the image's or app's)."},
			"env":       schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment variables; they override an app's."},
			"secret_env": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Environment variables from secrets: variable name → secret name."},
			"timeout_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.JobTimeoutDefault),
				Description: "Stop a run after this many seconds (1–86400).", Validators: []validator.Int64{int64validator.Between(api.JobTimeoutMin, api.JobTimeoutMax)}},
			"overlap": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.OverlapForbid),
				Description: "forbid: skip a run while one is going; allow: run anyway.", Validators: []validator.String{stringvalidator.OneOf(api.OverlapPolicies...)}},
			"memory_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.AppMemoryDefaultMiB),
				Description: "Memory limit in MiB (64–16384).", Validators: []validator.Int64{int64validator.Between(api.AppMemoryMinMiB, api.AppMemoryMaxMiB)}},
			"next_run_at":     schema.StringAttribute{Computed: true, Description: "When the next run is due (RFC 3339)."},
			"last_run_status": schema.StringAttribute{Computed: true, Description: "succeeded, failed, timed_out or skipped."},
			"network":         placementNetworkAttr(),
			"subnet":          placementSubnetAttr(),
			"volumes":         volumesAttr(),
			"timeouts":        timeoutsAttr(ctx),
		},
	}
}

func (m *jobModel) spec(ctx context.Context) (api.JobSpec, diag.Diagnostics) {
	var diags diag.Diagnostics
	s := api.JobSpec{Schedule: m.Schedule.ValueString(), TimeZone: m.TimeZone.ValueString(), App: m.App.ValueString(), Image: m.Image.ValueString(),
		TimeoutSeconds: int(m.Timeout.ValueInt64()), Overlap: m.Overlap.ValueString(), MemoryMiB: int(m.MemoryMiB.ValueInt64()),
		Network: m.Network.ValueString(), Subnet: m.Subnet.ValueString(), Volumes: mountsOf(m.Volumes)}
	if !m.Command.IsNull() {
		diags.Append(m.Command.ElementsAs(ctx, &s.Command, false)...)
	}
	if !m.Env.IsNull() {
		diags.Append(m.Env.ElementsAs(ctx, &s.Env, false)...)
	}
	if !m.SecretEnv.IsNull() {
		diags.Append(m.SecretEnv.ElementsAs(ctx, &s.SecretEnv, false)...)
	}
	return s, diags
}

func strOrNull(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func (m *jobModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.JobSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Schedule = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Schedule)
	m.TimeZone, m.App, m.Image = strOrNull(s.TimeZone), strOrNull(s.App), strOrNull(s.Image)
	m.Command = listOrNull(s.Command, m.Command)
	m.Env, m.SecretEnv = mapOrNull(s.Env, m.Env), mapOrNull(s.SecretEnv, m.SecretEnv)
	m.Timeout, m.Overlap, m.MemoryMiB = types.Int64Value(int64(s.TimeoutSeconds)), types.StringValue(s.Overlap), types.Int64Value(int64(s.MemoryMiB))
	m.NextRunAt, m.LastRunStatus = outString(r, "nextRunAt"), outString(r, "lastRunStatus")
	m.Network, m.Subnet = strOrNull(s.Network), strOrNull(s.Subnet)
	m.Volumes = mountModels(s.Volumes, m.Volumes)
}

func (r *jobResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan jobModel
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
		resp.Diagnostics.Append(apiDiag("Create job", err))
	}
}

func (r *jobResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state jobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read job", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *jobResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state jobModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	s, d := plan.spec(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The whole spec: present fields replace, null clears (L3-002 §2).
	patch := map[string]any{"schedule": s.Schedule, "timeZone": nilIfEmptyString(s.TimeZone), "app": nilIfEmptyString(s.App), "image": nilIfEmptyString(s.Image),
		"command": nilIfEmpty(s.Command), "env": nilIfEmptyMap(s.Env), "secretEnv": nilIfEmptyMap(s.SecretEnv),
		"timeoutSeconds": s.TimeoutSeconds, "overlap": s.Overlap, "memoryMiB": s.MemoryMiB,
		"network": nilIfEmptyString(s.Network), "subnet": nilIfEmptyString(s.Subnet), "volumes": nilIfNoMounts(s.Volumes)}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update job", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func nilIfEmptyString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *jobResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state jobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete job", err))
	}
}

func (r *jobResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}
