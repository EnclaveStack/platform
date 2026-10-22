package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// The catalogue and stacks (L2-003 TF-44 – TF-47, ADR-048).

// ─── enclave_stack ───────────────────────────────────────────────────────

type stackMemberModel struct {
	Kind  types.String `tfsdk:"kind"`
	Name  types.String `tfsdk:"name"`
	ID    types.String `tfsdk:"id"`
	Phase types.String `tfsdk:"phase"`
}

type stackModel struct {
	ID               types.String            `tfsdk:"id"`
	Name             types.String            `tfsdk:"name"`
	Project          types.String            `tfsdk:"project"`
	Template         types.String            `tfsdk:"template"`
	TemplateBody     types.String            `tfsdk:"template_body"`
	TemplateVersion  types.String            `tfsdk:"template_version"`
	Parameters       map[string]types.String `tfsdk:"parameters"`
	SecretParameters map[string]types.String `tfsdk:"secret_parameters"`
	Members          types.List              `tfsdk:"members"`
	Outputs          types.Map               `tfsdk:"outputs"`
	Timeouts         timeouts.Value          `tfsdk:"timeouts"`
}

func (m *stackModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *stackModel) limits() timeouts.Value            { return m.Timeouts }

// Parameters are strings in OpenTofu; the server reads numbers and booleans from them by the
// parameter's type.
func (m *stackModel) params() map[string]any {
	if len(m.Parameters) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range m.Parameters {
		out[k] = v.ValueString()
	}
	return out
}

func (m *stackModel) secrets() map[string]string {
	if len(m.SecretParameters) == 0 {
		return nil
	}
	return labelsOf(m.SecretParameters)
}

func (m *stackModel) spec() any {
	s := api.StackSpec{Template: m.Template.ValueString(), TemplateBody: m.TemplateBody.ValueString(), Parameters: m.params(), SecretParameters: m.secrets()}
	if !m.TemplateVersion.IsUnknown() {
		s.Version = m.TemplateVersion.ValueString()
	}
	return s
}

// patch is replaced by stackPatch, which needs the state: parameters removed from the configuration are
// cleared with null.
func (m *stackModel) patch() map[string]any { return map[string]any{} }

func (m *stackModel) stackPatch(state *stackModel) map[string]any {
	patch := map[string]any{}
	if !m.TemplateVersion.IsUnknown() && !m.TemplateVersion.Equal(state.TemplateVersion) {
		patch["version"] = m.TemplateVersion.ValueString()
	}
	params := map[string]any{}
	for k, v := range m.Parameters {
		if p, ok := state.Parameters[k]; !ok || !p.Equal(v) {
			params[k] = v.ValueString()
		}
	}
	for k := range state.Parameters {
		if _, ok := m.Parameters[k]; !ok {
			params[k] = nil
		}
	}
	if len(params) > 0 {
		patch["parameters"] = params
	}
	if !maps.Equal(labelsOf(m.SecretParameters), labelsOf(state.SecretParameters)) {
		patch["secretParameters"] = labelsOf(m.SecretParameters)
	}
	return patch
}

func (m *stackModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.StackSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name = types.StringValue(r.ID), types.StringValue(r.Name)
	m.Template, m.TemplateBody = strOrNull(s.Template), strOrNull(s.TemplateBody)
	m.TemplateVersion = outString(r, api.OutputTemplateVersion)
	if len(s.Parameters) > 0 || m.Parameters != nil {
		prior := m.Parameters
		m.Parameters = map[string]types.String{}
		for k, v := range s.Parameters {
			m.Parameters[k] = paramKeep(prior[k], v)
		}
	}
	// secret_parameters are write-only: they stay as configured.
	var members []api.StackMember
	b, _ := json.Marshal(r.Outputs[api.OutputMembers])
	_ = json.Unmarshal(b, &members)
	list := []stackMemberModel{}
	for _, x := range members {
		list = append(list, stackMemberModel{Kind: types.StringValue(string(x.Kind)), Name: types.StringValue(x.Name), ID: types.StringValue(x.ID), Phase: types.StringValue(string(x.Phase))})
	}
	m.Members, _ = types.ListValueFrom(context.Background(), stackMemberType, list)
	outs := map[string]string{}
	if o, ok := r.Outputs[api.OutputStackOutputs].(map[string]any); ok {
		for k, v := range o {
			outs[k] = fmt.Sprint(v)
		}
	}
	m.Outputs, _ = types.MapValueFrom(context.Background(), types.StringType, outs)
}

