package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/secretbox"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

// restartable kinds run containers or processes a restart helps.
var restartable = []api.Kind{api.KindApp, api.KindDatabase, api.KindCache, api.KindVM, api.KindSearch, api.KindEmail, api.KindCDN}

// IdempotencyTTL is how long a stored response answers replays (ADR-010).
const IdempotencyTTL = 24 * time.Hour

// Queue receives resource IDs the reconciler should look at now.
type Queue interface{ Enqueue(id string) }

// Resources implements resource CRUD, actions, events, logs and secret reveal.
type Resources struct {
	Store  *store.Store
	Box    *secretbox.Box
	Engine engine.Driver // read-only use: Info, Logs
	// Hypervisor answers VM limits and console logs (read-only use: Check, ConsoleLog, CachedImages);
	// SnapshotDir is where snapshots are, for the space they hold.
	Hypervisor  engine.Hypervisor
	SnapshotDir string
	Queue       Queue
	Now         func() time.Time
	Log         *slog.Logger
	// Messaging reaches the broker for queue and topic data; Mail reaches email services (ADR-029,
	// ADR-032). Metrics counts messages sent; nil in tests that don't need it.
	Messaging *messaging.Pool
	Mail      mail.Client
	Metrics   *observability.Metrics
	// MachineOnline tells whether a machine takes calls now (moves, L2-010 FAIL-03); nil means every machine.
	MachineOnline func(machineID string) bool
}

func notFound() error { return api.NewError(api.CodeNotFound, "That resource doesn't exist.") }

func (s *Resources) projectNames(ctx context.Context) ([]string, error) {
	rows, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names, nil
}

// Get returns one resource.
func (s *Resources) Get(ctx context.Context, id string) (api.Resource, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Resource{}, notFound()
	}
	if err != nil {
		return api.Resource{}, err
	}
	return r.ToAPI(), nil
}

// List pages through the resources the principal can see, in one project or all of them (PRJ-08).
func (s *Resources) List(ctx context.Context, p Principal, project string, f store.ListFilter) (api.ResourceList, error) {
	if project != "" {
		if p.RoleIn(project) == "" {
			return api.ResourceList{}, projectNotFound(project)
		}
		if _, err := s.Store.GetProject(ctx, project); errors.Is(err, store.ErrNotFound) {
			return api.ResourceList{}, projectNotFound(project)
		}
		f.Projects = []string{project}
	} else {
		names, err := s.projectNames(ctx)
		if err != nil {
			return api.ResourceList{}, err
		}
		f.Projects = p.VisibleProjects(names)
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return api.ResourceList{}, api.Validation([]api.FieldError{{Field: "kind", Code: api.FieldNotAllowed, Message: api.KindChoiceMessage}})
	}
	// ?machine= and ?stack= name a machine and a stack of the project (L3-002 §11.3, §11.7).
	if f.Machine != "" {
		m, err := s.Store.GetMachineByName(ctx, f.Machine)
		if errors.Is(err, store.ErrNotFound) {
			return api.ResourceList{}, api.NewError(api.CodeNotFound, "There's no machine named "+f.Machine+".")
		}
		if err != nil {
			return api.ResourceList{}, err
		}
		f.Machine = m.ID
	}
	if f.Stack != "" {
		st, err := s.Store.GetResourceByName(ctx, project, api.KindStack, f.Stack)
		if errors.Is(err, store.ErrNotFound) || err == nil && p.RoleIn(st.ProjectName()) == "" {
			return api.ResourceList{}, notFound()
		}
		if err != nil {
			return api.ResourceList{}, err
		}
		f.Stack = st.ID
	}
	rows, next, err := s.Store.ListResources(ctx, f)
	if errors.Is(err, store.ErrBadCursor) {
		return api.ResourceList{}, api.Validation([]api.FieldError{{Field: "cursor", Code: api.FieldInvalidFormat, Message: "Use the cursor from the previous page."}})
	}
	if err != nil {
		return api.ResourceList{}, err
	}
	out := api.ResourceList{Items: make([]api.Resource, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, r.ToAPI())
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// Create validates and stores a new resource, plus any managed resources it needs (L2-004 §1).
func (s *Resources) Create(ctx context.Context, p Principal, req api.CreateResourceRequest) (api.Resource, error) {
	return s.create(ctx, p, req, nil, "")
}

// create makes a resource; initialOutputs pre-set its outputs (a restore marker), and note is added to
// its created event.
func (s *Resources) create(ctx context.Context, p Principal, req api.CreateResourceRequest, initialOutputs map[string]any, note string) (api.Resource, error) {
	if req.Kind == api.KindStack {
		return s.createStack(ctx, p, req)
	}
	var r *domain.Resource
	var created []string
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		var err error
		r, created, err = s.createIn(ctx, q, p, req, createOpts{outputs: initialOutputs, note: note})
		return err
	})
	if err != nil {
		return api.Resource{}, err
	}
	for _, id := range created {
		s.Queue.Enqueue(id)
	}
	s.Log.Info("resource created", "resource_id", r.ID, "project", r.Project, "kind", r.Kind, "by", p.Key())
	return r.ToAPI(), nil
}

// createOpts are what a create inside another one adds: outputs to start with, a note for the created
// event, and the stack it is a member of (ADR-048).
type createOpts struct {
	outputs map[string]any
	note    string
	stack   string
}

