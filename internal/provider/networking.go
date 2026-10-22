package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.4 resources (L2-003 TF-16 – TF-21): network, firewall, load balancer, zone, record. They share one
// implementation of create, read, update, delete and import; each kind supplies its schema and model.

// kindModel is the state model of a v0.4 resource.
type kindModel interface {
	spec() any
	// patch is the whole mutable spec: present fields replace, nil clears (L3-002 §2).
	patch() map[string]any
	fill(r api.Resource)
	ids() (id, name types.String)
	limits() timeouts.Value
}

type kindResource[M any, P interface {
	*M
	kindModel
}] struct {
	base
	suffix, label string
	schemaOf      func(ctx context.Context) schema.Schema
}

func (r *kindResource[M, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.suffix
}

func (r *kindResource[M, P]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req, resp)
}

func (r *kindResource[M, P]) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.schemaOf(ctx)
}

func (r *kindResource[M, P]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m := P(&plan)
	timeout, d := m.limits().Create(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, name := m.ids()
	var project types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("project"), &project)...)
	res, err := r.createAndWait(ctx, project, name.ValueString(), m.spec(), timeout)
	if res.ID != "" {
		m.fill(res)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...) // keep a failed resource in state so it can be fixed or destroyed
	}
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Create "+r.label, err))
	}
}

