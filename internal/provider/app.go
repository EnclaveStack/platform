package provider

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

type appModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Image        types.String       `tfsdk:"image"`
	Source       *sourceGitModel    `tfsdk:"source"`
	BuiltCommit  types.String       `tfsdk:"built_commit"`
	Port         types.Int64        `tfsdk:"port"`
	Command      types.List         `tfsdk:"command"`
	Env          types.Map          `tfsdk:"env"`
	SecretEnv    types.Map          `tfsdk:"secret_env"`
	Publish      types.Bool         `tfsdk:"publish"`
	MemoryMiB    types.Int64        `tfsdk:"memory_mib"`
	Host         types.String       `tfsdk:"host"`
	HostPort     types.Int64        `tfsdk:"host_port"`
	InternalHost types.String       `tfsdk:"internal_host"`
	InternalPort types.Int64        `tfsdk:"internal_port"`
	Strategy     types.String       `tfsdk:"strategy"`
	HealthCheck  *healthModel       `tfsdk:"health_check"`
	Serving      types.Int64        `tfsdk:"serving_revision"`
	Network      types.String       `tfsdk:"network"`
	Subnet       types.String       `tfsdk:"subnet"`
	Replicas     types.Int64        `tfsdk:"replicas"`
	PrivateIP    types.String       `tfsdk:"private_ip"`
	PrivateDNS   types.String       `tfsdk:"private_dns"`
	Volumes      []mountModel       `tfsdk:"volumes"`
	BucketAccess []bucketGrantModel `tfsdk:"bucket_access"`
	BucketCreds  types.String       `tfsdk:"bucket_credentials_secret"`
	Timeouts     timeouts.Value     `tfsdk:"timeouts"`
}

// healthModel mirrors api.HealthCheck. Fields the user leaves unset stay null in state; the server's
// defaults are not echoed back, so plans stay quiet (TF-12).
type healthModel struct {
	Type        types.String `tfsdk:"type"`
	Path        types.String `tfsdk:"path"`
	Port        types.Int64  `tfsdk:"port"`
	Command     types.List   `tfsdk:"command"`
	Interval    types.Int64  `tfsdk:"interval_seconds"`
	Timeout     types.Int64  `tfsdk:"timeout_seconds"`
	Retries     types.Int64  `tfsdk:"retries"`
	StartPeriod types.Int64  `tfsdk:"start_period_seconds"`
}

// sourceGitModel mirrors api.GitSource (TF-15). dockerfile and context stay null when unset, like the
// health check's fields, so the server's defaults never show as drift.
type sourceGitModel struct {
	Git               types.String `tfsdk:"git"`
	Ref               types.String `tfsdk:"ref"`
	Dockerfile        types.String `tfsdk:"dockerfile"`
	Context           types.String `tfsdk:"context"`
	CredentialsSecret types.String `tfsdk:"credentials_secret"`
	BuildArgs         types.Map    `tfsdk:"build_args"`
}

type appResource struct{ base }

func (r *appResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("image"), path.MatchRoot("source"))}
}

func newApp() resource.Resource { return &appResource{base{kind: api.KindApp}} }