// createIn validates and inserts a resource, with the managed resources it needs, in a transaction the
// caller owns. It returns the resource and the IDs to enqueue once the transaction commits.
func (s *Resources) createIn(ctx context.Context, q *store.Queries, p Principal, req api.CreateResourceRequest, o createOpts) (*domain.Resource, []string, error) {
	if !req.Kind.Valid() {
		return nil, nil, api.Validation([]api.FieldError{{Field: "kind", Code: api.FieldNotAllowed, Message: api.KindChoiceMessage}})
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = api.DefaultProject
	}
	if err := requireProject(p, project, api.RoleEditor); err != nil {
		return nil, nil, err
	}
	details := api.ValidateName(req.Name)
	spec, specErrs, err := api.NormalizeSpec(req.Kind, req.Spec)
	if err != nil {
		return nil, nil, api.NewError(api.CodeMalformedRequest, "The spec isn't valid JSON for a "+string(req.Kind)+": "+err.Error())
	}
	details = append(details, specErrs...)
	if len(details) > 0 {
		return nil, nil, api.Validation(details)
	}
	now := s.Now()
	r := &domain.Resource{ID: domain.NewResourceID(req.Kind), Project: project, Kind: req.Kind, Name: req.Name, Generation: 1, Phase: api.PhasePending,
		Steps: domain.NewSteps(domain.StepNames[req.Kind]...), LastTransitionAt: now, CreatedAt: now, UpdatedAt: now, Outputs: o.outputs, Stack: o.stack}
	if err := s.setSpec(r, spec); err != nil {
		return nil, nil, err
	}
	if !domain.Placed(r.Kind) {
		r.Machine = s.Store.MainID() // placed kinds get theirs from the reconciler (ADR-044)
	}
	var created []string
	if _, err := q.GetProject(ctx, project); errors.Is(err, store.ErrNotFound) {
		return nil, nil, api.Validation([]api.FieldError{{Field: "project", Code: api.FieldNotFound, Message: "There's no project named " + project + "."}})
	} else if err != nil {
		return nil, nil, err
	}
	if err := s.checkName(ctx, q, r, "name"); err != nil {
		return nil, nil, err
	}
	if err := checkMachine(ctx, q, spec); err != nil {
		return nil, nil, err
	}
	refs, err := s.resolveRefs(ctx, q, r, spec)
	if err != nil {
		return nil, nil, err
	}
	if err := insert(ctx, q, r, now, "created", fmt.Sprintf("Created by %s.%s", p.Actor(), o.note)); err != nil {
		return nil, nil, err
	}
	created = append(created, r.ID)
	if err := q.SetRefs(ctx, r.ID, refs); err != nil {
		return nil, nil, err
	}
	if domain.HasManagedPassword(r.Kind) {
		pw := strongPassword()
		if r.Kind == api.KindDatabase {
			pw = randomPassword()
		}
		id, err := s.createManagedSecret(ctx, q, r.Project, domain.PasswordSecretFor(r.Kind, r.Name), r.ID, pw, now)
		if err != nil {
			return nil, nil, err
		}
		created = append(created, id)
	}
	if domain.UsesBroker(r.Kind) { // the broker's own password, once (ADR-029)
		if _, err := q.GetResourceByName(ctx, api.DefaultProject, api.KindSecret, domain.BrokerCredentialsSecret); errors.Is(err, store.ErrNotFound) {
			id, err := s.createManagedSecret(ctx, q, api.DefaultProject, domain.BrokerCredentialsSecret, domain.SystemOwner, randomPassword(), now)
			if err != nil {
				return nil, nil, err
			}
			created = append(created, id)
		} else if err != nil {
			return nil, nil, err
		}
	}
	if r.Kind == api.KindBucket {
		// The store's root credentials live in default (ADR-035); every other project gets its own
		// user, limited to its buckets by the reconciler.
		for _, proj := range slices.Compact([]string{api.DefaultProject, r.Project}) {
			if _, err := q.GetResourceByName(ctx, proj, api.KindSecret, domain.ObjectStoreCredentialsSecret); errors.Is(err, store.ErrNotFound) {
				access := "enclave-" + randomPassword()[:8]
				if proj != api.DefaultProject {
					access = domain.ProjectStoreUser(proj)
				}
				creds, _ := json.Marshal(map[string]string{"accessKey": access, "secretKey": randomPassword()})
				id, err := s.createManagedSecret(ctx, q, proj, domain.ObjectStoreCredentialsSecret, domain.SystemOwner, string(creds), now)
				if err != nil {
					return nil, nil, err
				}
				created = append(created, id)
			} else if err != nil {
				return nil, nil, err
			}
		}
	}
	return r, created, nil
}