func (r *kindResource[M, P]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state M
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m := P(&state)
	id, _ := m.ids()
	res, found, err := r.read(ctx, id.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Read "+r.label, err))
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	m.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *kindResource[M, P]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m := P(&plan)
	timeout, d := m.limits().Update(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := P(&state).ids()
	res, err := r.updateAndWait(ctx, id.ValueString(), m.patch(), timeout)
	if err != nil {
		resp.Diagnostics.Append(apiDiag("Update "+r.label, err))
		return
	}
	m.fill(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *kindResource[M, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state M
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m := P(&state)
	timeout, d := m.limits().Delete(ctx, DefaultTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := m.ids()
	if err := r.deleteAndWait(ctx, id.ValueString(), timeout); err != nil {
		resp.Diagnostics.Append(apiDiag("Delete "+r.label, err))
	}
}

func (r *kindResource[M, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	r.importByName(ctx, req, resp)
}

// Value helpers.

func toStrings(v []types.String) []string {
	if v == nil {
		return nil
	}
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = s.ValueString()
	}
	return out
}

func tfStrings(v []string) []types.String {
	if v == nil {
		return nil
	}
	out := make([]types.String, len(v))
	for i, s := range v {
		out[i] = types.StringValue(s)
	}
	return out
}

// foldKeep keeps the configured spelling of a value the server normalises (case, a trailing dot), so
// apply doesn't report a changed value.
func foldKeep(prior types.String, v string) types.String {
	if !prior.IsNull() && !prior.IsUnknown() && strings.EqualFold(strings.TrimSuffix(prior.ValueString(), "."), v) {
		return prior
	}
	return types.StringValue(v)
}

// defaultKeep returns null for a value left unset in configuration that the server filled with its
// default; otherwise the value.
func defaultKeep(prior types.Int64, v, def int) types.Int64 {
	if (prior.IsNull() || prior.IsUnknown()) && v == def {
		return types.Int64Null()
	}
	return types.Int64Value(int64(v))
}

func listOfStrings(desc string, required bool) schema.ListAttribute {
	return schema.ListAttribute{Required: required, Optional: !required, ElementType: types.StringType, Description: desc}
}

func nilIfNoStrings(v []types.String) any {
	if len(v) == 0 {
		return nil
	}
	return toStrings(v)
}

// ─── enclave_network (TF-16) ─────────────────────────────────────────────

type subnetModel struct {
	Name types.String `tfsdk:"name"`
	CIDR types.String `tfsdk:"cidr"`
}

type networkModel struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	Project       types.String   `tfsdk:"project"`
	CIDR          types.String   `tfsdk:"cidr"`
	Subnets       []subnetModel  `tfsdk:"subnet"`
	Internet      types.Bool     `tfsdk:"internet"`
	DockerNetwork types.String   `tfsdk:"docker_network"`
	Gateway       types.String   `tfsdk:"gateway"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (m *networkModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *networkModel) limits() timeouts.Value            { return m.Timeouts }

func (m *networkModel) spec() any {
	internet := m.Internet.IsNull() || m.Internet.IsUnknown() || m.Internet.ValueBool()
	s := api.NetworkSpec{CIDR: m.CIDR.ValueString(), Internet: &internet}
	for _, sub := range m.Subnets {
		s.Subnets = append(s.Subnets, api.Subnet{Name: sub.Name.ValueString(), CIDR: sub.CIDR.ValueString()})
	}
	return s
}

func (m *networkModel) patch() map[string]any { return map[string]any{} } // every field replaces

func (m *networkModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.NetworkSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.CIDR = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.CIDR)
	if m.Subnets != nil || len(s.Subnets) != 1 || s.Subnets[0].Name != api.DefaultSubnetName {
		m.Subnets = nil
		for _, sub := range s.Subnets {
			m.Subnets = append(m.Subnets, subnetModel{Name: types.StringValue(sub.Name), CIDR: types.StringValue(sub.CIDR)})
		}
	}
	m.Internet = types.BoolValue(s.HasInternet())
	m.DockerNetwork, m.Gateway = outString(r, "dockerNetwork"), outString(r, "gateway")
}

func newNetwork() resource.Resource {
	return &kindResource[networkModel, *networkModel]{base: base{kind: api.KindNetwork}, suffix: "_network", label: "network",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A private network with subnets. Its addresses can't change: any change replaces it, which fails while resources use it.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("network"),
					"project": projectAttr(),
					"cidr": schema.StringAttribute{Required: true, Description: "Private IPv4 range, /16 to /28, e.g. 10.40.0.0/16.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"subnet": schema.ListNestedAttribute{Optional: true, Description: "Named ranges inside the network (default: one subnet covering it).",
						PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"name": schema.StringAttribute{Required: true, Description: "Subnet name."},
							"cidr": schema.StringAttribute{Required: true, Description: "Range inside the network, /16 to /29."},
						}}},
					"internet": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
						Description:   "Route to the internet. Without it, nothing in the network can publish a host port.",
						PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
					"docker_network": stableString("The engine network's name."),
					"gateway":        stableString("The network's gateway address."),
					"timeouts":       timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_firewall (TF-17) ────────────────────────────────────────────

type ingressModel struct {
	From     []types.String `tfsdk:"from"`
	Protocol types.String   `tfsdk:"protocol"`
	Ports    []types.String `tfsdk:"ports"`
}

type egressModel struct {
	To       []types.String `tfsdk:"to"`
	Protocol types.String   `tfsdk:"protocol"`
	Ports    []types.String `tfsdk:"ports"`
}

type firewallModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Project           types.String   `tfsdk:"project"`
	Targets           []types.String `tfsdk:"targets"`
	Ingress           []ingressModel `tfsdk:"ingress"`
	EgressDefault     types.String   `tfsdk:"egress_default"`
	Egress            []egressModel  `tfsdk:"egress"`
	AppliedContainers types.Int64    `tfsdk:"applied_containers"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (m *firewallModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *firewallModel) limits() timeouts.Value            { return m.Timeouts }

func (m *firewallModel) spec() any {
	s := api.FirewallSpec{Targets: toStrings(m.Targets), EgressDefault: m.EgressDefault.ValueString()}
	for _, r := range m.Ingress {
		s.Ingress = append(s.Ingress, api.FirewallRule{From: toStrings(r.From), Protocol: r.Protocol.ValueString(), Ports: toStrings(r.Ports)})
	}
	for _, r := range m.Egress {
		s.Egress = append(s.Egress, api.FirewallRule{To: toStrings(r.To), Protocol: r.Protocol.ValueString(), Ports: toStrings(r.Ports)})
	}
	return s
}

func (m *firewallModel) patch() map[string]any {
	s := m.spec().(api.FirewallSpec)
	p := map[string]any{"targets": s.Targets, "egressDefault": s.EgressDefault, "ingress": nil, "egress": nil}
	if len(s.Ingress) > 0 {
		p["ingress"] = s.Ingress
	}
	if len(s.Egress) > 0 {
		p["egress"] = s.Egress
	}
	return p
}

func (m *firewallModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.FirewallSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Targets = types.StringValue(r.ID), types.StringValue(r.Name), tfStrings(s.Targets)
	m.Ingress, m.Egress = nil, nil
	for _, rule := range s.Ingress {
		m.Ingress = append(m.Ingress, ingressModel{From: tfStrings(rule.From), Protocol: types.StringValue(rule.Protocol), Ports: tfStrings(rule.Ports)})
	}
	for _, rule := range s.Egress {
		m.Egress = append(m.Egress, egressModel{To: tfStrings(rule.To), Protocol: types.StringValue(rule.Protocol), Ports: tfStrings(rule.Ports)})
	}
	m.EgressDefault = types.StringValue(s.EgressDefault)
	m.AppliedContainers = outInt(r, "appliedContainers")
}

func ruleAttrs(peers, desc string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		peers: listOfStrings(desc, true),
		"protocol": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.ProtocolTCP),
			Description: "tcp (default), udp, icmp or all.", Validators: []validator.String{stringvalidator.OneOf(api.Protocols...)}},
		"ports": listOfStrings(`Ports or ranges, e.g. ["5432", "8000-8100"]; none means all.`, false),
	}
}

func newFirewall() resource.Resource {
	return &kindResource[firewallModel, *firewallModel]{base: base{kind: api.KindFirewall}, suffix: "_firewall", label: "firewall",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "Allow rules for resources: once a firewall names a resource, only what a rule allows reaches it. Published host ports stay reachable through the machine.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("firewall"),
					"project": projectAttr(),
					"targets": listOfStrings(`Resources to protect, e.g. ["database/orders", "app/api"].`, true),
					"ingress": schema.ListNestedAttribute{Optional: true, Description: "Incoming rules.",
						NestedObject: schema.NestedAttributeObject{Attributes: ruleAttrs("from", `Sources: "any", a CIDR, "network/NAME" or a resource like "app/api".`)}},
					"egress_default": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.EgressAllow),
						Description: "Outgoing traffic no rule names: allow (default) or deny.", Validators: []validator.String{stringvalidator.OneOf(api.EgressDefaults...)}},
					"egress": schema.ListNestedAttribute{Optional: true, Description: "Outgoing rules (with egress_default = \"deny\").",
						NestedObject: schema.NestedAttributeObject{Attributes: ruleAttrs("to", `Destinations, as for ingress sources.`)}},
					"applied_containers": schema.Int64Attribute{Computed: true, Description: "How many containers carry these rules."},
					"timeouts":           timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_load_balancer (TF-18) ───────────────────────────────────────

type lbTargetModel struct {
	App    types.String `tfsdk:"app"`
	Port   types.Int64  `tfsdk:"port"`
	Weight types.Int64  `tfsdk:"weight"`
}

type lbCheckModel struct {
	Type               types.String `tfsdk:"type"`
	Path               types.String `tfsdk:"path"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	TimeoutSeconds     types.Int64  `tfsdk:"timeout_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

type lbModel struct {
	ID             types.String    `tfsdk:"id"`
	Name           types.String    `tfsdk:"name"`
	Project        types.String    `tfsdk:"project"`
	Port           types.Int64     `tfsdk:"port"`
	Protocol       types.String    `tfsdk:"protocol"`
	Publish        types.Bool      `tfsdk:"publish"`
	Algorithm      types.String    `tfsdk:"algorithm"`
	Targets        []lbTargetModel `tfsdk:"target"`
	HealthCheck    *lbCheckModel   `tfsdk:"health_check"`
	InternalHost   types.String    `tfsdk:"internal_host"`
	PrivateDNS     types.String    `tfsdk:"private_dns"`
	HostPort       types.Int64     `tfsdk:"host_port"`
	HealthyTargets types.Int64     `tfsdk:"healthy_targets"`
	Timeouts       timeouts.Value  `tfsdk:"timeouts"`
}

func (m *lbModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *lbModel) limits() timeouts.Value            { return m.Timeouts }

func (m *lbModel) spec() any {
	s := api.LoadBalancerSpec{Port: int(m.Port.ValueInt64()), Protocol: m.Protocol.ValueString(), Publish: m.Publish.ValueBool(), Algorithm: m.Algorithm.ValueString()}
	for _, t := range m.Targets {
		s.Targets = append(s.Targets, api.LoadBalancerTarget{App: t.App.ValueString(), Port: int(t.Port.ValueInt64()), Weight: int(t.Weight.ValueInt64())})
	}
	if h := m.HealthCheck; h != nil {
		s.HealthCheck = &api.LoadBalancerCheck{Type: h.Type.ValueString(), Path: h.Path.ValueString(), IntervalSeconds: int(h.IntervalSeconds.ValueInt64()),
			TimeoutSeconds: int(h.TimeoutSeconds.ValueInt64()), HealthyThreshold: int(h.HealthyThreshold.ValueInt64()), UnhealthyThreshold: int(h.UnhealthyThreshold.ValueInt64())}
	}
	return s
}

func (m *lbModel) patch() map[string]any {
	s := m.spec().(api.LoadBalancerSpec)
	return map[string]any{"port": s.Port, "protocol": s.Protocol, "publish": s.Publish, "algorithm": s.Algorithm, "targets": s.Targets,
		"healthCheck": s.HealthCheck} // nil clears: the server's default check
}

func (m *lbModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.LoadBalancerSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Port = types.StringValue(r.ID), types.StringValue(r.Name), types.Int64Value(int64(s.Port))
	m.Protocol, m.Publish, m.Algorithm = types.StringValue(s.Protocol), types.BoolValue(s.Publish), types.StringValue(s.Algorithm)
	prior := m.Targets
	m.Targets = nil
	for i, t := range s.Targets {
		p := lbTargetModel{Port: types.Int64Null(), Weight: types.Int64Null()}
		if i < len(prior) {
			p = prior[i]
		}
		m.Targets = append(m.Targets, lbTargetModel{App: types.StringValue(t.App), Port: defaultKeep(p.Port, t.Port, 0), Weight: types.Int64Value(int64(t.Weight))})
	}
	if h := s.HealthCheck; h != nil && (m.HealthCheck != nil || h.Type != api.HealthTCP || h.Path != "" || h.IntervalSeconds != api.LBIntervalDefault ||
		h.TimeoutSeconds != api.LBTimeoutDefault || h.HealthyThreshold != api.LBHealthyDefault || h.UnhealthyThreshold != api.LBUnhealthyDefault) {
		p := m.HealthCheck
		if p == nil {
			p = &lbCheckModel{Path: types.StringNull(), IntervalSeconds: types.Int64Null(), TimeoutSeconds: types.Int64Null(),
				HealthyThreshold: types.Int64Null(), UnhealthyThreshold: types.Int64Null()}
		}
		path := types.StringNull()
		if h.Path != "" && !(p.Path.IsNull() && h.Path == "/") {
			path = types.StringValue(h.Path)
		}
		m.HealthCheck = &lbCheckModel{Type: types.StringValue(h.Type), Path: path,
			IntervalSeconds: defaultKeep(p.IntervalSeconds, h.IntervalSeconds, api.LBIntervalDefault), TimeoutSeconds: defaultKeep(p.TimeoutSeconds, h.TimeoutSeconds, api.LBTimeoutDefault),
			HealthyThreshold: defaultKeep(p.HealthyThreshold, h.HealthyThreshold, api.LBHealthyDefault), UnhealthyThreshold: defaultKeep(p.UnhealthyThreshold, h.UnhealthyThreshold, api.LBUnhealthyDefault)}
	} else {
		m.HealthCheck = nil
	}
	m.InternalHost, m.PrivateDNS = outString(r, "internalHost"), outString(r, "privateDns")
	m.HostPort, m.HealthyTargets = outInt(r, "port"), outInt(r, "healthyTargets")
}

func newLoadBalancer() resource.Resource {
	return &kindResource[lbModel, *lbModel]{base: base{kind: api.KindLoadBalancer}, suffix: "_load_balancer", label: "load balancer",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A TCP or HTTP load balancer in front of apps' replicas, in their network.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("load balancer"),
					"project": projectAttr(),
					"port":    schema.Int64Attribute{Required: true, Description: "Port it listens on.", Validators: []validator.Int64{int64validator.Between(api.PortMin, api.PortMax)}},
					"protocol": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.LBProtocolHTTP),
						Description: "http (default) or tcp.", Validators: []validator.String{stringvalidator.OneOf(api.LBProtocols...)}},
					"publish": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Also listen on a host port."},
					"algorithm": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(api.AlgorithmRoundRobin),
						Description: "round_robin (default), least_conn or source.", Validators: []validator.String{stringvalidator.OneOf(api.Algorithms...)}},
					"target": schema.ListNestedAttribute{Required: true, Description: "Apps to send traffic to (1–8, one network).",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"app":  schema.StringAttribute{Required: true, Description: "App name."},
							"port": schema.Int64Attribute{Optional: true, Description: "App port (default: the app's port)."},
							"weight": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.LBWeightDefault),
								Description: "Share of traffic (1–256).", Validators: []validator.Int64{int64validator.Between(api.LBWeightMin, api.LBWeightMax)}},
						}}},
					"health_check": schema.SingleNestedAttribute{Optional: true, Description: "How replicas are checked (default: tcp every 2 s).",
						Attributes: map[string]schema.Attribute{
							"type":                schema.StringAttribute{Required: true, Description: "tcp or http.", Validators: []validator.String{stringvalidator.OneOf(api.LBCheckTypes...)}},
							"path":                schema.StringAttribute{Optional: true, Description: "HTTP path (default /)."},
							"interval_seconds":    schema.Int64Attribute{Optional: true, Description: "Seconds between checks (default 2)."},
							"timeout_seconds":     schema.Int64Attribute{Optional: true, Description: "Seconds before a check gives up (default 1)."},
							"healthy_threshold":   schema.Int64Attribute{Optional: true, Description: "Passing checks before a replica gets traffic (default 2)."},
							"unhealthy_threshold": schema.Int64Attribute{Optional: true, Description: "Failing checks before it stops getting traffic (default 3)."},
						}},
					"internal_host":   stableString("Host other resources in the network use."),
					"private_dns":     stableString("Name it answers to inside its network: <name>.loadbalancer.internal."),
					"host_port":       schema.Int64Attribute{Computed: true, Description: "Port on this machine when published."},
					"healthy_targets": schema.Int64Attribute{Computed: true, Description: "Replicas passing their checks (refreshed on read)."},
					"timeouts":        timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_zone (TF-19) ────────────────────────────────────────────────

type zoneModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Project           types.String   `tfsdk:"project"`
	Provider          types.String   `tfsdk:"dns_provider"`
	Zone              types.String   `tfsdk:"zone"`
	CredentialsSecret types.String   `tfsdk:"credentials_secret"`
	ZoneID            types.String   `tfsdk:"zone_id"`
	NameServers       types.List     `tfsdk:"name_servers"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (m *zoneModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *zoneModel) limits() timeouts.Value            { return m.Timeouts }

func (m *zoneModel) spec() any {
	return api.ZoneSpec{Provider: m.Provider.ValueString(), Zone: m.Zone.ValueString(), CredentialsSecret: m.CredentialsSecret.ValueString()}
}

func (m *zoneModel) patch() map[string]any {
	return map[string]any{"credentialsSecret": m.CredentialsSecret.ValueString()}
}

func (m *zoneModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.ZoneSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Provider = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Provider)
	m.Zone, m.CredentialsSecret = foldKeep(m.Zone, s.Zone), types.StringValue(s.CredentialsSecret)
	m.ZoneID = outString(r, "zoneId")
	var ns []string
	if v, _ := r.Outputs["nameServers"].(string); v != "" {
		ns = strings.Split(v, ",")
	}
	m.NameServers = listOrNull(ns, types.ListNull(types.StringType))
}

func newZone() resource.Resource {
	return &kindResource[zoneModel, *zoneModel]{base: base{kind: api.KindZone}, suffix: "_zone", label: "zone",
		schemaOf: func(ctx context.Context) schema.Schema {
			return schema.Schema{
				Description: "A DNS zone at Cloudflare or Route 53 that EnclaveStack may keep records in. EnclaveStack never creates or deletes the zone itself.",
				Attributes: map[string]schema.Attribute{
					"id":      idAttr(),
					"name":    nameAttr("zone"),
					"project": projectAttr(),
					"dns_provider": schema.StringAttribute{Required: true, Description: "cloudflare or route53 (\"provider\" is reserved by OpenTofu). Changing it replaces the zone.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.OneOf(api.DNSProviders...)}},
					"zone": schema.StringAttribute{Required: true, Description: "The zone's name, e.g. example.com. Changing it replaces the zone.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					"credentials_secret": schema.StringAttribute{Required: true,
						Description: `Secret holding {"apiToken"} (Cloudflare: Zone:Read and DNS:Edit) or {"accessKeyId","secretAccessKey"} (Route 53).`},
					"zone_id":      stableString("The provider's zone ID."),
					"name_servers": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The zone's name servers."},
					"timeouts":     timeoutsAttr(ctx),
				},
			}
		}}
}

// ─── enclave_record (TF-19) ──────────────────────────────────────────────

type recordModel struct {
	ID       types.String   `tfsdk:"id"`
	Name     types.String   `tfsdk:"name"`
	Project  types.String   `tfsdk:"project"`
	Zone     types.String   `tfsdk:"zone"`
	Record   types.String   `tfsdk:"record_name"`
	Type     types.String   `tfsdk:"type"`
	Values   []types.String `tfsdk:"values"`
	Machine  types.Bool     `tfsdk:"machine"`
	TTL      types.Int64    `tfsdk:"ttl"`
	FQDN     types.String   `tfsdk:"fqdn"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (m *recordModel) ids() (types.String, types.String) { return m.ID, m.Name }
