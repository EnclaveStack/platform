package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// enclave_bucket_object (L2-003 TF-50, L2-013): one file in a bucket. Its id is "<bucket>/<key>".

type bucketObjectModel struct {
	ID          types.String `tfsdk:"id"`
	Project     types.String `tfsdk:"project"`
	Bucket      types.String `tfsdk:"bucket"`
	Key         types.String `tfsdk:"key"`
	Source      types.String `tfsdk:"source"`
	Content     types.String `tfsdk:"content"`
	SourceHash  types.String `tfsdk:"source_hash"`
	Tags        types.Map    `tfsdk:"tags"`
	Size        types.Int64  `tfsdk:"size"`
	ContentType types.String `tfsdk:"content_type"`
}

type bucketObjectResource struct{ base }

func newBucketObject() resource.Resource { return &bucketObjectResource{} }

func (r *bucketObjectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_object"
}

func (r *bucketObjectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *bucketObjectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A file in a bucket, up to 5 GiB. Destroying it deletes the file. Import as <bucket>/<key>.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "<bucket>/<key>.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project": projectAttr(),
			"bucket":  schema.StringAttribute{Required: true, Description: "The bucket's name.", PlanModifiers: replace},
			"key":     schema.StringAttribute{Required: true, Description: "Where the file goes, like media/intro.mp4.", PlanModifiers: replace},
			"source": schema.StringAttribute{Optional: true, Description: "A file to upload. Set source_hash = filesha256(...) so a changed file is uploaded again.",
				Validators: []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("source"), path.MatchRoot("content"))}},
			"content":     schema.StringAttribute{Optional: true, Description: "Text to upload instead of a file."},
			"source_hash": schema.StringAttribute{Optional: true, Description: "Any value; when it changes the file is uploaded again."},
			"tags": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Tags on the file (x-amz-meta-*): names in lowercase letters, digits and hyphens. Transcoders carry them to their outputs."},
			"size":         schema.Int64Attribute{Computed: true, Description: "Its size in bytes."},
			"content_type": schema.StringAttribute{Computed: true, Description: "Its media type, from the key's extension."},
		},
	}
}

func (m *bucketObjectModel) body() (io.Reader, int64, func(), error) {
	if !m.Content.IsNull() {
		b := []byte(m.Content.ValueString())
		return bytes.NewReader(b), int64(len(b)), func() {}, nil
	}
	f, err := os.Open(m.Source.ValueString())
	if err != nil {
		return nil, 0, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, nil, err
	}
	return f, st.Size(), func() { f.Close() }, nil
}

func (r *bucketObjectResource) bucketID(ctx context.Context, m *bucketObjectModel) (*client.Client, string, error) {
	c := r.in(m.Project)
	b, err := c.FindResource(ctx, api.KindBucket, m.Bucket.ValueString())
	if err != nil {
		return nil, "", err
	}
	m.Project = types.StringValue(b.Project)
	return c, b.ID, nil
}

func (r *bucketObjectResource) upload(ctx context.Context, m *bucketObjectModel) error {
	c, id, err := r.bucketID(ctx, m)
	if err != nil {
		return err
	}
	body, size, done, err := m.body()
	if err != nil {
		return err
	}
	defer done()
	tags := map[string]string{}
	if !m.Tags.IsNull() {
		for k, v := range m.Tags.Elements() {
			s, _ := v.(types.String)
			tags[k] = s.ValueString()
		}
	}
	info, err := c.PutObject(ctx, id, m.Key.ValueString(), body, size, tags)
	if err != nil {
		return err
	}
	m.ID = types.StringValue(m.Bucket.ValueString() + "/" + info.Key)
	m.Size, m.ContentType = types.Int64Value(info.Size), types.StringValue(info.ContentType)
	return nil
}

func (r *bucketObjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketObjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.upload(ctx, &plan); err != nil {
		resp.Diagnostics.Append(apiDiag("Upload object", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read keeps state as configured while the file is there; a deleted file is planned for upload again.
func (r *bucketObjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketObjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, id, err := r.bucketID(ctx, &state)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read object", err))
		return
	}
	info, err := c.HeadObject(ctx, id, state.Key.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read object", err))
		return
	}
	state.Size, state.ContentType = types.Int64Value(info.Size), types.StringValue(info.ContentType)
	if !state.Tags.IsNull() || len(info.Tags) > 0 { // unset tags stay null while the file has none
		tags, d := types.MapValueFrom(ctx, types.StringType, info.Tags)
		resp.Diagnostics.Append(d...)
		state.Tags = tags
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *bucketObjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan bucketObjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.upload(ctx, &plan); err != nil {
		resp.Diagnostics.Append(apiDiag("Upload object", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketObjectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketObjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, id, err := r.bucketID(ctx, &state)
	if client.IsNotFound(err) {
		return // the bucket went first, and its files with it
	}
	if err == nil {
		err = c.DeleteObject(ctx, id, state.Key.ValueString())
	}
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.Append(apiDiag("Delete object", err))
	}
}

// ImportState adopts "<bucket>/<key>"; source or content must then be added to the configuration.
func (r *bucketObjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	bucket, key, ok := strings.Cut(req.ID, "/")
	if !ok || bucket == "" || key == "" {
		resp.Diagnostics.AddError("Import object", fmt.Sprintf("Use <bucket>/<key>, like media/intro.mp4; got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket"), bucket)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
}