// setSpec stores the normalized spec; a secret's value goes to ciphertext, never to spec_json. A synced
// secret keeps its source in spec_json; the reconciler fills its ciphertext (ADR-041).
func (s *Resources) setSpec(r *domain.Resource, spec any) error {
	if sec, ok := spec.(api.SecretSpec); ok && sec.Source != nil {
		b, err := json.Marshal(api.SecretSpec{Source: sec.Source})
		if err != nil {
			return err
		}
		r.Spec = b
		return nil
	}
	if sec, ok := spec.(api.SecretSpec); ok {
		ct, err := s.Box.Seal([]byte(sec.Value), []byte(r.ID))
		if err != nil {
			return err
		}
		r.SecretCiphertext, r.Spec = ct, json.RawMessage(`{}`)
		return nil
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	r.Spec = b
	return nil
}

// checkName refuses a name its kind already uses in the project; bucket names are machine-wide, since the
// object store has one namespace (ADR-035).
func (s *Resources) checkName(ctx context.Context, q *store.Queries, r *domain.Resource, field string) error {
	if r.Kind == api.KindBucket {
		others, err := q.ResourcesNamed(ctx, api.KindBucket, r.Name)
		if err != nil {
			return err
		}
		for _, o := range others {
			if o.ProjectName() != r.ProjectName() {
				msg := fmt.Sprintf("A bucket named %s already exists in another project. Bucket names are shared by the whole machine.", r.Name)
				return &api.Error{Code: api.CodeNameTaken, Message: msg, Details: []api.FieldError{{Field: field, Code: api.FieldNotAllowed, Message: msg}}}
			}
		}
	}
	_, err := q.GetResourceByName(ctx, r.ProjectName(), r.Kind, r.Name)
	if err == nil {
		return nameTaken(r.Kind, r.Name, field)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func nameTaken(kind api.Kind, name, field string) error {
	return &api.Error{Code: api.CodeNameTaken, Message: fmt.Sprintf("A %s named %s already exists.", kind, name),
		Details: []api.FieldError{{Field: field, Code: api.FieldNotAllowed, Message: fmt.Sprintf("A %s named %s already exists.", kind, name)}}}
}

// requireProject checks the principal holds need in a project named by a request: one it can't see is
// reported as a field that names no project, not as a missing resource.
func requireProject(p Principal, project, need string) error {
	have := p.RoleIn(project)
	if have == "" {
		return api.Validation([]api.FieldError{{Field: "project", Code: api.FieldNotFound, Message: "There's no project named " + project + "."}})
	}
	if !api.RoleAtLeast(have, need) {
		return forbidden(need, project)
	}
	return nil
}

// Authorize checks the principal holds need in the project of resource id (ADR-036). The HTTP layer calls
// it for every route on one resource.
func (s *Resources) Authorize(ctx context.Context, p Principal, id, need string) error {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound()
	}
	if err != nil {
		return err
	}
	return p.Require(r.ProjectName(), need)
}

// resolveRefs turns names in a spec into references, failing on unknown names (CP-12): an app's
// secrets, a domain's app, a placed resource's network, a firewall's targets and peers, and so on.
func (s *Resources) resolveRefs(ctx context.Context, q *store.Queries, r *domain.Resource, spec any) ([]store.Ref, error) {
	project := r.ProjectName()
	switch sp := spec.(type) {
	case api.DomainSpec:
		refs, err := s.domainRefs(ctx, q, r, sp)
		if err != nil {
			return nil, err
		}
		zone, err := s.domainZoneRef(ctx, q, project, sp)
		return append(refs, zone...), err
	case api.AppSpec:
		if err := s.checkDomainPorts(ctx, q, r, sp); err != nil {
			return nil, err
		}
		if err := s.checkLoadBalancersFollow(ctx, q, r, sp); err != nil {
			return nil, err
		}
		refs, err := s.secretRefs(ctx, q, project, sp.SecretEnv)
		if err != nil {
			return nil, err
		}
		placed, err := s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, sp.Publish)
		if err != nil {
			return nil, err
		}
		refs = append(refs, placed...)
		mounted, err := s.mountRefs(ctx, q, project, sp.Volumes)
		if err != nil {
			return nil, err
		}
		refs = append(refs, mounted...)
		if sp.Source == nil || sp.Source.CredentialsSecret == "" {
			return refs, nil
		}
		// A source app references its credentials secret (L3-002 §6.6).
		cred, err := q.GetResourceByName(ctx, project, api.KindSecret, sp.Source.CredentialsSecret)
		if err != nil || cred.Phase == api.PhaseDeleting {
			return nil, api.Validation([]api.FieldError{{Field: "spec.source.credentialsSecret", Code: api.FieldNotFound,
				Message: fmt.Sprintf("There's no secret named %s.", sp.Source.CredentialsSecret)}})
		}
		return append(refs, store.Ref{ToID: cred.ID, Field: "spec.source.credentialsSecret"}), nil
	case api.DatabaseSpec:
		return s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, false)
	case api.CacheSpec:
		return s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, false)
	case api.BackupSpec:
		return s.backupRefs(ctx, q, project, sp)
	case api.JobSpec:
		refs, err := s.secretRefs(ctx, q, project, sp.SecretEnv)
		if err != nil {
			return nil, err
		}
		if sp.App != "" {
			app, err := q.GetResourceByName(ctx, project, api.KindApp, sp.App)
			if errors.Is(err, store.ErrNotFound) || (err == nil && app.Phase == api.PhaseDeleting) {
				return nil, api.Validation([]api.FieldError{{Field: "spec.app", Code: api.FieldNotFound, Message: "There's no app named " + sp.App + "."}})
			}
			if err != nil {
				return nil, err
			}
			refs = append(refs, store.Ref{ToID: app.ID, Field: "spec.app"})
		}
		placed, err := s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, false)
		if err != nil {
			return nil, err
		}
		mounted, err := s.mountRefs(ctx, q, project, sp.Volumes)
		return append(append(refs, placed...), mounted...), err
	case api.NetworkSpec:
		return nil, s.networkOverlaps(ctx, q, r, sp)
	case api.FirewallSpec:
		return s.firewallRefs(ctx, q, project, sp)
	case api.LoadBalancerSpec:
		return s.loadBalancerRefs(ctx, q, project, sp)
	case api.ZoneSpec:
		return s.zoneRefs(ctx, q, r, sp)
	case api.RecordSpec:
		return s.recordRefs(ctx, q, r, sp)
	case api.VolumeSpec:
		return s.volumeRefs(ctx, q, r, sp)
	case api.VMSpec:
		return s.vmRefs(ctx, q, r, sp)
	case api.SnapshotSpec:
		return nil, s.snapshotSourceCheck(ctx, q, project, sp)
	case api.QueueSpec, api.TopicSpec, api.SearchSpec, api.EmailSpec, api.TranscoderSpec, api.CDNSpec:
		return s.dataRefs(ctx, q, r, sp)
	case api.AlertSpec, api.ChannelSpec, api.LogExportSpec, api.SecretStoreSpec, api.SecretSpec:
		return s.operateRefs(ctx, q, r, sp)
	}
	return nil, nil
}

// domainRefs checks a domain's app and hostname (L2-005 DOM-02, DOM-06).
func (s *Resources) domainRefs(ctx context.Context, q *store.Queries, r *domain.Resource, d api.DomainSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	app, err := q.GetResourceByName(ctx, project, api.KindApp, d.App)
	if errors.Is(err, store.ErrNotFound) || (err == nil && app.Phase == api.PhaseDeleting) {
		return nil, api.Validation([]api.FieldError{{Field: "spec.app", Code: api.FieldNotFound, Message: "There's no app named " + d.App + "."}})
	}
	if err != nil {
		return nil, err
	}
	if d.Port == 0 {
		var as api.AppSpec
		if json.Unmarshal(app.Spec, &as) != nil || as.Port == 0 {
			return nil, api.Validation([]api.FieldError{{Field: "spec.port", Code: api.FieldRequired,
				Message: "The app " + d.App + " has no port. Give the app a port, or set the port here."}})
		}
	}
	if err := s.hostnameFree(ctx, q, r, d.Hostname); err != nil { // domains and CDNs share hostnames (CDN-01)
		return nil, err
	}
	return []store.Ref{{ToID: app.ID, Field: "spec.app"}}, nil
}

// checkDomainPorts refuses removing an app's port while a domain routes to it without its own port.
func (s *Resources) checkDomainPorts(ctx context.Context, q *store.Queries, r *domain.Resource, a api.AppSpec) error {
	if a.Port != 0 {
		return nil
	}
	deps, err := q.Dependants(ctx, r.ID)
	if err != nil {
		return err
	}
	for _, d := range deps {
		var ds api.DomainSpec
		if d.Kind == api.KindDomain && json.Unmarshal(d.Spec, &ds) == nil && ds.Port == 0 {
			return api.Validation([]api.FieldError{{Field: "spec.port", Code: api.FieldRequired,
				Message: "The domain " + d.Name + " routes to this app's port. Keep a port, or set one on the domain first."}})
		}
	}
	return nil
}

// backupRefs checks a backup's source and destination exist: they can't be deleted while it does (BAK-08).
func (s *Resources) backupRefs(ctx context.Context, q *store.Queries, project string, b api.BackupSpec) ([]store.Ref, error) {
	var refs []store.Ref
	var missing []api.FieldError
	need := func(kind api.Kind, name, field string) {
		res, err := q.GetResourceByName(ctx, project, kind, name)
		if err != nil || res.Phase == api.PhaseDeleting {
			missing = append(missing, api.FieldError{Field: field, Code: api.FieldNotFound, Message: fmt.Sprintf("There's no %s named %s.", kind, name)})
			return
		}
		refs = append(refs, store.Ref{ToID: res.ID, Field: field})
	}
	if b.Source.Database != "" {
		need(api.KindDatabase, b.Source.Database, "spec.source")
	} else {
		need(api.KindCache, b.Source.Cache, "spec.source")
	}
	if b.Destination.Bucket != "" {
		need(api.KindBucket, b.Destination.Bucket, "spec.destination.bucket")
	} else if b.Destination.S3 != nil {
		need(api.KindSecret, b.Destination.S3.CredentialsSecret, "spec.destination.s3.credentialsSecret")
	}
	if len(missing) > 0 {
		return nil, api.Validation(missing)
	}
	return refs, nil
}

