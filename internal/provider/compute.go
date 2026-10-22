package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.5 resources (L2-003 TF-22 – TF-26): VMs, volumes, snapshots, and volume mounts on apps and jobs.

// ─── volumes on enclave_app and enclave_job (TF-24) ───────────────────

type mountModel struct {
	Volume   types.String `tfsdk:"volume"`
	Path     types.String `tfsdk:"path"`
	ReadOnly types.Bool   `tfsdk:"read_only"`
}

func volumesAttr() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Optional: true, Description: "Filesystem volumes mounted into every container; a change deploys a new revision.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"volume":    schema.StringAttribute{Required: true, Description: "A filesystem volume's name."},
			"path":      schema.StringAttribute{Required: true, Description: "Absolute path inside the container, e.g. /data."},
			"read_only": schema.BoolAttribute{Optional: true, Description: "Mount it read-only (default false)."},
		}}}
}

func mountsOf(ms []mountModel) []api.VolumeMount {
	var out []api.VolumeMount
	for _, m := range ms {
		out = append(out, api.VolumeMount{Volume: m.Volume.ValueString(), Path: m.Path.ValueString(), ReadOnly: m.ReadOnly.ValueBool()})
	}
	return out
}

// mountModels mirrors the server's mounts, keeping read_only null where the configuration left it unset.
func mountModels(ms []api.VolumeMount, prior []mountModel) []mountModel {
	if len(ms) == 0 {
		return nil
	}
	out := make([]mountModel, len(ms))
	for i, m := range ms {
		ro := types.BoolValue(m.ReadOnly)
		if !m.ReadOnly && (i >= len(prior) || prior[i].ReadOnly.IsNull()) {
			ro = types.BoolNull()
		}
		out[i] = mountModel{Volume: types.StringValue(m.Volume), Path: types.StringValue(m.Path), ReadOnly: ro}
	}
	return out
}

func nilIfNoMounts(ms []api.VolumeMount) any {
	if len(ms) == 0 {
		return nil
	}
	return ms
}

// growOnly refuses a plan that lowers a size (VM-02, VOL-03): the error shows at plan time, not apply.
type growOnly struct{ what string }

func (g growOnly) Description(context.Context) string { return g.what + " can only grow" }

func (g growOnly) MarkdownDescription(ctx context.Context) string { return g.Description(ctx) }

func (g growOnly) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || req.StateValue.IsNull() || req.PlanValue.IsUnknown() || req.PlanValue.IsNull() {
		return
	}
	if req.PlanValue.ValueInt64() < req.StateValue.ValueInt64() {
		resp.Diagnostics.AddAttributeError(req.Path, "Can't shrink",
			fmt.Sprintf("The %s can only grow; it is %d GiB now.", g.what, req.StateValue.ValueInt64()))
	}
}

// ─── enclave_volume (TF-23) ──────────────────────────────────────────────

type volumeModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Type       types.String   `tfsdk:"type"`
	SizeGiB    types.Int64    `tfsdk:"size_gib"`
	Snapshot   types.String   `tfsdk:"snapshot"`
	AttachedTo types.String   `tfsdk:"attached_to"`
	UsedBytes  types.Int64    `tfsdk:"used_bytes"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

func (m *volumeModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *volumeModel) limits() timeouts.Value            { return m.Timeouts }

func (m *volumeModel) spec() any {
	return api.VolumeSpec{Type: m.Type.ValueString(), SizeGiB: int(m.SizeGiB.ValueInt64()), Snapshot: m.Snapshot.ValueString(), Placed: m.placed()}
}

func (m *volumeModel) patch() map[string]any {
	if m.Type.ValueString() != api.VolumeDisk || m.SizeGiB.IsNull() || m.SizeGiB.IsUnknown() {
		return map[string]any{}
	}
	return map[string]any{"sizeGiB": m.SizeGiB.ValueInt64()}
}

func (m *volumeModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.VolumeSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Type, m.Snapshot = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Type), strOrNull(s.Snapshot)
	m.SizeGiB = types.Int64Null()
	if s.Type == api.VolumeDisk {
		m.SizeGiB = types.Int64Value(int64(s.SizeGiB))
	}
	m.AttachedTo = types.StringValue(outString(r, "attachedTo").ValueString())
	m.UsedBytes = outInt(r, "usedBytes")
}

func newVolume() resource.Resource {
	return &kindResource[volumeModel, *volumeModel]{base: base{kind: api.KindVolume}, suffix: "_volume", label: "volume",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A volume: a disk attached to one VM, or a filesystem mounted into apps and jobs. Deleting it deletes its data.",
				Attributes: map[string]schema.Attribute{
					"id":             idAttr(),
					"name":           nameAttr("volume"),
					"project":        projectAttr(),
					"machine":        machineAttr(),
					"machine_labels": machineLabelsAttr(),
					"type": schema.StringAttribute{Required: true, Description: "disk or filesystem. Changing it replaces the volume.",
						Validators:    []validator.String{stringvalidator.OneOf(api.VolumeTypes...)},
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"size_gib": schema.Int64Attribute{Optional: true, Computed: true, Description: "A disk's size in GiB (default 10); it only grows, live. Filesystems have none.",
						PlanModifiers: []planmodifier.Int64{growOnly{what: "disk"}}},
					"snapshot": schema.StringAttribute{Optional: true, Description: "A snapshot of the same type to start from. Changing it replaces the volume.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"attached_to": schema.StringAttribute{Computed: true, Description: "What uses it, as comma-separated kind/name; refreshed by the server every minute or so."},
					"used_bytes":  schema.Int64Attribute{Computed: true, Description: "Space a filesystem's files use (measured every few minutes)."},
					"timeouts":    timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_snapshot (TF-25) ────────────────────────────────────────────

type snapshotModel struct {
	ID        types.String   `tfsdk:"id"`
	Name      types.String   `tfsdk:"name"`
	Project   types.String   `tfsdk:"project"`
	Source    types.String   `tfsdk:"source"`
	Type      types.String   `tfsdk:"type"`
	SizeBytes types.Int64    `tfsdk:"size_bytes"`
	TakenAt   types.String   `tfsdk:"taken_at"`
	Timeouts  timeouts.Value `tfsdk:"timeouts"`
}

func (m *snapshotModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *snapshotModel) limits() timeouts.Value            { return m.Timeouts }
func (m *snapshotModel) spec() any                         { return api.SnapshotSpec{Source: m.Source.ValueString()} }
func (m *snapshotModel) patch() map[string]any             { return map[string]any{} } // never changes

func (m *snapshotModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.SnapshotSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Source = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Source)
	m.Type, m.SizeBytes, m.TakenAt = outString(r, "type"), outInt(r, "sizeBytes"), outString(r, "takenAt")
}

func newSnapshot() resource.Resource {
	return &kindResource[snapshotModel, *snapshotModel]{base: base{kind: api.KindSnapshot}, suffix: "_snapshot", label: "snapshot",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A full copy of a volume or a VM's root disk, taken while in use and kept on the machine. It never changes: any change replaces it.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("snapshot"),
					"project": projectAttr(),
					"source": schema.StringAttribute{Required: true, Description: "What to copy: volume/NAME or vm/NAME.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"type":       stableString("disk or filesystem."),
					"size_bytes": schema.Int64Attribute{Computed: true, Description: "Size of the copy on the machine's disk."},
					"taken_at":   stableString("When the copy was taken (UTC, RFC 3339)."),
					"timeouts":   timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_vm (TF-22) ──────────────────────────────────────────────────

type vmModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	placementModel
	Image        types.String   `tfsdk:"image"`
	ImageSHA256  types.String   `tfsdk:"image_sha256"`
	SSHKeys      []types.String `tfsdk:"ssh_keys"`
	SSHKeySecret types.String   `tfsdk:"ssh_key_secret"`
	UserData     types.String   `tfsdk:"user_data"`
	CPUs         types.Int64    `tfsdk:"cpus"`
	MemoryMiB    types.Int64    `tfsdk:"memory_mib"`
	DiskGiB      types.Int64    `tfsdk:"disk_gib"`
	Network      types.String   `tfsdk:"network"`
	Subnet       types.String   `tfsdk:"subnet"`
	Disks        []types.String `tfsdk:"disks"`
	Publish      []types.Int64  `tfsdk:"publish"`
	Power        types.String   `tfsdk:"power"`
	PrivateIP    types.String   `tfsdk:"private_ip"`
	PrivateDNS   types.String   `tfsdk:"private_dns"`
	SSHAddress   types.String   `tfsdk:"ssh_address"`
	SSHUser      types.String   `tfsdk:"ssh_user"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (m *vmModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *vmModel) limits() timeouts.Value            { return m.Timeouts }

func ints(v []types.Int64) []int {
	var out []int
	for _, n := range v {
		out = append(out, int(n.ValueInt64()))
	}
	return out
}

func (m *vmModel) spec() any {
	return api.VMSpec{Image: m.Image.ValueString(), ImageSHA256: m.ImageSHA256.ValueString(), SSHKeys: toStrings(m.SSHKeys), SSHKeySecret: m.SSHKeySecret.ValueString(),
		UserData: m.UserData.ValueString(), CPUs: int(m.CPUs.ValueInt64()), MemoryMiB: int(m.MemoryMiB.ValueInt64()), DiskGiB: int(m.DiskGiB.ValueInt64()),
		Network: m.Network.ValueString(), Subnet: m.Subnet.ValueString(), Disks: toStrings(m.Disks), Publish: ints(m.Publish), Power: m.Power.ValueString(), Placed: m.placed()}
}

