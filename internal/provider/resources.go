package provider

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// Stable computed outputs keep their prior value in plans.
func stableString(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}

func stableInt(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: desc, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}}
}

// ─── enclave_database ────────────────────────────────────────────────────

type databaseModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Engine         types.String `tfsdk:"engine"`
	Version        types.String `tfsdk:"version"`
	NativePort     types.Int64  `tfsdk:"engine_port_native"`
	Host           types.String `tfsdk:"host"`
	Port           types.Int64  `tfsdk:"port"`
	InternalHost   types.String `tfsdk:"internal_host"`
	InternalPort   types.Int64  `tfsdk:"internal_port"`
	DatabaseName   types.String `tfsdk:"database_name"`
	Username       types.String `tfsdk:"username"`
	PasswordSecret types.String `tfsdk:"password_secret"`
	Network        types.String `tfsdk:"network"`
	Subnet         types.String `tfsdk:"subnet"`
	PrivateIP      types.String `tfsdk:"private_ip"`
	PrivateDNS     types.String `tfsdk:"private_dns"`
	// v0.10 (L2-016 TF-56, ADR-058)
	TLS                  types.Bool     `tfsdk:"tls"`
	Exposed              types.Bool     `tfsdk:"exposed"`
	InitScript           types.String   `tfsdk:"init_script"`
	CACertificate        types.String   `tfsdk:"ca_certificate"`
	CertificateExpiresAt types.String   `tfsdk:"certificate_expires_at"`
	ExposedAddresses     types.List     `tfsdk:"exposed_addresses"`
	Timeouts             timeouts.Value `tfsdk:"timeouts"`
}

type databaseResource struct{ base }

func newDatabase() resource.Resource { return &databaseResource{base{kind: api.KindDatabase}} }

func (r *databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *databaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *databaseResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A database: PostgreSQL, MySQL, MongoDB or ClickHouse. Its owner password lives in the managed secret named by password_secret; read it with the enclave_secret_value data source.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr(),
			"name":           nameAttr("database"),
			"project":        projectAttr(),
			"machine":        machineAttr(),
			"machine_labels": machineLabelsAttr(),
			"engine": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.DatabaseEngine),
				Description:   "postgres (default), mysql, mongodb or clickhouse. Changing it replaces the database (and its data).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.OneOf(api.DatabaseEngines...)}},
			"version": schema.StringAttribute{Optional: true, Computed: true,
				Description:   "The engine's version (default the engine's own: postgres 16, mysql 8.4, mongodb 7.0, clickhouse 26.3). Changing it replaces the database (and its data).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}},
			"engine_port_native": schema.Int64Attribute{Computed: true, Description: "ClickHouse's native-protocol port on this machine (port is its HTTP interface)."},
			"host":               placedString("Host to connect to from this machine (none in a network without internet)."),
			"port":               placedInt("Port to connect to from this machine (none in a network without internet)."),
			"internal_host":      stableString("Host apps use on the EnclaveStack network."),
			"internal_port":      stableInt("Port apps use on the EnclaveStack network."),
			"database_name":      stableString("Database name."),
			"username":           stableString("Owner user name."),
			"password_secret":    stableString("Name of the managed secret holding the owner password."),
			"network":            placementNetworkAttr(),
			"subnet":             placementSubnetAttr(),
			"private_ip":         schema.StringAttribute{Computed: true, Description: "Address in its network."},
			"private_dns":        stableString("Name it answers to inside its network: <name>.database.internal."),
			"tls": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Encrypted connections only, with a certificate from EnclaveStack's database CA (PostgreSQL and MySQL). Changing it restarts the database."},
			"exposed": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Reachable from other computers on the machine's addresses; needs tls."},
			"init_script": schema.StringAttribute{Optional: true, Description: "SQL (MongoDB: JavaScript) run once when the database is first made. Changing it replaces the database (and its data).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"ca_certificate":         schema.StringAttribute{Computed: true, Description: "The database CA's certificate (PEM), to verify the server with (with tls)."},
			"certificate_expires_at": schema.StringAttribute{Computed: true, Description: "When the server certificate ends (RFC 3339); it is renewed 30 days before."},
			"exposed_addresses":      schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "host:port addresses other computers connect to (with exposed)."},
			"timeouts":               timeoutsAttr(ctx),
		},
	}
}