// secretRefs turns secret environment names into references (apps and jobs).
func (s *Resources) secretRefs(ctx context.Context, q *store.Queries, project string, secretEnv map[string]string) ([]store.Ref, error) {
	var refs []store.Ref
	var missing []api.FieldError
	for key, name := range secretEnv {
		sec, err := q.GetResourceByName(ctx, project, api.KindSecret, name)
		if errors.Is(err, store.ErrNotFound) {
			missing = append(missing, api.FieldError{Field: "spec.secretEnv." + key, Code: api.FieldNotFound, Message: "There's no secret named " + name + "."})
			continue
		}
		if err != nil {
			return nil, err
		}
		if sec.Phase == api.PhaseDeleting {
			missing = append(missing, api.FieldError{Field: "spec.secretEnv." + key, Code: api.FieldNotFound, Message: "The secret " + name + " is being deleted."})
			continue
		}
		refs = append(refs, store.Ref{ToID: sec.ID, Field: "spec.secretEnv." + key})
	}
	if len(missing) > 0 {
		return nil, api.Validation(missing)
	}
	return refs, nil
}

func (s *Resources) createManagedSecret(ctx context.Context, q *store.Queries, project, name, owner, value string, now time.Time) (string, error) {
	sec := &domain.Resource{ID: domain.NewResourceID(api.KindSecret), Project: project, Kind: api.KindSecret, Name: name, Generation: 1, Phase: api.PhasePending,
		Steps: domain.NewSteps(domain.StepNames[api.KindSecret]...), OwnerID: &owner, LastTransitionAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.checkName(ctx, q, sec, "name"); err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			ae.Message = fmt.Sprintf("A secret named %s already exists, and this resource needs that name for its managed credentials. Rename or delete it first.", name)
			ae.Details[0].Message = ae.Message
		}
		return "", err
	}
	if err := s.setSpec(sec, api.SecretSpec{Value: value}); err != nil {
		return "", err
	}
	return sec.ID, insert(ctx, q, sec, now, "created", "Created and managed by EnclaveStack.")
}

func insert(ctx context.Context, q *store.Queries, r *domain.Resource, now time.Time, event, message string) error {
	if err := q.InsertResource(ctx, r); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nameTaken(r.Kind, r.Name, "name")
		}
		return err
	}
	return q.AddEvent(ctx, r.ID, event, message, now)
}

func randomPassword() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func checkIfMatch(r *domain.Resource, ifMatch *int64) error {
	if ifMatch != nil && *ifMatch != r.Generation {
		return api.NewError(api.CodePreconditionFailed, "This resource changed since you read it. Reload and try again.")
	}
	return nil
}

// Update applies a spec patch. It returns changed=false for a no-op patch (200 instead of 202).
func (s *Resources) Update(ctx context.Context, p Principal, id string, req api.UpdateResourceRequest, ifMatch *int64) (api.Resource, bool, error) {
	if r, err := s.Store.GetResource(ctx, id); err == nil && r.Kind == api.KindStack {
		return s.updateStack(ctx, p, r, req, ifMatch)
	}
	var out *domain.Resource
	changed := false
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		var err error
		out, changed, err = s.updateIn(ctx, q, p, id, req, ifMatch, false)
		return err
	})
	if err != nil {
		return api.Resource{}, false, err
	}
	if changed {
		s.Queue.Enqueue(out.ID)
		s.Log.Info("resource updated", "resource_id", out.ID, "generation", out.Generation, "by", p.Key())
	}
	return out.ToAPI(), changed, nil
}

// updateIn applies a spec patch in a transaction the caller owns; fromStack is the stack changing its own
// member (ADR-048), which no one else may.
func (s *Resources) updateIn(ctx context.Context, q *store.Queries, p Principal, id string, req api.UpdateResourceRequest, ifMatch *int64, fromStack bool) (*domain.Resource, bool, error) {
	r, err := q.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, notFound()
	}
	if err != nil {
		return nil, false, err
	}
	if r.Phase == api.PhaseDeleting {
		return nil, false, api.NewError(api.CodeResourceBusy, "This resource is being deleted.")
	}
	if err := checkIfMatch(r, ifMatch); err != nil {
		return nil, false, err
	}
	if r.Stack != "" && !fromStack {
		return nil, false, partOfStack(ctx, q, r)
	}
	if r.OwnerID != nil {
		return nil, false, &api.Error{Code: api.CodeImmutableField, Message: "EnclaveStack manages this secret; it changes with its owner.",
			Details: []api.FieldError{{Field: "spec", Code: api.FieldImmutable, Message: "Managed secrets can't be edited."}}}
	}
	if len(bytes.TrimSpace(req.Spec)) == 0 || bytes.Equal(bytes.TrimSpace(req.Spec), []byte("null")) {
		return nil, false, api.Validation([]api.FieldError{{Field: "spec", Code: api.FieldRequired, Message: "Send the fields to change."}})
	}
	before, _, err := api.NormalizeSpec(r.Kind, s.storedSpecForMerge(r))
	if err != nil {
		return nil, false, err
	}
	merged, err := api.MergeSpec(s.storedSpecForMerge(r), req.Spec)
	if err != nil {
		return nil, false, api.NewError(api.CodeMalformedRequest, "The spec isn't valid JSON: "+err.Error())
	}
	after, details, err := api.NormalizeSpec(r.Kind, merged)
	if err != nil {
		return nil, false, api.NewError(api.CodeMalformedRequest, "The spec isn't valid for a "+string(r.Kind)+": "+err.Error())
	}
	if len(details) > 0 {
		return nil, false, api.Validation(details)
	}
	if v := api.ImmutableViolations(r.Kind, before, after); len(v) > 0 {
		return nil, false, &api.Error{Code: api.CodeImmutableField, Message: v[0].Message, Details: v}
	}
	if v := api.GrowOnlyViolations(r.Kind, before, after); len(v) > 0 {
		return nil, false, api.Validation(v)
	}
	if specEqual(before, after) {
		return r, false, nil
	}
	if err := checkMachine(ctx, q, after); err != nil {
		return nil, false, err
	}
	if err := s.checkMove(ctx, q, r, before, after); err != nil {
		return nil, false, err
	}
	refs, err := s.resolveRefs(ctx, q, r, after)
	if err != nil {
		return nil, false, err
	}
	if err := s.setSpec(r, after); err != nil {
		return nil, false, err
	}
	now := s.Now()
	r.Generation++
	target := api.PhaseUpdating
	if r.ObservedGeneration == 0 {
		target = api.PhaseProvisioning
	}
	if err := r.SetPhase(target, "", now); err != nil {
		return nil, false, err
	}
	r.Attempts, r.NextAttemptAt, r.UpdatedAt = 0, nil, now
	r.Steps = domain.NewSteps(domain.StepNames[r.Kind]...)
	if err := q.SaveResource(ctx, r); err != nil {
		return nil, false, err
	}
	if err := q.SetRefs(ctx, r.ID, refs); err != nil {
		return nil, false, err
	}
	return r, true, q.AddEvent(ctx, r.ID, "updated", fmt.Sprintf("Spec changed to generation %d by %s.", r.Generation, p.Actor()), now)
}