func (r *appResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *appResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *appResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A long-running container. Every field except name changes in place (the container is recreated).",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr(),
			"name":           nameAttr("app"),
			"project":        projectAttr(),
			"machine":        machineAttr(),
			"machine_labels": machineLabelsAttr(),
			"image":          schema.StringAttribute{Optional: true, Description: "Container image, e.g. nginx:1.27-alpine. Exactly one of image or source."},
			"source": schema.SingleNestedAttribute{Optional: true,
				Description: "Build the app from a Git repository and its Dockerfile (BuildKit). Exactly one of image or source. A redeploy builds the ref's latest commit.",
				Attributes: map[string]schema.Attribute{
					"git":                schema.StringAttribute{Required: true, Description: "Repository URL: https:// or ssh://."},
					"ref":                schema.StringAttribute{Required: true, Description: "Branch, tag or commit."},
					"dockerfile":         schema.StringAttribute{Optional: true, Description: "Dockerfile path relative to the context (default Dockerfile)."},
					"context":            schema.StringAttribute{Optional: true, Description: "Build context directory in the repository (default .)."},
					"credentials_secret": schema.StringAttribute{Optional: true, Description: `Secret holding {"token"} (https) or {"privateKey","knownHosts"} (ssh).`},
					"build_args":         schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Build arguments. Not for secrets: they end up in image metadata."},
				}},
			"built_commit": schema.StringAttribute{Computed: true, Description: "The commit the serving revision was built from (source apps)."},
			"port": schema.Int64Attribute{Optional: true, Description: "Port the app listens on.",
				Validators: []validator.Int64{int64validator.Between(api.PortMin, api.PortMax)}},
			"command":    schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Replaces the image's command."},
			"env":        schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Plain environment variables."},
			"secret_env": schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment variables taken from secrets: variable name → secret name."},
			"publish": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Publish the port on the host (requires port)."},
			"memory_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.AppMemoryDefaultMiB),
				Description: "Memory limit in MiB (64–16384).", Validators: []validator.Int64{int64validator.Between(api.AppMemoryMinMiB, api.AppMemoryMaxMiB)}},
			"host":          schema.StringAttribute{Computed: true, Description: "Host to reach a published app from this machine."},
			"host_port":     schema.Int64Attribute{Computed: true, Description: "Port to reach a published app from this machine."},
			"internal_host": stableString("Host other apps use on the EnclaveStack network."),
			"internal_port": schema.Int64Attribute{Computed: true, Description: "Port other apps use on the EnclaveStack network."},
			"strategy": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.StrategyRolling),
				Description: "rolling: a new revision takes traffic only once healthy; recreate: stop, then start. Published apps always recreate.",
				Validators:  []validator.String{stringvalidator.OneOf(api.Strategies...)}},
			"health_check": schema.SingleNestedAttribute{Optional: true,
				Description: "When a new revision may take traffic. Unset: tcp on the port, or 5 s running without restarts.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{Required: true, Description: "http, tcp, command or none.",
						Validators: []validator.String{stringvalidator.OneOf(api.HealthCheckTypes...)}},
					"path":    schema.StringAttribute{Optional: true, Description: "HTTP path (default /)."},
					"port":    schema.Int64Attribute{Optional: true, Description: "Port to check (default: the app's port)."},
					"command": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command run inside the container (command checks)."},
					"interval_seconds": schema.Int64Attribute{Optional: true, Description: "Seconds between checks (default 5).",
						Validators: []validator.Int64{int64validator.Between(api.HealthIntervalMin, api.HealthIntervalMax)}},
					"timeout_seconds": schema.Int64Attribute{Optional: true, Description: "Seconds before one check gives up (default 3).",
						Validators: []validator.Int64{int64validator.Between(api.HealthTimeoutMin, api.HealthTimeoutMax)}},
					"retries": schema.Int64Attribute{Optional: true, Description: "Checks before a new revision is discarded (default 12).",
						Validators: []validator.Int64{int64validator.Between(api.HealthRetriesMin, api.HealthRetriesMax)}},
					"start_period_seconds": schema.Int64Attribute{Optional: true, Description: "Seconds to wait before the first check.",
						Validators: []validator.Int64{int64validator.Between(0, api.HealthStartPeriodMax)}},
				}},
			"serving_revision": schema.Int64Attribute{Computed: true, Description: "The revision receiving traffic."},
			"replicas": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.ReplicasDefault),
				Description: "Containers per revision (1–10); a published app runs one.", Validators: []validator.Int64{int64validator.Between(api.ReplicasMin, api.ReplicasMax)}},
			"network":                   placementNetworkAttr(),
			"subnet":                    placementSubnetAttr(),
			"private_ip":                schema.StringAttribute{Computed: true, Description: "The serving revision's address in its network."},
			"private_dns":               stableString("Name the app answers to inside its network: <name>.app.internal."),
			"volumes":                   volumesAttr(),
			"bucket_access":             bucketAccessAttr("the app"),
			"bucket_credentials_secret": bucketCredentialsAttr(),
			"timeouts":                  timeoutsAttr(ctx),
		},
	}
}

