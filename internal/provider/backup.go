package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// ─── enclave_backup (TF-14) ──────────────────────────────────────────────
//
// Restores are deliberately not part of the provider: they are one-off operations, not desired state
// (ADR-018). Use the CLI or console.

type s3Model struct {
	Endpoint          types.String `tfsdk:"endpoint"`
	Region            types.String `tfsdk:"region"`
	Bucket            types.String `tfsdk:"bucket"`
	CredentialsSecret types.String `tfsdk:"credentials_secret"`
}

type sourceModel struct {
	Database types.String `tfsdk:"database"`
	Cache    types.String `tfsdk:"cache"`
	Volume   types.String `tfsdk:"volume"`
}

type backupModel struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Project       types.String   `tfsdk:"project"`
	Source        *sourceModel   `tfsdk:"source"`
	Bucket        types.String   `tfsdk:"bucket"`
	S3            *s3Model       `tfsdk:"s3"`
	Prefix        types.String   `tfsdk:"prefix"`
	Schedule      types.String   `tfsdk:"schedule"`
	TimeZone      types.String   `tfsdk:"time_zone"`
	Keep          types.Int64    `tfsdk:"keep"`
	KeepDays      types.Int64    `tfsdk:"keep_days"`
	Timeout       types.Int64    `tfsdk:"timeout_seconds"`
	NextRunAt     types.String   `tfsdk:"next_run_at"`
	LastRunStatus types.String   `tfsdk:"last_run_status"`
	LastSuccessAt types.String   `tfsdk:"last_success_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

type backupResource struct{ base }

func newBackup() resource.Resource { return &backupResource{base{kind: api.KindBackup}} }

func (r *backupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup"
}

func (r *backupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *backupResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("source").AtName("database"), path.MatchRoot("source").AtName("cache"), path.MatchRoot("source").AtName("volume")),
		resourcevalidator.ExactlyOneOf(path.MatchRoot("bucket"), path.MatchRoot("s3")),
	}
}

func (r *backupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Scheduled backups of a database, cache or filesystem volume to a bucket or external S3. Run history and restores are not part of state; use the CLI or console.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("backup"),
			"project": projectAttr(),
			"source": schema.SingleNestedAttribute{Required: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Description: "What to back up: { database = … }, { cache = … } or { volume = … }. Changing it replaces the backup.",
				Attributes: map[string]schema.Attribute{
					"database": schema.StringAttribute{Optional: true, Description: "A database. Exactly one of database, cache or volume."},
					"cache":    schema.StringAttribute{Optional: true, Description: "A cache. Exactly one of database, cache or volume."},
					"volume":   schema.StringAttribute{Optional: true, Description: "A filesystem volume (disks use snapshots). Exactly one of database, cache or volume."},
				}},
			"bucket": schema.StringAttribute{Optional: true, Description: "Store backups in this EnclaveStack bucket. Exactly one of bucket or s3."},
			"s3": schema.SingleNestedAttribute{Optional: true, Description: "Store backups in external S3. Exactly one of bucket or s3.",
				Attributes: map[string]schema.Attribute{
					"endpoint":           schema.StringAttribute{Required: true, Description: "https endpoint, e.g. https://s3.eu-central-1.amazonaws.com."},
					"region":             schema.StringAttribute{Optional: true, Description: "Region, when the endpoint needs one."},
					"bucket":             schema.StringAttribute{Required: true, Description: "Bucket name."},
					"credentials_secret": schema.StringAttribute{Required: true, Description: `Secret holding {"accessKey","secretKey"}.`},
				}},
			"prefix": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.BackupPrefixDefault),
				Description: "Object key prefix."},
			"schedule": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.BackupScheduleDefault),
				Description: `5-field cron or @hourly, @daily, @weekly, @monthly.`},
			"time_zone": schema.StringAttribute{Optional: true, Description: "IANA time zone for the schedule (default: the machine's)."},
			"keep": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.BackupKeepDefault),
				Description: "Keep this many backups (1–365).", Validators: []validator.Int64{int64validator.Between(api.BackupKeepMin, api.BackupKeepMax)}},
			"keep_days": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				Description: "Also delete backups older than this many days (0: no age limit). The newest is always kept.",
				Validators:  []validator.Int64{int64validator.Between(0, api.BackupKeepDaysMax)}},
			"timeout_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.BackupTimeoutDefault),
				Description: "Stop a backup after this many seconds (60–86400).", Validators: []validator.Int64{int64validator.Between(api.BackupTimeoutMin, api.BackupTimeoutMax)}},
			"next_run_at":     schema.StringAttribute{Computed: true, Description: "When the next backup is due (RFC 3339)."},
			"last_run_status": schema.StringAttribute{Computed: true, Description: "succeeded, failed, timed_out or skipped."},
			"last_success_at": schema.StringAttribute{Computed: true, Description: "When the last backup succeeded (RFC 3339)."},
			"timeouts":        timeoutsAttr(ctx),
		},
	}
}

func (m *backupModel) spec() api.BackupSpec {
	s := api.BackupSpec{Destination: m.destination(), Prefix: m.Prefix.ValueString(), Schedule: m.Schedule.ValueString(), TimeZone: m.TimeZone.ValueString(),
		Keep: int(m.Keep.ValueInt64()), KeepDays: int(m.KeepDays.ValueInt64()), TimeoutSeconds: int(m.Timeout.ValueInt64())}
	if m.Source != nil {
		s.Source = api.BackupSource{Database: m.Source.Database.ValueString(), Cache: m.Source.Cache.ValueString(), Volume: m.Source.Volume.ValueString()}
	}
	return s
}

func (m *backupModel) destination() api.BackupDestination {
	if m.S3 != nil {
		return api.BackupDestination{S3: &api.S3Destination{Endpoint: m.S3.Endpoint.ValueString(), Region: m.S3.Region.ValueString(),
			Bucket: m.S3.Bucket.ValueString(), CredentialsSecret: m.S3.CredentialsSecret.ValueString()}}
	}
	return api.BackupDestination{Bucket: m.Bucket.ValueString()}
}

func (m *backupModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.BackupSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.Source = &sourceModel{Database: strOrNull(s.Source.Database), Cache: strOrNull(s.Source.Cache), Volume: strOrNull(s.Source.Volume)}
	m.Bucket, m.S3 = strOrNull(s.Destination.Bucket), nil
	if d := s.Destination.S3; d != nil {
		m.S3 = &s3Model{Endpoint: types.StringValue(d.Endpoint), Region: strOrNull(d.Region), Bucket: types.StringValue(d.Bucket),
			CredentialsSecret: types.StringValue(d.CredentialsSecret)}
	}
	m.Prefix, m.Schedule, m.TimeZone = types.StringValue(s.Prefix), types.StringValue(s.Schedule), strOrNull(s.TimeZone)
	m.Keep, m.KeepDays, m.Timeout = types.Int64Value(int64(s.Keep)), types.Int64Value(int64(s.KeepDays)), types.Int64Value(int64(s.TimeoutSeconds))
	m.NextRunAt, m.LastRunStatus, m.LastSuccessAt = outString(r, "nextRunAt"), outString(r, "lastRunStatus"), outString(r, "lastSuccessAt")
}

func (r *backupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan backupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), plan.spec(), timeout)
	if res.ID != "" {
		plan.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create backup", err))
	}
}

func (r *backupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state backupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read backup", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *backupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state backupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Everything but the source, which can't change (a source change replaces the resource).
	s := plan.spec()
	patch := map[string]any{"destination": s.Destination, "prefix": s.Prefix, "schedule": s.Schedule, "timeZone": nilIfEmptyString(s.TimeZone),
		"keep": s.Keep, "keepDays": s.KeepDays, "timeoutSeconds": s.TimeoutSeconds}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update backup", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete backup", err))
	}
}

func (r *backupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}