// storedSpecForMerge returns the spec to merge a patch into; for secrets, the decrypted value, or the
// source of a synced secret (whose value can't be set, EXT-06).
func (s *Resources) storedSpecForMerge(r *domain.Resource) json.RawMessage {
	if r.Kind != api.KindSecret {
		return r.Spec
	}
	var stored api.SecretSpec
	if json.Unmarshal(r.Spec, &stored) == nil && stored.Source != nil {
		return r.Spec
	}
	v, err := s.Box.Open(r.SecretCiphertext, []byte(r.ID))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(api.SecretSpec{Value: string(v)})
	return b
}

func specEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// Delete starts deleting a resource after checking dependants (CP-12).
func (s *Resources) Delete(ctx context.Context, p Principal, id string, ifMatch *int64) (api.Resource, error) {
	var out *domain.Resource
	queued := []string{id}
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if err := checkIfMatch(r, ifMatch); err != nil {
			return err
		}
		if r.Phase == api.PhaseDeleting {
			out = r
			return nil
		}
		if r.Stack != "" {
			return partOfStack(ctx, q, r)
		}
		if r.Kind == api.KindStack {
			ids, err := s.deleteStackIn(ctx, q, p, r)
			queued, out = ids, r
			return err
		}
		if r.OwnerID != nil {
			owner := "EnclaveStack"
			if *r.OwnerID != domain.SystemOwner {
				if o, err := q.GetResource(ctx, *r.OwnerID); err == nil {
					owner = fmt.Sprintf("the %s %s", o.Kind, o.Name)
				}
			}
			return &api.Error{Code: api.CodeResourceInUse, Message: fmt.Sprintf("This secret is managed by %s and is deleted with it.", owner)}
		}
		deps, err := q.Dependants(ctx, r.ID)
		if err != nil {
			return err
		}
		if len(deps) > 0 {
			e := &api.Error{Code: api.CodeResourceInUse, Message: fmt.Sprintf("%s is still used. Remove it from these first.", r.Name)}
			for _, d := range deps {
				e.Details = append(e.Details, api.FieldError{Field: string(d.Kind) + "/" + d.Name, Code: api.FieldNotAllowed, Message: fmt.Sprintf("Used by the %s %s.", d.Kind, d.Name)})
			}
			return e
		}
		now := s.Now()
		if err := r.SetPhase(api.PhaseDeleting, "", now); err != nil {
			return err
		}
		r.Attempts, r.NextAttemptAt, r.RequestedAction, r.UpdatedAt = 0, nil, "", now
		if err := q.SaveResource(ctx, r); err != nil {
			return err
		}
		out = r
		return q.AddEvent(ctx, r.ID, "deleting", "Deletion requested by "+p.Actor()+".", now)
	})
	if err != nil {
		return api.Resource{}, err
	}
	for _, q := range queued {
		s.Queue.Enqueue(q)
	}
	s.Log.Info("resource deletion requested", "resource_id", out.ID, "by", p.Key())
	return out.ToAPI(), nil
}

// Action records restart, redeploy or reconcile for the reconciler (CP-08, CP-11).
func (s *Resources) Action(ctx context.Context, p Principal, id, action string) (api.Resource, error) {
	if action == api.ActionStart || action == api.ActionStop {
		return s.power(ctx, p, id, action)
	}
	if action == api.ActionRefresh {
		return s.refresh(ctx, p, id)
	}
	var out *domain.Resource
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if r.Phase == api.PhaseDeleting {
			return api.NewError(api.CodeResourceBusy, "This resource is being deleted.")
		}
		now := s.Now()
		switch action {
		case api.ActionRestart, api.ActionRedeploy, api.ActionPurge:
			ok := (action == api.ActionRestart && slices.Contains(restartable, r.Kind)) ||
				(action == api.ActionRedeploy && r.Kind == api.KindApp) ||
				(action == api.ActionPurge && (r.Kind == api.KindQueue || r.Kind == api.KindCDN))
			if !ok {
				return api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s can't be %s.", r.Kind, pastTense(action)))
			}
			if r.Phase != api.PhaseReady {
				return api.NewError(api.CodeActionNotSupported, "Wait until it's ready, or use Retry if it failed.")
			}
			r.RequestedAction = action
			if action == api.ActionRedeploy {
				if err := r.SetPhase(api.PhaseUpdating, "Redeploying", now); err != nil {
					return err
				}
				r.Steps = domain.NewSteps(domain.StepNames[r.Kind]...)
			}
		case api.ActionReconcile:
			target := api.PhaseProvisioning
			if r.ObservedGeneration > 0 {
				target = api.PhaseUpdating
			}
			if r.Phase == api.PhaseReady {
				target = api.PhaseProvisioning
			}
			if err := r.SetPhase(target, "", now); err != nil {
				return err
			}
			r.Steps = domain.NewSteps(domain.StepNames[r.Kind]...)
		default:
			return api.NewError(api.CodeNotFound, "Unknown action. Use restart, redeploy, reconcile, purge, run, start, stop, test or refresh.")
		}
		r.Attempts, r.NextAttemptAt, r.UpdatedAt = 0, nil, now
		if err := q.SaveResource(ctx, r); err != nil {
			return err
		}
		out = r
		return q.AddEvent(ctx, r.ID, "action_"+action, fmt.Sprintf("%s requested by %s.", capitalize(action), p.Actor()), now)
	})
	if err != nil {
		return api.Resource{}, err
	}
	s.Queue.Enqueue(out.ID)
	return out.ToAPI(), nil
}

