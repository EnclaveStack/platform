// Package provider is the OpenTofu / Terraform provider enclavestack/enclave (L2-003, ADR-009). It is
// a client of the control-plane API through pkg/client, so every resource behaves exactly as it does
// in the console and the CLI (ADR-003).
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/client"
)

// Address is the provider's registry address.
const Address = "registry.opentofu.org/enclavestack/enclave"

// DefaultServer matches the CLI's default.
const DefaultServer = "http://127.0.0.1:7070"

// Provider is the enclave provider.
type Provider struct{ version string }

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &Provider{version: version} }
}

type providerModel struct {
	Server  types.String `tfsdk:"server"`
	Token   types.String `tfsdk:"token"`
	Project types.String `tfsdk:"project"`
}

// Metadata implements provider.Provider.
func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "enclave"
	resp.Version = p.version
}

// Schema implements provider.Provider.
func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage apps, databases, caches, buckets and secrets on a EnclaveStack machine.",
		Attributes: map[string]schema.Attribute{
			"server":  schema.StringAttribute{Optional: true, Description: "Control-plane URL. Defaults to ENCLAVE_SERVER, then " + DefaultServer + "."},
			"token":   schema.StringAttribute{Optional: true, Sensitive: true, Description: "API token with an editor role (or admin, for projects and people). Defaults to ENCLAVE_TOKEN."},
			"project": schema.StringAttribute{Optional: true, Description: "Project resources are created in unless they set their own. Defaults to ENCLAVE_PROJECT, then default."},
		},
	}
}

// Configure implements provider.Provider (TF-07).
func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	server, token := cfg.Server.ValueString(), cfg.Token.ValueString()
	if server == "" {
		server = os.Getenv("ENCLAVE_SERVER")
	}
	if server == "" {
		server = DefaultServer
	}
	if token == "" {
		token = os.Getenv("ENCLAVE_TOKEN")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing EnclaveStack token",
			"Set token in the provider block or ENCLAVE_TOKEN. Create one with `enclave token create --name opentofu` or in the console under API tokens.")
		return
	}
	c := client.New(server, token)
	c.UserAgent = "terraform-provider-enclave/" + p.version
	c.Project = cfg.Project.ValueString()
	if c.Project == "" {
		c.Project = os.Getenv("ENCLAVE_PROJECT")
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

// Resources implements provider.Provider (TF-01).
func (p *Provider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newDatabase, newCache, newBucket, newApp, newSecret, newDomain, newMachineSettings, newJob, newBackup,
		newNetwork, newFirewall, newLoadBalancer, newZone, newRecord, newVM, newVolume, newSnapshot,
		newQueue, newTopic, newSearch, newTranscoder, newCDN, newEmail, newProject, newUser,
		newDashboard, newAlert, newChannel, newLogExport, newSecretStore,
		newMachine, newStack, newCatalogueTemplate, newBucketObject}
}

// DataSources implements provider.Provider (TF-01).
func (p *Provider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{newMachineData, newSecretValueData, newCAData, newMetricsData, newMachinesData, newCatalogueData}
}