func (m *appModel) spec(ctx context.Context) (api.AppSpec, diag.Diagnostics) {
	var diags diag.Diagnostics
	s := api.AppSpec{Image: m.Image.ValueString(), Port: int(m.Port.ValueInt64()), Publish: m.Publish.ValueBool(), MemoryMiB: int(m.MemoryMiB.ValueInt64()),
		Network: m.Network.ValueString(), Subnet: m.Subnet.ValueString(), Replicas: int(m.Replicas.ValueInt64()), Volumes: mountsOf(m.Volumes), BucketAccess: grantsOf(m.BucketAccess), Placed: m.placed()}
	if g := m.Source; g != nil {
		s.Source = &api.GitSource{Git: g.Git.ValueString(), Ref: g.Ref.ValueString(), Dockerfile: g.Dockerfile.ValueString(), Context: g.Context.ValueString(),
			CredentialsSecret: g.CredentialsSecret.ValueString()}
		if !g.BuildArgs.IsNull() {
			diags.Append(g.BuildArgs.ElementsAs(ctx, &s.Source.BuildArgs, false)...)
		}
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
	s.Strategy = m.Strategy.ValueString()
	if h := m.HealthCheck; h != nil {
		s.HealthCheck = &api.HealthCheck{Type: h.Type.ValueString(), Path: h.Path.ValueString(), Port: int(h.Port.ValueInt64()),
			IntervalSeconds: int(h.Interval.ValueInt64()), TimeoutSeconds: int(h.Timeout.ValueInt64()),
			Retries: int(h.Retries.ValueInt64()), StartPeriodSeconds: int(h.StartPeriod.ValueInt64())}
		if !h.Command.IsNull() {
			diags.Append(h.Command.ElementsAs(ctx, &s.HealthCheck.Command, false)...)
		}
	}
	return s, diags
}

// fillHealth copies the server's check. A field null in the prior state (or config) stays null, so the
// server's defaults never show as drift; with no prior state (import) only non-zero values appear.
func fillHealth(h *api.HealthCheck, prior *healthModel) *healthModel {
	if h == nil {
		return nil
	}
	imported := prior == nil
	if imported {
		prior = &healthModel{}
	}
	str := func(v string, p types.String) types.String {
		if (imported && v == "") || (!imported && p.IsNull()) {
			return types.StringNull()
		}
		return types.StringValue(v)
	}
	num := func(v int, p types.Int64) types.Int64 {
		if (imported && v == 0) || (!imported && p.IsNull()) {
			return types.Int64Null()
		}
		return types.Int64Value(int64(v))
	}
	cmd := types.ListNull(types.StringType)
	if !(imported && len(h.Command) == 0) && !(!imported && prior.Command.IsNull()) {
		cmd = listOrNull(h.Command, types.ListValueMust(types.StringType, nil))
	}
	return &healthModel{Type: types.StringValue(h.Type), Path: str(h.Path, prior.Path), Port: num(h.Port, prior.Port), Command: cmd,
		Interval: num(h.IntervalSeconds, prior.Interval), Timeout: num(h.TimeoutSeconds, prior.Timeout),
		Retries: num(h.Retries, prior.Retries), StartPeriod: num(h.StartPeriodSeconds, prior.StartPeriod)}
}

// fillSource copies the server's Git source; defaults the user didn't write stay null.
func fillSource(g *api.GitSource, prior *sourceGitModel) *sourceGitModel {
	if g == nil {
		return nil
	}
	keep := func(v, def string, p *types.String) types.String {
		if v == def && (p == nil || p.IsNull()) {
			return types.StringNull()
		}
		return types.StringValue(v)
	}
	var pd, pc *types.String
	priorArgs := types.MapNull(types.StringType)
	if prior != nil {
		pd, pc, priorArgs = &prior.Dockerfile, &prior.Context, prior.BuildArgs
	}
	return &sourceGitModel{Git: types.StringValue(g.Git), Ref: types.StringValue(g.Ref), Dockerfile: keep(g.Dockerfile, api.GitDockerfileDefault, pd),
		Context: keep(g.Context, api.GitContextDefault, pc), CredentialsSecret: strOrNull(g.CredentialsSecret), BuildArgs: mapOrNull(g.BuildArgs, priorArgs)}
}

// fill copies server state into the model. Optional collections the user left unset stay null so
// plans don't show spurious diffs between "unset" and "empty".
func (m *appModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.AppSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Image = types.StringValue(r.ID), types.StringValue(r.Name), strOrNull(s.Image)
	m.Source = fillSource(s.Source, m.Source)
	m.BuiltCommit = outString(r, "builtCommit")
	m.Port = types.Int64Null()
	if s.Port > 0 {
		m.Port = types.Int64Value(int64(s.Port))
	}
	m.Publish, m.MemoryMiB = types.BoolValue(s.Publish), types.Int64Value(int64(s.MemoryMiB))
	m.Command = listOrNull(s.Command, m.Command)
	m.Env = mapOrNull(s.Env, m.Env)
	m.SecretEnv = mapOrNull(s.SecretEnv, m.SecretEnv)
	m.Host, m.HostPort = outString(r, "host"), outInt(r, "port")
	m.InternalHost, m.InternalPort = outString(r, "internalHost"), outInt(r, "internalPort")
	m.Strategy = types.StringValue(s.Strategy)
	if s.Strategy == "" {
		m.Strategy = types.StringValue(api.StrategyRolling)
	}
	m.HealthCheck = fillHealth(s.HealthCheck, m.HealthCheck)
	m.Serving = outInt(r, "servingRevision")
	m.Network, m.Subnet = strOrNull(s.Network), strOrNull(s.Subnet)
	m.Replicas = types.Int64Value(int64(max(s.Replicas, 1)))
	m.PrivateIP, m.PrivateDNS = outString(r, "privateIp"), outString(r, "privateDns")
	m.Volumes = mountModels(s.Volumes, m.Volumes)
	m.BucketAccess, m.BucketCreds = grantModels(s.BucketAccess), credentialsSecretOf(r)
}

func listOrNull(v []string, prior types.List) types.List {
	if len(v) == 0 && (prior.IsNull() || prior.IsUnknown()) {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, len(v))
	for i, s := range v {
		elems[i] = types.StringValue(s)
	}
	return types.ListValueMust(types.StringType, elems)
}

func mapOrNull(v map[string]string, prior types.Map) types.Map {
	if len(v) == 0 && (prior.IsNull() || prior.IsUnknown()) {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(v))
	for k, s := range v {
		elems[k] = types.StringValue(s)
	}
	return types.MapValueMust(types.StringType, elems)
}

func (r *appResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan appModel
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
		resp.Diagnostics.Append(apiDiag("Create app", err))
	}
}

func (r *appResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read app", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	spec, d := plan.spec(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Send the whole spec: the API replaces present fields and clears the ones set to null.
	patch := map[string]any{"image": nilIfEmptyString(spec.Image), "source": spec.Source, "publish": spec.Publish, "memoryMiB": spec.MemoryMiB,
		"port": nilIfZero(spec.Port), "command": nilIfEmpty(spec.Command), "env": nilIfEmptyMap(spec.Env), "secretEnv": nilIfEmptyMap(spec.SecretEnv),
		"strategy": spec.Strategy, "healthCheck": spec.HealthCheck, // a nil check encodes as null: cleared
		"network": nilIfEmptyString(spec.Network), "subnet": nilIfEmptyString(spec.Subnet), "replicas": spec.Replicas, "volumes": nilIfNoMounts(spec.Volumes),
		"bucketAccess": nilIfNoGrants(spec.BucketAccess)}
	maps.Copy(patch, plan.machinePatch(&state.placementModel))
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update app", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nilIfEmpty(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

func nilIfEmptyMap(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (r *appResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete app", err))
	}
}

func (r *appResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}