func pastTense(action string) string {
	switch action {
	case api.ActionRestart:
		return "restarted"
	case api.ActionRedeploy:
		return "redeployed"
	case api.ActionPurge:
		return "purged"
	}
	return action + "ed"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// Events returns a resource's history.
func (s *Resources) Events(ctx context.Context, id string, limit int) (api.EventList, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return api.EventList{}, err
	}
	items, err := s.Store.ListEvents(ctx, id, limit)
	return api.EventList{Items: items}, err
}

// Logs returns the last lines of a resource's container output.
func (s *Resources) Logs(ctx context.Context, id string, tail int, buildLog bool) (api.LogList, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.LogList{}, notFound()
	}
	if err != nil {
		return api.LogList{}, err
	}
	if buildLog {
		return s.buildLog(ctx, r, tail)
	}
	if r.Kind == api.KindJob {
		return api.LogList{}, api.NewError(api.CodeActionNotSupported, "A job's output is kept with each run: see its runs.")
	}
	if r.Kind == api.KindBackup {
		return api.LogList{}, api.NewError(api.CodeActionNotSupported, "A backup's results are kept with each run: see its runs.")
	}
	if r.Kind == api.KindTranscoder {
		return api.LogList{}, api.NewError(api.CodeActionNotSupported, "A transcoder's output is kept with each run: see its runs.")
	}
	if slices.Contains([]api.Kind{api.KindBucket, api.KindSecret, api.KindDomain, api.KindVolume, api.KindSnapshot, api.KindQueue, api.KindTopic,
		api.KindDashboard, api.KindAlert, api.KindChannel, api.KindLogExport, api.KindSecretStore}, r.Kind) {
		return api.LogList{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no logs.", r.Kind))
	}
	if r.Kind == api.KindVM { // the boot console (VM-09)
		var lines []api.LogLine
		if s.Hypervisor != nil {
			if lines, err = s.Hypervisor.ConsoleLog(ctx, r.ID, tail); err != nil {
				s.Log.Warn("read the boot console failed", "resource_id", id, "error", err)
			}
		}
		if lines == nil {
			lines = []api.LogLine{}
		}
		return api.LogList{Items: lines}, nil
	}
	lines, err := s.Engine.Logs(ctx, r, tail)
	if errors.Is(err, engine.ErrUnavailable) {
		return api.LogList{}, api.NewError(api.CodeEngineUnavailable, "The container engine isn't running, so logs can't be read right now.")
	}
	if err != nil {
		s.Log.Warn("read logs failed", "resource_id", id, "error", err)
		return api.LogList{Items: []api.LogLine{}}, nil
	}
	if lines == nil {
		lines = []api.LogLine{}
	}
	return api.LogList{Items: lines}, nil
}

// RevealSecret decrypts a secret's value and records who revealed it (CP-15).
func (s *Resources) RevealSecret(ctx context.Context, p Principal, id string) (api.SecretValue, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.SecretValue{}, notFound()
	}
	if err != nil {
		return api.SecretValue{}, err
	}
	if r.Kind != api.KindSecret {
		return api.SecretValue{}, api.NewError(api.CodeActionNotSupported, "Only secrets have a value to reveal.")
	}
	v, err := s.Box.Open(r.SecretCiphertext, []byte(r.ID))
	if err != nil {
		return api.SecretValue{}, fmt.Errorf("open secret %s: %w", r.ID, err)
	}
	if err := s.Store.AddEvent(ctx, r.ID, "secret_revealed", "Value revealed by "+p.Actor()+".", s.Now()); err != nil {
		return api.SecretValue{}, err
	}
	s.Log.Info("secret revealed", "resource_id", r.ID, "by", p.Key())
	writeAudit(ctx, s.Store, s.Now(), s.Log, p, "secret_revealed", r.ProjectName()+"/"+r.Name, "")
	return api.SecretValue{Value: string(v)}, nil
}

// RunNow starts a run of a job now (the run action, JOB-07). The scheduler picks it up within a tick.
func (s *Resources) RunNow(ctx context.Context, p Principal, id string) (api.Run, error) {
	var run *domain.Run
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if r.Kind == api.KindTranscoder {
			return api.Validation([]api.FieldError{{Field: "key", Code: api.FieldRequired, Message: "Give the object's key to transcode, like videos/intro.mov."}})
		}
		if r.Kind != api.KindJob && r.Kind != api.KindBackup {
			return api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s can't be run.", r.Kind))
		}
		if r.Phase != api.PhaseReady {
			return api.NewError(api.CodeActionNotSupported, fmt.Sprintf("Wait until the %s is ready, or use Retry if it failed.", r.Kind))
		}
		raw, _, err := api.NormalizeSpec(r.Kind, r.Spec)
		if err != nil {
			return err
		}
		if js, ok := raw.(api.JobSpec); !ok || js.Overlap == api.OverlapForbid { // backups never overlap
			busy, err := q.HasRunningRun(ctx, id)
			if err != nil {
				return err
			}
			if busy {
				return api.NewError(api.CodeRunInProgress, fmt.Sprintf("A run of this %s is still going. Wait for it to finish.", r.Kind))
			}
		}
		now := s.Now()
		run = &domain.Run{ID: domain.NewID("run"), ResourceID: id, Trigger: api.TriggerManual, Status: api.RunRunning, StartedAt: now}
		if err := q.InsertRun(ctx, run); err != nil {
			return err
		}
		return q.AddEvent(ctx, id, "action_run", fmt.Sprintf("Run %s requested by %s.", run.ID, p.Actor()), now)
	})
	if err != nil {
		return api.Run{}, err
	}
	s.Log.Info("run requested", "resource_id", id, "run_id", run.ID, "by", p.Key())
	return run.ToAPI(), nil
}