func (m *recordModel) limits() timeouts.Value            { return m.Timeouts }

func (m *recordModel) spec() any {
	return api.RecordSpec{Zone: m.Zone.ValueString(), Name: m.Record.ValueString(), Type: m.Type.ValueString(), Values: toStrings(m.Values),
		Machine: m.Machine.ValueBool(), TTL: int(m.TTL.ValueInt64())}
}

func (m *recordModel) patch() map[string]any {
	return map[string]any{"values": nilIfNoStrings(m.Values), "machine": m.Machine.ValueBool(), "ttl": m.TTL.ValueInt64()}
}

func (m *recordModel) fill(r api.Resource) {
	m.Project = types.StringValue(r.Project)
	var s api.RecordSpec
	_ = json.Unmarshal(r.Spec, &s)
	m.ID, m.Name, m.Zone = types.StringValue(r.ID), types.StringValue(r.Name), types.StringValue(s.Zone)
	m.Record, m.Type = foldKeep(m.Record, s.Name), foldKeep(m.Type, s.Type)
	if len(m.Values) != len(s.Values) {
		m.Values = tfStrings(s.Values)
	} else {
		for i, v := range s.Values {
			m.Values[i] = foldKeep(m.Values[i], v)
		}
	}
	m.Machine, m.TTL = types.BoolValue(s.Machine), types.Int64Value(int64(s.TTL))
	m.FQDN = outString(r, "fqdn")
}

