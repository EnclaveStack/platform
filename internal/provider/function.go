package provider

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// ─── enclave_function (L2-016 TF-54, ADR-055) ─────────────────────────────

type triggerModel struct {
	Schedule types.String `tfsdk:"schedule"`
	TimeZone types.String `tfsdk:"time_zone"`
	Queue    types.String `tfsdk:"queue"`
	Bucket   types.String `tfsdk:"bucket"`
	Prefix   types.String `tfsdk:"prefix"`
}

type functionModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Image         types.String       `tfsdk:"image"`
	Command       types.List         `tfsdk:"command"`
	Env           types.Map          `tfsdk:"env"`
	SecretEnv     types.Map          `tfsdk:"secret_env"`
	MemoryMiB     types.Int64        `tfsdk:"memory_mib"`
	Timeout       types.Int64        `tfsdk:"timeout_seconds"`
	Concurrency   types.Int64        `tfsdk:"concurrency"`
	Triggers      []triggerModel     `tfsdk:"triggers"`
	Network       types.String       `tfsdk:"network"`
	Subnet        types.String       `tfsdk:"subnet"`
	BucketAccess  []bucketGrantModel `tfsdk:"bucket_access"`
	BucketCreds   types.String       `tfsdk:"bucket_credentials_secret"`
	NextRunAt     types.String       `tfsdk:"next_run_at"`
	LastRunStatus types.String       `tfsdk:"last_run_status"`
	Timeouts      timeouts.Value     `tfsdk:"timeouts"`
}

type functionResource struct{ base }

func newFunction() resource.Resource { return &functionResource{base{kind: api.KindFunction}} }

func (r *functionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_function"
}

func (r *functionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *functionResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A container image started once per event: a schedule, a queue's messages, new objects in a bucket folder, a topic's messages " +
			"or an invocation. The run reads the event from $ENCLAVE_EVENT_FILE. Run history is not part of state.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr(),
			"name":           nameAttr("function"),
			"project":        projectAttr(),
			"machine":        machineAttr(),
			"machine_labels": machineLabelsAttr(),
			"image":          schema.StringAttribute{Required: true, Description: "The container image, e.g. alpine:3.22."},
			"command":        schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command and arguments (default: the image's)."},
			"env":            schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment variables; names starting ENCLAVE_ are reserved."},
			"secret_env": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Environment variables from secrets: variable name → secret name."},
			"memory_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.FunctionMemoryDefaultMiB),
				Description: "Memory limit in MiB.", Validators: []validator.Int64{int64validator.Between(api.AppMemoryMinMiB, api.FunctionMemoryMaxMiB)}},
			"timeout_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.FunctionTimeoutDefault),
				Description: "Stop a run after this many seconds (1–900).", Validators: []validator.Int64{int64validator.Between(api.FunctionTimeoutMin, api.FunctionTimeoutMax)}},
			"concurrency": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.FunctionConcurrencyDef),
				Description: "Runs at the same time (1–20).", Validators: []validator.Int64{int64validator.Between(api.FunctionConcurrencyMin, api.FunctionConcurrencyMax)}},
			"triggers": schema.ListNestedAttribute{Optional: true, Description: "What starts runs besides invocations and topics: each is exactly one of schedule, queue or bucket.",
				Validators: []validator.List{listvalidator.SizeAtMost(api.FunctionTriggersMax)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"schedule":  schema.StringAttribute{Optional: true, Description: `5-field cron or @hourly, @daily, @weekly, @monthly. At most one schedule.`},
					"time_zone": schema.StringAttribute{Optional: true, Description: "IANA time zone for the schedule."},
					"queue":     schema.StringAttribute{Optional: true, Description: "A queue: one run per message, deleted when the run succeeds."},
					"bucket":    schema.StringAttribute{Optional: true, Description: "A bucket: one run per new object."},
					"prefix":    schema.StringAttribute{Optional: true, Description: "Only objects in this folder of the bucket, e.g. in/."},
				}}},
			"network":                   placementNetworkAttr(),
			"subnet":                    placementSubnetAttr(),
			"bucket_access":             bucketAccessAttr("the function"),
			"bucket_credentials_secret": bucketCredentialsAttr(),
			"next_run_at":               schema.StringAttribute{Computed: true, Description: "When the schedule next runs it (RFC 3339)."},
			"last_run_status":           schema.StringAttribute{Computed: true, Description: "succeeded, failed, timed_out or skipped."},
			"timeouts":                  timeoutsAttr(ctx),
		},
	}
}

func (m *functionModel) spec(ctx context.Context) (api.FunctionSpec, diag.Diagnostics) {
	var diags diag.Diagnostics
	s := api.FunctionSpec{Image: m.Image.ValueString(), MemoryMiB: int(m.MemoryMiB.ValueInt64()), TimeoutSeconds: int(m.Timeout.ValueInt64()),
		Concurrency: int(m.Concurrency.ValueInt64()), Network: m.Network.ValueString(), Subnet: m.Subnet.ValueString(), BucketAccess: grantsOf(m.BucketAccess),
		Placed: m.placed()}
	for _, t := range m.Triggers {
		s.Triggers = append(s.Triggers, api.FunctionTrigger{Schedule: t.Schedule.ValueString(), TimeZone: t.TimeZone.ValueString(), Queue: t.Queue.ValueString(),
			Bucket: t.Bucket.ValueString(), Prefix: t.Prefix.ValueString()})
	}
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

func (m *functionModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.FunctionSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Image = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Image)
	m.Command = listOrNull(s.Command, m.Command)
	m.Env, m.SecretEnv = mapOrNull(s.Env, m.Env), mapOrNull(s.SecretEnv, m.SecretEnv)
	m.MemoryMiB, m.Timeout, m.Concurrency = types.Int64Value(int64(s.MemoryMiB)), types.Int64Value(int64(s.TimeoutSeconds)), types.Int64Value(int64(s.Concurrency))
	m.Triggers = nil
	for _, t := range s.Triggers {
		m.Triggers = append(m.Triggers, triggerModel{Schedule: strOrNull(t.Schedule), TimeZone: strOrNull(t.TimeZone), Queue: strOrNull(t.Queue),
			Bucket: strOrNull(t.Bucket), Prefix: strOrNull(t.Prefix)})
	}
	m.Network, m.Subnet = strOrNull(s.Network), strOrNull(s.Subnet)
	m.BucketAccess, m.BucketCreds = grantModels(s.BucketAccess), credentialsSecretOf(r)
	m.NextRunAt, m.LastRunStatus = outString(r, "nextRunAt"), outString(r, "lastRunStatus")
}

func (r *functionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan functionModel
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
		resp.Diagnostics.Append(apiDiag("Create function", err))
	}
}

func (r *functionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state functionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read function", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *functionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state functionModel
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
	var triggers any
	if len(s.Triggers) > 0 {
		triggers = s.Triggers
	}
	patch := map[string]any{"image": s.Image, "command": nilIfEmpty(s.Command), "env": nilIfEmptyMap(s.Env), "secretEnv": nilIfEmptyMap(s.SecretEnv),
		"memoryMiB": s.MemoryMiB, "timeoutSeconds": s.TimeoutSeconds, "concurrency": s.Concurrency, "triggers": triggers,
		"network": nilIfEmptyString(s.Network), "subnet": nilIfEmptyString(s.Subnet), "bucketAccess": nilIfNoGrants(s.BucketAccess)}
	maps.Copy(patch, plan.machinePatch(&state.placementModel))
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update function", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *functionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state functionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete function", err))
	}
}

func (r *functionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}