// Runs lists a job's runs, newest first.
func (s *Resources) Runs(ctx context.Context, id string, limit int, cursor string) (api.RunList, error) {
	if _, err := s.schedulable(ctx, id); err != nil {
		return api.RunList{}, err
	}
	runs, next, err := s.Store.ListRuns(ctx, id, limit, cursor)
	if errors.Is(err, store.ErrBadCursor) {
		return api.RunList{}, api.NewError(api.CodeMalformedRequest, "That cursor isn't valid. Start from the first page.")
	}
	if err != nil {
		return api.RunList{}, err
	}
	out := api.RunList{Items: make([]api.Run, 0, len(runs))}
	for _, r := range runs {
		out.Items = append(out.Items, r.ToAPI())
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// Run returns one run with its output tail.
func (s *Resources) Run(ctx context.Context, id, runID string) (api.RunDetail, error) {
	if _, err := s.schedulable(ctx, id); err != nil {
		return api.RunDetail{}, err
	}
	r, err := s.Store.GetRun(ctx, id, runID)
	if errors.Is(err, store.ErrNotFound) {
		return api.RunDetail{}, api.NewError(api.CodeNotFound, "That run doesn't exist, or was removed by retention.")
	}
	if err != nil {
		return api.RunDetail{}, err
	}
	return api.RunDetail{Run: r.ToAPI(), OutputTail: string(r.OutputTail)}, nil
}

func (s *Resources) schedulable(ctx context.Context, id string) (*domain.Resource, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if r.Kind != api.KindJob && r.Kind != api.KindBackup && r.Kind != api.KindTranscoder {
		return nil, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no runs.", r.Kind))
	}
	return r, nil
}

// Restore restores a backup run (BAK-06, BAK-07): into a new database or cache (the default), or over
// the source after its name is typed (a safety backup is taken first). It returns the resource being
// restored into; the reconciler does the work.
func (s *Resources) Restore(ctx context.Context, p Principal, id string, req api.RestoreRequest) (api.Resource, error) {
	if errs := api.ValidateRestore(req); len(errs) > 0 {
		return api.Resource{}, api.Validation(errs)
	}
	b, err := s.schedulable(ctx, id)
	if err != nil {
		return api.Resource{}, err
	}
	if b.Kind != api.KindBackup {
		return api.Resource{}, api.NewError(api.CodeActionNotSupported, "Only backups can be restored.")
	}
	run, err := s.Store.GetRun(ctx, id, req.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return api.Resource{}, api.NewError(api.CodeRunNotRestorable, "That backup run doesn't exist, or was removed by retention.")
	}
	if err != nil {
		return api.Resource{}, err
	}
	if run.Status != api.RunSucceeded {
		return api.Resource{}, api.NewError(api.CodeRunNotRestorable, "Only a successful backup run can be restored.")
	}
	if run.Artifact == nil {
		return api.Resource{}, api.NewError(api.CodeRunNotRestorable, "This run's backup was removed by retention. Choose a newer run.")
	}
	raw, _, err := api.NormalizeSpec(api.KindBackup, b.Spec)
	if err != nil {
		return api.Resource{}, err
	}
	spec := raw.(api.BackupSpec)
	kind, srcName := api.KindDatabase, spec.Source.Database
	if spec.Source.Cache != "" {
		kind, srcName = api.KindCache, spec.Source.Cache
	}
	src, err := s.Store.GetResourceByName(ctx, b.Project, kind, srcName)
	if err != nil {
		return api.Resource{}, api.NewError(api.CodeRunNotRestorable, fmt.Sprintf("The %s %s no longer exists. Restore into a new one instead.", kind, srcName))
	}
	marker := map[string]any{"backupId": b.ID, "runId": run.ID, "replace": req.Mode == api.RestoreReplace}
	from := fmt.Sprintf(" Restoring from backup %s run %s.", b.Name, run.ID)
	if req.Mode == api.RestoreNew {
		// Same engine and settings as the source; the restore runs once it is up.
		return s.create(ctx, p, api.CreateResourceRequest{Kind: kind, Name: req.Name, Project: b.ProjectName(), Spec: src.Spec}, map[string]any{"pendingRestore": marker}, from)
	}
	if req.ConfirmName != src.Name {
		return api.Resource{}, api.Validation([]api.FieldError{{Field: "confirmName", Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("Type %s to confirm that its data will be replaced.", src.Name)}})
	}
	var out *domain.Resource
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		cur, err := q.GetResource(ctx, src.ID)
		if err != nil {
			return err
		}
		if cur.Phase != api.PhaseReady {
			return api.NewError(api.CodeActionNotSupported, fmt.Sprintf("Wait until the %s %s is ready.", cur.Kind, cur.Name))
		}
		now := s.Now()
		if err := cur.SetPhase(api.PhaseUpdating, "Restoring from backup "+b.Name+".", now); err != nil {
			return err
		}
		cur.Outputs = withOutputs(cur.Outputs, map[string]any{"pendingRestore": marker})
		cur.Steps, cur.Attempts, cur.NextAttemptAt, cur.UpdatedAt = domain.NewSteps(domain.StepNames[cur.Kind]...), 0, nil, now
		if err := q.SaveResource(ctx, cur); err != nil {
			return err
		}
		out = cur
		return q.AddEvent(ctx, cur.ID, "restore_requested", fmt.Sprintf("Replace its data with backup %s run %s, requested by %s. A safety backup is taken first.", b.Name, run.ID, p.Actor()), now)
	})
	if err != nil {
		return api.Resource{}, err
	}
	s.Queue.Enqueue(out.ID)
	s.Log.Info("restore requested", "backup_id", b.ID, "run_id", run.ID, "target", out.ID, "mode", req.Mode, "by", p.Key())
	return out.ToAPI(), nil
}

func withOutputs(base map[string]any, add map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(add))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// Machine describes the host and engine (CP-16).
func (s *Resources) Machine(ctx context.Context, name string) (api.Machine, error) {
	host, _ := os.Hostname()
	m := api.Machine{ID: s.Store.MainID(), Name: name, Hostname: host, Version: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		Engine: api.Engine{Driver: s.Engine.Name()}}
	if info, err := s.Engine.Info(ctx); err == nil {
		m.Engine.Available, m.Engine.Version = true, info.Version
		m.MemoryTotalBytes = info.MemoryTotalBytes
		if info.CPUs > 0 {
			m.CPUs = info.CPUs
		}
	} else {
		m.Engine.Message = "The container engine isn't reachable. Changes are queued and will apply when it's back."
	}
	if mc, err := s.Store.GetMachine(ctx, m.ID); err == nil {
		m.Handle = mc.Name
	}
	all, err := s.Store.AllResources(ctx)
	if err != nil {
		return m, err
	}
	// GET /machine describes the main machine (L3-002 §11.2): what is placed there.
	m.MemoryReservedBytes = int64(domain.ReservedByMachine(all, s.Store.MainID())[s.Store.MainID()]) << 20
	for _, r := range all {
		if r.Phase != api.PhaseDeleting && r.MachineOn(s.Store.MainID()) == s.Store.MainID() {
			m.ReservedCPUs += domain.CPUsReserved(r)
		}
	}
	s.vmFacts(ctx, &m)
	s.brokerFacts(ctx, &m)
	if m.PublicAddress, err = s.setting(ctx, store.SettingPublicAddress); err != nil {
		return m, err
	}
	if m.AcmeEmail, err = s.setting(ctx, store.SettingACMEEmail); err != nil {
		return m, err
	}
	if m.InviteEmail, err = InviteEmailSetting(ctx, s.Store); err != nil {
		return m, err
	}
	edge := s.Engine.EdgeStatus(ctx)
	m.Edge = api.Edge{Available: edge.Available, Message: edge.Message}
	return m, nil
}

