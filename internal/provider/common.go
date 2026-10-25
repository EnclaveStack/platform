package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// DefaultTimeout bounds create, update and delete (TF-02).
const DefaultTimeout = 10 * time.Minute

const pollInterval = time.Second

// base carries what every resource needs.
type base struct {
	kind api.Kind
	c    *client.Client
}

func (b *base) configure(req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	b.c = c
}

// Shared schema pieces.

func idAttr() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: "Resource ID.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}

func nameAttr(kind string) schema.StringAttribute {
	return schema.StringAttribute{Required: true, Description: "Name of the " + kind + ": 3–40 lowercase letters, digits or hyphens, starting with a letter. Changing it replaces the " + kind + ".",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators:    []validator.String{nameValidator{}}}
}

// projectAttr is every resource's project (TF-34): the provider's project when not set; changing it
// replaces the resource (ADR-035).
func projectAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true,
		Description:   "The project to create it in. Defaults to the provider's project (ENCLAVE_PROJECT, else default). Changing it replaces the resource.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
		Validators:    []validator.String{nameValidator{}}}
}

// in returns the client for a configured project, or the provider's when it isn't set.
func (b *base) in(project types.String) *client.Client {
	if project.IsNull() || project.IsUnknown() || project.ValueString() == "" {
		return b.c
	}
	return b.c.InProject(project.ValueString())
}

func timeoutsAttr(ctx context.Context) schema.Attribute {
	return timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true})
}

// nameValidator applies the API's name rule at plan time (L3-002 §4).
type nameValidator struct{}

func (nameValidator) Description(context.Context) string { return "a valid EnclaveStack resource name" }
func (nameValidator) MarkdownDescription(context.Context) string {
	return "a valid EnclaveStack resource name"
}
func (nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, e := range api.ValidateName(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name", e.Message)
	}
}

// apiDiag turns an API or wait error into a diagnostic carrying the server's message and request ID.
func apiDiag(action string, err error) diag.Diagnostic {
	var ae *api.Error
	var fe *client.FailedError
	switch {
	case errors.As(err, &fe):
		return diag.NewErrorDiagnostic(action+" failed", fe.Error())
	case errors.As(err, &ae):
		detail := ae.Message
		for _, d := range ae.Details {
			detail += fmt.Sprintf("\n  %s: %s", d.Field, d.Message)
		}
		if ae.RequestID != "" {
			detail += "\nRequest ID: " + ae.RequestID
		}
		return diag.NewErrorDiagnostic(action+" failed", detail)
	case errors.Is(err, context.DeadlineExceeded):
		return diag.NewErrorDiagnostic(action+" timed out", "The operation is still running on the EnclaveStack machine. Raise the timeout or check `enclave get`.")
	}
	return diag.NewErrorDiagnostic(action+" failed", err.Error())
}

// createAndWait creates a resource and waits until it is ready.
func (b *base) createAndWait(ctx context.Context, project types.String, name string, spec any, timeout time.Duration) (api.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := b.in(project).CreateResource(ctx, b.kind, name, spec)
	if err != nil {
		return r, err
	}
	return b.c.WaitReady(ctx, r.ID, pollInterval, nil)
}

// updateAndWait patches and waits.
func (b *base) updateAndWait(ctx context.Context, id string, patch map[string]any, timeout time.Duration) (api.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if len(patch) > 0 {
		cur, err := b.c.GetResource(ctx, id)
		if err != nil {
			return cur, err
		}
		gen := cur.Generation
		if _, err := b.c.UpdateResource(ctx, id, patch, &gen); err != nil {
			return cur, err
		}
	}
	return b.c.WaitReady(ctx, id, pollInterval, nil)
}

// deleteAndWait deletes and waits until gone; an already-missing resource is success.
func (b *base) deleteAndWait(ctx context.Context, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := b.c.DeleteResource(ctx, id, nil); err != nil {
		if client.IsNotFound(err) {
			return nil
		}
		return err
	}
	return b.c.WaitGone(ctx, id, pollInterval)
}

// read fetches a resource; found=false when it no longer exists (TF-04: drift removes it from state).
func (b *base) read(ctx context.Context, id string) (api.Resource, bool, error) {
	r, err := b.c.GetResource(ctx, id)
	if client.IsNotFound(err) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if r.Status.Phase == api.PhaseDeleting {
		return r, false, nil
	}
	return r, true, nil
}

// importByName adopts an existing resource by name: "<name>" in the provider's project, or
// "<project>/<name>" (TF-05, PRJ-09).
func (b *base) importByName(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	c, name := b.c, req.ID
	if project, n, ok := strings.Cut(req.ID, "/"); ok {
		c, name = b.c.InProject(project), n
	}
	r, err := c.FindResource(ctx, b.kind, name)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Import", err))
		return
	}
	if r.Stack != nil { // TF-46: a stack's member is managed through its stack
		stack := *r.Stack
		if st, err := b.c.GetResource(ctx, stack); err == nil {
			stack = st.Name
		}
		resp.Diagnostics.AddError("Import failed", fmt.Sprintf("%s %s is part of the stack %s: manage it with enclave_stack, and read it with the data sources.", b.kind, r.Name, stack))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), r.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), r.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), r.Project)...)
}

// Output helpers.

func outString(r api.Resource, key string) types.String {
	if v, ok := r.Outputs[key]; ok && v != nil {
		return types.StringValue(fmt.Sprint(v))
	}
	return types.StringNull()
}

func outInt(r api.Resource, key string) types.Int64 {
	switch v := r.Outputs[key].(type) {
	case float64:
		return types.Int64Value(int64(v))
	case int:
		return types.Int64Value(int64(v))
	case int64:
		return types.Int64Value(v)
	}
	return types.Int64Null()
}