var stackMemberType = types.ObjectType{AttrTypes: map[string]attr.Type{"kind": types.StringType, "name": types.StringType, "id": types.StringType, "phase": types.StringType}}

// paramKeep keeps the configured spelling of a parameter the server read as a number or boolean.
func paramKeep(prior types.String, v any) types.String {
	text := fmt.Sprint(v)
	if prior.IsNull() || prior.IsUnknown() {
		return types.StringValue(text)
	}
	p := prior.ValueString()
	switch x := v.(type) {
	case float64:
		if f, err := strconv.ParseFloat(p, 64); err == nil && f == x {
			return prior
		}
	case bool:
		if b, err := strconv.ParseBool(p); err == nil && b == x {
			return prior
		}
	}
	return types.StringValue(text)
}

type stackResource struct {
	kindResource[stackModel, *stackModel]
}

func newStack() resource.Resource {
	return &stackResource{kindResource[stackModel, *stackModel]{base: base{kind: api.KindStack}, suffix: "_stack", label: "stack",
		schemaOf: func(ctx context.Context) schema.Schema {
			oneTemplate := []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("template"), path.MatchRoot("template_body"))}
			return schema.Schema{
				Description: "A stack: a product installed from the catalogue, or from a template of your own, as one thing. Its members are created, changed and deleted with it.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("stack"),
					"project": projectAttr(),
					"template": schema.StringAttribute{Optional: true, Description: "A catalogue entry, like wordpress. Changing it replaces the stack.",
						PlanModifiers: replaced(), Validators: oneTemplate},
					"template_body": schema.StringAttribute{Optional: true, Description: "A template of your own, in YAML or JSON (for example file(\"worker.yaml\")). Changing it replaces the stack.",
						PlanModifiers: replaced(), Validators: oneTemplate},
					"template_version": schema.StringAttribute{Optional: true, Computed: true, Description: "The template's version (default: the newest when created). Changes in place.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
					"parameters": schema.MapAttribute{Optional: true, ElementType: types.StringType,
						Description: "The template's parameters; numbers and booleans are written as strings. Change in place."},
					"secret_parameters": schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType,
						Description: "Secret parameters: they become the stack's secrets and are never read back. Change in place."},
					"members": schema.ListNestedAttribute{Computed: true, Description: "The resources the stack holds.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"kind": schema.StringAttribute{Computed: true}, "name": schema.StringAttribute{Computed: true},
							"id": schema.StringAttribute{Computed: true}, "phase": schema.StringAttribute{Computed: true},
						}}},
					"outputs":  schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "The template's outputs, like its URL."},
					"timeouts": timeoutsAttr(ctx),
				},
			}
		}}}
}

// Update sends only what changed: parameters left out of the configuration are cleared.
func (r *stackResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state stackModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.updateAndWait(ctx, state.ID.ValueString(), plan.stackPatch(&state), timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update stack", err))
		return
	}
	plan.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ─── enclave_catalogue_template ──────────────────────────────────────────

type templateModel struct {
	ID       types.String `tfsdk:"id"`
	Body     types.String `tfsdk:"body"`
	Name     types.String `tfsdk:"name"`
	Title    types.String `tfsdk:"title"`
	Version  types.String `tfsdk:"version"`
	Versions types.List   `tfsdk:"versions"`
}

type templateResource struct{ base }

func newCatalogueTemplate() resource.Resource { return &templateResource{} }

func (r *templateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalogue_template"
}

func (r *templateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *templateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "One of the machine's own catalogue templates (machine admins). A new body with a new version adds that version; destroying it removes every version, refused while stacks use it.",
		Attributes: map[string]schema.Attribute{
			"id":       idAttr(),
			"body":     schema.StringAttribute{Required: true, Description: "The template, in YAML or JSON. Use file(\"worker.yaml\"); written inline, its placeholders are escaped as $${...}."},
			"name":     schema.StringAttribute{Computed: true, Description: "Its name, from the body."},
			"title":    schema.StringAttribute{Computed: true},
			"version":  schema.StringAttribute{Computed: true, Description: "The newest version."},
			"versions": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Every version, newest first."},
		},
	}
}

func (m *templateModel) fill(e api.CatalogueEntry) {
	m.ID, m.Name, m.Title, m.Version, m.Versions = types.StringValue(e.Name), types.StringValue(e.Name), types.StringValue(e.Title), types.StringValue(e.Version), types.ListValueMust(types.StringType, stringValues(e.Versions))
}

func stringValues(v []string) []attr.Value {
	out := make([]attr.Value, len(v))
	for i, s := range v {
		out[i] = types.StringValue(s)
	}
	return out
}