func (m *databaseModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var spec api.DatabaseSpec
	_ = json.Unmarshal(r.Spec, &spec)
	m.fillMachine(r, spec.Placed)
	m.ID, m.Name, m.Engine, m.Version = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(spec.Engine), types.StringValue(spec.Version)
	m.Host, m.Port, m.NativePort = outString(r, "host"), outInt(r, "port"), outInt(r, "nativePort")
	m.InternalHost, m.InternalPort = outString(r, "internalHost"), outInt(r, "internalPort")
	m.DatabaseName, m.Username, m.PasswordSecret = outString(r, "database"), outString(r, "username"), outString(r, "passwordSecret")
	m.Network, m.Subnet = strOrNull(spec.Network), strOrNull(spec.Subnet)
	m.PrivateIP, m.PrivateDNS = outString(r, "privateIp"), outString(r, "privateDns")
	m.TLS, m.Exposed, m.InitScript = types.BoolValue(spec.TLS), types.BoolValue(spec.Exposed), strOrNull(spec.InitScript)
	m.CACertificate, m.CertificateExpiresAt = outString(r, "caCertificate"), outString(r, "certificateExpiresAt")
	var addrs []string
	if raw, ok := r.Outputs["exposedAddresses"].([]any); ok {
		for _, a := range raw {
			if s, ok := a.(string); ok {
				addrs = append(addrs, s)
			}
		}
	}
	m.ExposedAddresses, _ = types.ListValueFrom(context.Background(), types.StringType, addrs)
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), api.DatabaseSpec{Engine: plan.Engine.ValueString(), Version: plan.Version.ValueString(),
		Network: plan.Network.ValueString(), Subnet: plan.Subnet.ValueString(), Placed: plan.placed(),
		TLS: plan.TLS.ValueBool(), Exposed: plan.Exposed.ValueBool(), InitScript: plan.InitScript.ValueString()}, timeout)
	if res.ID != "" {
		plan.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...) // keep a failed resource in state so it can be fixed or destroyed
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create database", err))
	}
}

func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read database", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update moves the database to another network or subnet (its data is kept); the version forces
// replacement.
func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := placementPatch(plan.Network, state.Network, plan.Subnet, state.Subnet)
	maps.Copy(patch, plan.machinePatch(&state.placementModel))
	if !plan.TLS.Equal(state.TLS) {
		patch["tls"] = plan.TLS.ValueBool()
	}
	if !plan.Exposed.Equal(state.Exposed) {
		patch["exposed"] = plan.Exposed.ValueBool()
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update database", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete database", err))
	}
}

func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// ─── enclave_cache ───────────────────────────────────────────────────────

type cacheModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	MaxMemoryMiB types.Int64    `tfsdk:"max_memory_mib"`
	Host         types.String   `tfsdk:"host"`
	Port         types.Int64    `tfsdk:"port"`
	InternalHost types.String   `tfsdk:"internal_host"`
	InternalPort types.Int64    `tfsdk:"internal_port"`
	Network      types.String   `tfsdk:"network"`
	Subnet       types.String   `tfsdk:"subnet"`
	PrivateIP    types.String   `tfsdk:"private_ip"`
	PrivateDNS   types.String   `tfsdk:"private_dns"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

type cacheResource struct{ base }

func newCache() resource.Resource { return &cacheResource{base{kind: api.KindCache}} }

func (r *cacheResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cache"
}

func (r *cacheResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *cacheResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Redis cache, persisted to disk.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr(),
			"name":           nameAttr("cache"),
			"project":        projectAttr(),
			"machine":        machineAttr(),
			"machine_labels": machineLabelsAttr(),
			"max_memory_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.CacheMemoryDefault),
				Description: "Maximum memory in MiB (64–4096). Changes in place.",
				Validators:  []validator.Int64{int64validator.Between(api.CacheMemoryMinMiB, api.CacheMemoryMaxMiB)}},
			"host":          placedString("Host to connect to from this machine (none in a network without internet)."),
			"port":          placedInt("Port to connect to from this machine (none in a network without internet)."),
			"internal_host": stableString("Host apps use on the EnclaveStack network."),
			"internal_port": stableInt("Port apps use on the EnclaveStack network."),
			"network":       placementNetworkAttr(),
			"subnet":        placementSubnetAttr(),
			"private_ip":    schema.StringAttribute{Computed: true, Description: "Address in its network."},
			"private_dns":   stableString("Name it answers to inside its network: <name>.cache.internal."),
			"timeouts":      timeoutsAttr(ctx),
		},
	}
}

func (m *cacheModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var spec api.CacheSpec
	_ = json.Unmarshal(r.Spec, &spec)
	m.fillMachine(r, spec.Placed)
	m.ID, m.Name, m.MaxMemoryMiB = types.StringValue(r.ID), types.StringValue(r.Name), types.Int64Value(int64(spec.MaxMemoryMiB))
	m.Host, m.Port = outString(r, "host"), outInt(r, "port")
	m.InternalHost, m.InternalPort = outString(r, "internalHost"), outInt(r, "internalPort")
	m.Network, m.Subnet = strOrNull(spec.Network), strOrNull(spec.Subnet)
	m.PrivateIP, m.PrivateDNS = outString(r, "privateIp"), outString(r, "privateDns")
}

func (r *cacheResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan cacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), api.CacheSpec{Engine: api.CacheEngine, Version: api.CacheVersion, MaxMemoryMiB: int(plan.MaxMemoryMiB.ValueInt64()),
		Network: plan.Network.ValueString(), Subnet: plan.Subnet.ValueString(), Placed: plan.placed()}, timeout)
	if res.ID != "" {
		plan.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create cache", err))
	}
}

func (r *cacheResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state cacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read cache", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *cacheResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state cacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := placementPatch(plan.Network, state.Network, plan.Subnet, state.Subnet)
	maps.Copy(patch, plan.machinePatch(&state.placementModel))
	if !plan.MaxMemoryMiB.Equal(state.MaxMemoryMiB) {
		patch["maxMemoryMiB"] = plan.MaxMemoryMiB.ValueInt64()
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update cache", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cacheResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state cacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete cache", err))
	}
}

func (r *cacheResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// ─── enclave_bucket ──────────────────────────────────────────────────────

type bucketModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Project           types.String   `tfsdk:"project"`
	Versioning        types.Bool     `tfsdk:"versioning"`
	Endpoint          types.String   `tfsdk:"endpoint"`
	InternalEndpoint  types.String   `tfsdk:"internal_endpoint"`
	CredentialsSecret types.String   `tfsdk:"credentials_secret"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

type bucketResource struct{ base }

func newBucket() resource.Resource { return &bucketResource{base{kind: api.KindBucket}} }

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *bucketResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A bucket on the machine's S3-compatible object store.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("bucket"),
			"project": projectAttr(),
			"versioning": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Keep previous versions of objects. Changes in place."},
			"endpoint":           stableString("S3 endpoint reachable from this machine."),
			"internal_endpoint":  stableString("S3 endpoint apps use on the EnclaveStack network."),
			"credentials_secret": stableString("Name of the secret holding the object store's access and secret keys (JSON)."),
			"timeouts":           timeoutsAttr(ctx),
		},
	}
}

func (m *bucketModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var spec api.BucketSpec
	_ = json.Unmarshal(r.Spec, &spec)
	m.ID, m.Name, m.Versioning = types.StringValue(r.ID), types.StringValue(r.Name), types.BoolValue(spec.Versioning)
	m.Endpoint, m.InternalEndpoint, m.CredentialsSecret = outString(r, "endpoint"), outString(r, "internalEndpoint"), outString(r, "credentialsSecret")
}

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), api.BucketSpec{Versioning: plan.Versioning.ValueBool()}, timeout)
	if res.ID != "" {
		plan.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create bucket", err))
	}
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read bucket", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	if !plan.Versioning.Equal(state.Versioning) {
		patch["versioning"] = plan.Versioning.ValueBool()
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update bucket", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete bucket", err))
	}
}