func (s *Resources) setting(ctx context.Context, key string) (*string, error) {
	v, err := s.Store.GetSetting(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// UpdateMachine changes the machine's settings (PATCH /machine); a null or empty value clears one.
// Domains, and records that point at the machine, are re-checked so they use the new values.
func (s *Resources) UpdateMachine(ctx context.Context, p Principal, name string, req api.MachineSettings) (api.Machine, error) {
	if !p.IsMachineAdmin() {
		return api.Machine{}, api.NewError(api.CodeForbidden, "Only machine admins change the machine's settings.")
	}
	for _, v := range []*string{req.PublicAddress, req.AcmeEmail} {
		if v != nil {
			*v = strings.TrimSpace(*v)
		}
	}
	if ie := req.InviteEmail; ie != nil {
		ie.Project, ie.Service, ie.From = strings.TrimSpace(ie.Project), strings.TrimSpace(ie.Service), strings.TrimSpace(ie.From)
		if ie.Project == "" {
			ie.Project = api.DefaultProject
		}
	}
	if errs := api.ValidateMachineSettings(req); len(errs) > 0 {
		return api.Machine{}, api.Validation(errs)
	}
	var inviteEmail []byte
	if ie := req.InviteEmail; ie != nil {
		if _, err := s.Store.GetResourceByName(ctx, ie.Project, api.KindEmail, ie.Service); errors.Is(err, store.ErrNotFound) {
			msg := fmt.Sprintf("There's no email service named %s in the project %s.", ie.Service, ie.Project)
			return api.Machine{}, api.Validation([]api.FieldError{{Field: "inviteEmail.service", Code: api.FieldNotFound, Message: msg}})
		} else if err != nil {
			return api.Machine{}, err
		}
		inviteEmail, _ = json.Marshal(ie)
	}
	var affected []*domain.Resource
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		switch {
		case inviteEmail != nil:
			if err := q.SetSetting(ctx, store.SettingInviteEmail, string(inviteEmail)); err != nil {
				return err
			}
		case req.ClearInviteEmail:
			if err := q.DeleteSetting(ctx, store.SettingInviteEmail); err != nil {
				return err
			}
		}
		for key, v := range map[string]*string{store.SettingPublicAddress: req.PublicAddress, store.SettingACMEEmail: req.AcmeEmail} {
			var err error
			if v == nil || *v == "" {
				err = q.DeleteSetting(ctx, key)
			} else {
				err = q.SetSetting(ctx, key, *v)
			}
			if err != nil {
				return err
			}
		}
		var err error
		if affected, err = q.ResourcesOfKind(ctx, api.KindDomain); err != nil {
			return err
		}
		// Records that point at the machine follow its public address (L2-006 DNS-03).
		records, err := q.ResourcesOfKind(ctx, api.KindRecord)
		for _, rec := range records {
			var rs api.RecordSpec
			if json.Unmarshal(rec.Spec, &rs) == nil && rs.Machine {
				affected = append(affected, rec)
			}
		}
		return err
	})
	if err != nil {
		return api.Machine{}, err
	}
	s.Log.Info("machine settings updated", "by", p.Key())
	for _, d := range affected {
		if d.Phase == api.PhaseReady || d.Phase == api.PhaseFailed {
			if _, err := s.Action(ctx, p, d.ID, api.ActionReconcile); err != nil {
				s.Log.Warn("re-check after settings change failed", "resource_id", d.ID, "error", err)
			}
		}
	}
	return s.Machine(ctx, name)
}

// RootCA returns the internal CA's root certificate (GET /machine/ca).
func (s *Resources) RootCA(ctx context.Context) ([]byte, error) {
	pem, err := s.Engine.RootCA(ctx)
	switch {
	case errors.Is(err, engine.ErrNoCA):
		return nil, api.NewError(api.CodeNotFound, "There's no internal certificate authority yet. It's created with the first domain that uses internal TLS.")
	case errors.Is(err, engine.ErrUnavailable):
		return nil, api.NewError(api.CodeEngineUnavailable, "The container engine isn't running, so the certificate can't be read right now.")
	}
	return pem, err
}

// Idempotency stores and replays responses for Idempotency-Key requests (ADR-010).
type Idempotency struct {
	Store *store.Store
	Now   func() time.Time
}

// RequestHash fingerprints a request so a reused key with a different body is detected.
func RequestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method + " " + path + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup returns a stored response for the key; found=false when there is none.
func (i *Idempotency) Lookup(ctx context.Context, p Principal, key, hash string) (status int, body []byte, found bool, err error) {
	rec, err := i.Store.GetIdempotency(ctx, p.Key(), key, i.Now().Add(-IdempotencyTTL))
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	if rec.RequestHash != hash {
		return 0, nil, false, api.NewError(api.CodeIdempotencyKeyReused, "This Idempotency-Key was already used for a different request.")
	}
	return rec.Status, rec.Body, true, nil
}

// Remember stores a response for the key.
func (i *Idempotency) Remember(ctx context.Context, p Principal, key, hash string, status int, body []byte) error {
	return i.Store.PutIdempotency(ctx, store.Idempotency{Principal: p.Key(), Key: key, RequestHash: hash, Status: status, Body: body, CreatedAt: i.Now()})
}

// buildLog returns the end of a source app's latest build log (L3-002 §6.3, ADR-019).
func (s *Resources) buildLog(ctx context.Context, r *domain.Resource, tail int) (api.LogList, error) {
	if r.Kind != api.KindApp {
		return api.LogList{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no builds.", r.Kind))
	}
	b, err := s.Store.LatestBuild(ctx, r.ID)
	if errors.Is(err, store.ErrNotFound) {
		return api.LogList{Items: []api.LogLine{}}, nil
	}
	if err != nil {
		return api.LogList{}, err
	}
	at := b.StartedAt
	if b.FinishedAt != nil {
		at = *b.FinishedAt
	}
	lines := strings.Split(strings.TrimRight(string(b.Log), "\n"), "\n")
	if len(b.Log) == 0 {
		lines = nil
	}
	if b.Status == "failed" && b.Message != "" {
		lines = append(lines, "ERROR: "+b.Message)
	}
	lines = lines[max(0, len(lines)-tail):]
	items := make([]api.LogLine, 0, len(lines))
	for _, l := range lines {
		items = append(items, api.LogLine{Time: at, Stream: "stdout", Message: l})
	}
	return api.LogList{Items: items}, nil
}