func (r *templateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan templateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.c.AddTemplate(ctx, plan.Body.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Add template", err))
		return
	}
	plan.fill(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *templateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state templateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.c.CatalogueEntry(ctx, state.Name.ValueString(), "")
	if client.Code(err) == api.CodeNotFound {
		resp.State.RemoveResource(ctx) // TF-04
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read template", err))
		return
	}
	state.fill(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update adds the new body as a version; a body with another name replaces the old template.
func (r *templateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state templateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.c.AddTemplate(ctx, plan.Body.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Add template version", err))
		return
	}
	if e.Name != state.Name.ValueString() {
		if err := r.c.RemoveTemplate(ctx, state.Name.ValueString()); err != nil && client.Code(err) != api.CodeNotFound {
			resp.Diagnostics.Append(apiDiag("Remove the old template", err))
		}
	}
	plan.fill(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *templateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state templateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveTemplate(ctx, state.Name.ValueString()); err != nil && client.Code(err) != api.CodeNotFound {
		resp.Diagnostics.Append(apiDiag("Remove template", err))
	}
}

// ImportState adopts a template by name, with its newest version's body (TF-47).
func (r *templateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	e, err := r.c.CatalogueEntry(ctx, req.ID, "")
	if err == nil && e.BuiltIn {
		resp.Diagnostics.AddError("Import failed", req.ID+" is built in: only the machine's own templates are managed.")
		return
	}
	body, err2 := r.c.TemplateSource(ctx, req.ID, "")
	if err == nil {
		err = err2
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Import", err))
		return
	}
	var m templateModel
	m.Body = types.StringValue(body)
	m.fill(e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ─── enclave_catalogue ───────────────────────────────────────────────────

type catalogueData struct{ dataBase }

type catalogueParamModel struct {
	Name     types.String `tfsdk:"name"`
	Title    types.String `tfsdk:"title"`
	Type     types.String `tfsdk:"type"`
	Required types.Bool   `tfsdk:"required"`
	Default  types.String `tfsdk:"default"`
}

type catalogueEntryModel struct {
	Name        types.String          `tfsdk:"name"`
	Title       types.String          `tfsdk:"title"`
	Description types.String          `tfsdk:"description"`
	Version     types.String          `tfsdk:"version"`
	BuiltIn     types.Bool            `tfsdk:"built_in"`
	Parameters  []catalogueParamModel `tfsdk:"parameters"`
}

type catalogueModel struct {
	Entries []catalogueEntryModel `tfsdk:"entries"`
}

func newCatalogueData() datasource.DataSource { return &catalogueData{} }

func (d *catalogueData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalogue"
}

func (d *catalogueData) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configure(req, resp)
}

func (d *catalogueData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "The catalogue: built-in stacks and the machine's own templates, with their parameters.",
		Attributes: map[string]dschema.Attribute{
			"entries": dschema.ListNestedAttribute{Computed: true, NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
				"name": dschema.StringAttribute{Computed: true}, "title": dschema.StringAttribute{Computed: true},
				"description": dschema.StringAttribute{Computed: true}, "version": dschema.StringAttribute{Computed: true},
				"built_in": dschema.BoolAttribute{Computed: true},
				"parameters": dschema.ListNestedAttribute{Computed: true, NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"name": dschema.StringAttribute{Computed: true}, "title": dschema.StringAttribute{Computed: true},
					"type": dschema.StringAttribute{Computed: true}, "required": dschema.BoolAttribute{Computed: true},
					"default": dschema.StringAttribute{Computed: true, Description: "The default, as a string; null without one."},
				}}},
			}}},
		},
	}
}

func (d *catalogueData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	items, err := d.c.Catalogue(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read catalogue", err))
		return
	}
	out := catalogueModel{Entries: []catalogueEntryModel{}}
	for _, e := range items {
		entry := catalogueEntryModel{Name: types.StringValue(e.Name), Title: types.StringValue(e.Title), Description: types.StringValue(e.Description),
			Version: types.StringValue(e.Version), BuiltIn: types.BoolValue(e.BuiltIn), Parameters: []catalogueParamModel{}}
		for _, p := range e.Parameters {
			def := types.StringNull()
			if p.Default != nil {
				def = types.StringValue(fmt.Sprint(p.Default))
			}
			entry.Parameters = append(entry.Parameters, catalogueParamModel{Name: types.StringValue(p.Name), Title: types.StringValue(p.Title),
				Type: types.StringValue(p.Type), Required: types.BoolValue(p.Required), Default: def})
		}
		out.Entries = append(out.Entries, entry)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