// patch is every field that changes in place (L3-002 §8.2); the rest replace the VM.
func (m *vmModel) patch() map[string]any {
	s := m.spec().(api.VMSpec)
	var publish any
	if len(s.Publish) > 0 {
		publish = s.Publish
	}
	return map[string]any{"cpus": s.CPUs, "memoryMiB": s.MemoryMiB, "diskGiB": s.DiskGiB, "network": nilIfEmptyString(s.Network),
		"subnet": nilIfEmptyString(s.Subnet), "disks": nilIfNoStrings(m.Disks), "publish": publish, "power": s.Power}
}

func (m *vmModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.VMSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.fillMachine(r, s.Placed)
	m.ID, m.Name, m.Image = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Image)
	m.ImageSHA256, m.SSHKeySecret, m.UserData = strOrNull(s.ImageSHA256), strOrNull(s.SSHKeySecret), strOrNull(s.UserData)
	if len(s.SSHKeys) > 0 || m.SSHKeys != nil {
		m.SSHKeys = tfStrings(s.SSHKeys)
	}
	m.CPUs, m.MemoryMiB, m.DiskGiB = types.Int64Value(int64(s.CPUs)), types.Int64Value(int64(s.MemoryMiB)), types.Int64Value(int64(s.DiskGiB))
	m.Network, m.Subnet, m.Power = strOrNull(s.Network), strOrNull(s.Subnet), types.StringValue(s.Power)
	if len(s.Disks) > 0 || m.Disks != nil {
		m.Disks = tfStrings(s.Disks)
	}
	if len(s.Publish) > 0 || m.Publish != nil {
		m.Publish = nil
		for _, p := range s.Publish {
			m.Publish = append(m.Publish, types.Int64Value(int64(p)))
		}
	}
	m.PrivateIP, m.PrivateDNS = outString(r, "privateIp"), outString(r, "privateDns")
	m.SSHAddress, m.SSHUser = outString(r, "sshAddress"), outString(r, "sshUser")
}

// replaced is the plan modifier of a field that can't change in place (CP-04).
func replaced() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplace()}
}

func newVM() resource.Resource {
	return &kindResource[vmModel, *vmModel]{base: base{kind: api.KindVM}, suffix: "_vm", label: "VM",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A virtual machine run by QEMU on the machine. Image, keys and first-boot script are set once (changing them replaces the VM); CPUs, memory and disks apply by restarting it.",
				Attributes: map[string]schema.Attribute{
					"id":             idAttr(),
					"name":           nameAttr("VM"),
					"project":        projectAttr(),
					"machine":        machineAttr(),
					"machine_labels": machineLabelsAttr(),
					"image": schema.StringAttribute{Required: true, Description: "ubuntu-24.04, debian-12, an https:// URL of a cloud image, or snapshot/NAME.",
						PlanModifiers: replaced()},
					"image_sha256": schema.StringAttribute{Optional: true, Description: "SHA-256 of a URL image.", PlanModifiers: replaced()},
					"ssh_keys": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "OpenSSH public keys (or ssh_key_secret).",
						PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
					"ssh_key_secret": schema.StringAttribute{Optional: true, Description: "A secret holding public keys, one per line.", PlanModifiers: replaced()},
					"user_data": schema.StringAttribute{Optional: true, Description: "First-boot script: #cloud-config or #!...; runs once.",
						PlanModifiers: replaced()},
					"cpus": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.VMCPUsDefault),
						Description: "CPUs, 1–16 (default 1). A change restarts the VM."},
					"memory_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.VMMemoryDefaultMiB),
						Description: "Memory in MiB, 512–65536 in steps of 128 (default 1024). A change restarts the VM."},
					"disk_gib": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.VMDiskDefaultGiB),
						Description: "Root disk in GiB (default 10); it only grows, live.", PlanModifiers: []planmodifier.Int64{growOnly{what: "root disk"}}},
					"network": placementNetworkAttr(),
					"subnet":  placementSubnetAttr(),
					"disks": schema.ListAttribute{Optional: true, ElementType: types.StringType,
						Description: "Disk volumes to attach, in order. A change restarts the VM."},
					"publish": schema.ListAttribute{Optional: true, ElementType: types.Int64Type, Description: "VM ports published on the same host port (not 22)."},
					"power": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.PowerRunning),
						Validators: []validator.String{stringvalidator.OneOf(api.PowerStates...)}, Description: "running (default) or stopped."},
					"private_ip":  placedString("The VM's address in its network."),
					"private_dns": stableString("Name the VM answers to inside its network: <name>.vm.internal."),
					"ssh_address": stableString("Where SSH answers on this machine, 127.0.0.1:PORT; the user is ssh_user."),
					"ssh_user":    stableString("The user with your keys and sudo: enclave."),
					"timeouts":    timeoutsAttr(ctx),
				},
			}
		}}
}