func newRecord() resource.Resource {
	return &kindResource[recordModel, *recordModel]{base: base{kind: api.KindRecord}, suffix: "_record", label: "record",
		schemaOf: func(ctx context.Context) schema.Schema {
			replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
			return schema.Schema{
				Description: "A DNS record kept at its zone's provider. A record that already exists there with other values is never overwritten.",
				Attributes: map[string]schema.Attribute{
					"id":          idAttr(),
					"name":        nameAttr("record"),
					"project":     projectAttr(),
					"zone":        schema.StringAttribute{Required: true, Description: "Name of the enclave_zone it belongs to.", PlanModifiers: replace},
					"record_name": schema.StringAttribute{Required: true, Description: `Name inside the zone: "api", "@" for the zone itself, "*.dev" for a wildcard.`, PlanModifiers: replace},
					"type": schema.StringAttribute{Required: true, Description: "A, AAAA, CNAME or TXT.", PlanModifiers: replace,
						Validators: []validator.String{stringvalidator.OneOfCaseInsensitive(api.RecordTypes...)}},
					"values": listOfStrings("Values. Exactly one of values or machine.", false),
					"machine": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
						Description: "Point an A or AAAA record at the machine's public address, and follow it when it changes."},
					"ttl": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(api.RecordTTLDefault),
						Description: "TTL in seconds (60–86400).", Validators: []validator.Int64{int64validator.Between(api.RecordTTLMin, api.RecordTTLMax)}},
					"fqdn":     stableString("The record's full name."),
					"timeouts": timeoutsAttr(ctx),
				},
			}
		}}
}
