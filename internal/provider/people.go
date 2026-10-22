package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// enclave_project and enclave_user (L2-003 TF-35, TF-36). They aren't resources of the API's
// /resources: projects and people have their own endpoints and change synchronously.

// ─── enclave_project ─────────────────────────────────────────────────────

type projectModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

type projectResource struct{ base }

func newProject() resource.Resource { return &projectResource{} }

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A project: a namespace of resources with its own people and tokens. Creating and deleting one needs a machine admin's token; deleting fails while it holds resources.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttr(),
			"name": nameAttr("project"),
			"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				Description: "What the project is for (up to 280 characters).", Validators: []validator.String{stringvalidator.LengthAtMost(api.ProjectDescriptionMaxLen)}},
		},
	}
}

func (m *projectModel) fill(p api.Project) {
	m.ID, m.Name, m.Description = types.StringValue(p.Name), types.StringValue(p.Name), types.StringValue(p.Description)
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.c.CreateProject(ctx, api.CreateProject{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()})
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create project", err))
		return
	}
	plan.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.c.GetProject(ctx, state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read project", err))
		return
	}
	state.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.c.UpdateProject(ctx, plan.Name.ValueString(), api.UpdateProject{Description: plan.Description.ValueString()})
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update project", err))
		return
	}
	plan.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteProject(ctx, state.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(apiDiag("Delete project", err))
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

// ─── enclave_user ────────────────────────────────────────────────────────

type userModel struct {
	ID           types.String `tfsdk:"id"`
	Email        types.String `tfsdk:"email"`
	Role         types.String `tfsdk:"role"`
	ProjectRoles types.Map    `tfsdk:"project_roles"`
	Status       types.String `tfsdk:"status"`
	InviteURL    types.String `tfsdk:"invite_url"`
}

type userResource struct{ base }

func newUser() resource.Resource { return &userResource{} }

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A person with access to the machine. Creating one invites them: send them invite_url, which works once for 7 days. Needs an admin's token.",
		Attributes: map[string]schema.Attribute{
			"id": idAttr(),
			"email": schema.StringAttribute{Required: true, Description: "Their email. Changing it removes the person and invites the new address.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"role": schema.StringAttribute{Optional: true, Description: "Machine role, on every project: viewer, editor or admin.",
				Validators: []validator.String{stringvalidator.OneOf(api.Roles...)}},
			"project_roles": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Role per project, like { shop = \"editor\" }."},
			"status": schema.StringAttribute{Computed: true, Description: "invited until they set a password, then active."},
			"invite_url": schema.StringAttribute{Computed: true, Sensitive: true, Description: "The one-time link to send them (set when created).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (m *userModel) fill(ctx context.Context, u api.UserSummary) {
	m.ID, m.Email, m.Status = types.StringValue(u.ID), types.StringValue(u.Email), types.StringValue(u.Status)
	m.Role = types.StringNull()
	if u.MachineRole != nil {
		m.Role = types.StringValue(*u.MachineRole)
	}
	if len(u.ProjectRoles) == 0 && m.ProjectRoles.IsNull() {
		return
	}
	m.ProjectRoles, _ = types.MapValueFrom(ctx, types.StringType, u.ProjectRoles)
}

func (m *userModel) projectRoles(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if m.ProjectRoles.IsNull() || m.ProjectRoles.IsUnknown() {
		return out, nil
	}
	if d := m.ProjectRoles.ElementsAs(ctx, &out, false); d.HasError() {
		return nil, fmt.Errorf("project_roles must map project names to roles")
	}
	for p, role := range out {
		if !api.ValidRole(role) {
			return nil, fmt.Errorf("project_roles.%s: use viewer, editor or admin", p)
		}
	}
	return out, nil
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roles, err := plan.projectRoles(ctx)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project_roles"), "Invalid project roles", err.Error())
		return
	}
	in := api.InviteUser{Email: plan.Email.ValueString(), ProjectRoles: roles}
	if role := plan.Role.ValueString(); role != "" {
		in.MachineRole = &role
	}
	inv, err := r.c.InviteUser(ctx, in)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Invite person", err))
		return
	}
	plan.fill(ctx, inv.User)
	plan.InviteURL = types.StringValue(inv.URL)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.c.GetUser(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read person", err))
		return
	}
	state.fill(ctx, u)
	if u.Status == api.UserActive {
		state.InviteURL = types.StringNull() // the link was used
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want, err := plan.projectRoles(ctx)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project_roles"), "Invalid project roles", err.Error())
		return
	}
	had, _ := state.projectRoles(ctx)
	changes := map[string]*string{}
	for p, role := range want {
		if had[p] != role {
			role := role
			changes[p] = &role
		}
	}
	for p := range had {
		if _, ok := want[p]; !ok {
			changes[p] = nil
		}
	}
	setMachine := !plan.Role.Equal(state.Role)
	var machine *string
	if role := plan.Role.ValueString(); role != "" {
		machine = &role
	}
	u, err := r.c.UpdateUser(ctx, state.ID.ValueString(), setMachine, machine, changes)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update person", err))
		return
	}
	plan.fill(ctx, u)
	plan.InviteURL = state.InviteURL
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteUser(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(apiDiag("Remove person", err))
	}
}

// ImportState adopts a person by email.
func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	u, err := r.c.FindUser(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Import", err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), u.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), u.Email)...)
}