func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// ─── enclave_secret ──────────────────────────────────────────────────────

type secretSourceModel struct {
	Store types.String `tfsdk:"store"`
	Key   types.String `tfsdk:"key"`
	Field types.String `tfsdk:"field"`
}

type secretModel struct {
	ID       types.String       `tfsdk:"id"`
	Name     types.String       `tfsdk:"name"`
	Project  types.String       `tfsdk:"project"`
	Value    types.String       `tfsdk:"value"`
	Source   *secretSourceModel `tfsdk:"source"`
	Version  types.Int64        `tfsdk:"version"`
	SyncedAt types.String       `tfsdk:"synced_at"`
	Timeouts timeouts.Value     `tfsdk:"timeouts"`
}

// secretSpec is the API spec: a value, or a source in a secret store (TF-40).
func (m *secretModel) secretSpec() api.SecretSpec {
	if m.Source != nil {
		return api.SecretSpec{Source: &api.SecretSource{Store: m.Source.Store.ValueString(), Key: m.Source.Key.ValueString(), Field: m.Source.Field.ValueString()}}
	}
	return api.SecretSpec{Value: m.Value.ValueString()}
}

type secretResource struct{ base }

func newSecret() resource.Resource { return &secretResource{base{kind: api.KindSecret}} }

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *secretResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An encrypted secret apps can use as an environment variable. The value is write-only on the server: a change made outside OpenTofu is not detected.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    nameAttr("secret"),
			"project": projectAttr(),
			"value": schema.StringAttribute{Optional: true, Sensitive: true, Description: "The secret value (1 byte – 64 KiB). Changes in place. Exactly one of value and source.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, api.SecretValueMaxBytes), stringvalidator.ExactlyOneOf(path.MatchRoot("value"), path.MatchRoot("source"))}},
			"source": schema.SingleNestedAttribute{Optional: true, Description: "Read the value from a secret store of the project, and keep it in sync.", Attributes: map[string]schema.Attribute{
				"store": schema.StringAttribute{Required: true, Description: "A secret store of the project."},
				"key":   schema.StringAttribute{Required: true, Description: "The path, item or secret name in the store."},
				"field": schema.StringAttribute{Optional: true, Description: "The field of the value."},
			}},
			"version":   schema.Int64Attribute{Computed: true, Description: "Increments each time the value changes."},
			"synced_at": schema.StringAttribute{Computed: true, Description: "When a synced secret was last read from its store."},
			"timeouts":  timeoutsAttr(ctx),
		},
	}
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.createAndWait(ctx, plan.Project, plan.Name.ValueString(), plan.secretSpec(), timeout)
	if res.ID != "" {
		plan.ID, plan.Project, plan.Version = types.StringValue(res.ID), types.StringValue(res.Project), types.Int64Value(res.Generation)
		plan.fillSync(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create secret", err))
	}
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, found, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read secret", err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Name, state.Project, state.Version = types.StringValue(res.Name), types.StringValue(res.Project), types.Int64Value(res.Generation)
	state.fillSync(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	if plan.Source != nil {
		patch["source"], patch["value"] = plan.secretSpec().Source, nil
	} else if !plan.Value.Equal(state.Value) || state.Source != nil {
		patch["value"], patch["source"] = plan.Value.ValueString(), nil
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), patch, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update secret", err))
		return
	}
	plan.ID, plan.Project, plan.Version = state.ID, types.StringValue(res.Project), types.Int64Value(res.Generation)
	plan.fillSync(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAndWait(ctx, state.ID.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete secret", err))
	}
}

// ImportState adopts a secret; its value is unknown to OpenTofu until the next apply sets it.
func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("value"), types.StringNull())...)
}

// fillSync reads a synced secret's version and last sync from its outputs (EXT-03).
func (m *secretModel) fillSync(r api.Resource) {
	m.SyncedAt = outString(r, "syncedAt")
	if v := outInt(r, "version"); m.Source != nil && !v.IsNull() {
		m.Version = v
	}
}
