package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/enclavestack/platform/internal/catalogue"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Stacks (ADR-048, L2-010 CAT-04 – CAT-09): a stack renders its template and creates every member with the
// same code path as a hand-made create, in one transaction, so nothing exists unless all of it is valid.

// keptSecret stands for a secret parameter's value that stays as it is on an update.
const keptSecret = "\x00kept"

func partOfStack(ctx context.Context, q *store.Queries, r *domain.Resource) error {
	name := r.Stack
	if st, err := q.GetResource(ctx, r.Stack); err == nil {
		name = st.Name
	}
	return api.NewError(api.CodePartOfStack, fmt.Sprintf("%s is part of the stack %s. Change or delete the stack instead.", r.Name, name))
}

// stackTemplate finds a stack's template: inline, built in, or one of the machine's own.
func (s *Resources) stackTemplate(ctx context.Context, q *store.Queries, spec api.StackSpec) (*catalogue.Template, error) {
	if spec.TemplateBody != "" {
		t, err := catalogue.Parse([]byte(spec.TemplateBody))
		if err != nil {
			return nil, templateError(err, "spec.templateBody")
		}
		return t, nil
	}
	if t, ok := catalogue.Builtin(spec.Template); ok {
		if spec.Version != "" && spec.Version != t.Version {
			return nil, api.Validation([]api.FieldError{{Field: "spec.version", Code: api.FieldNotFound,
				Message: fmt.Sprintf("%s has the version %s, not %s.", t.Title, t.Version, spec.Version)}})
		}
		return t, nil
	}
	versions, err := q.TemplateVersions(ctx, spec.Template)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, api.Validation([]api.FieldError{{Field: "spec.template", Code: api.FieldNotFound, Message: "There's no catalogue entry named " + spec.Template + "."}})
	}
	row := versions[len(versions)-1]
	if spec.Version != "" {
		i := slices.IndexFunc(versions, func(v store.CatalogueTemplate) bool { return v.Version == spec.Version })
		if i < 0 {
			return nil, api.Validation([]api.FieldError{{Field: "spec.version", Code: api.FieldNotFound, Message: spec.Template + " has no version " + spec.Version + "."}})
		}
		row = versions[i]
	}
	t, err := catalogue.Parse([]byte(row.Body))
	if err != nil {
		return nil, templateError(err, "spec.template")
	}
	return t, nil
}

// templateError turns a template's problems into a validation error.
func templateError(err error, prefix string) error {
	var ps catalogue.Problems
	if errors.As(err, &ps) {
		return api.Validation(ps.FieldErrors(prefix))
	}
	return err
}

// memberError names the member in a member's validation error: "members.blog-db.spec.version" (CAT-08).
func memberError(err error, name string) error {
	var ae *api.Error
	if !errors.As(err, &ae) {
		return err
	}
	out := *ae
	out.Message = name + ": " + ae.Message
	out.Details = make([]api.FieldError, len(ae.Details))
	for i, d := range ae.Details {
		d.Field = "members." + name + "." + d.Field
		out.Details[i] = d
	}
	return &out
}

// splitStackSpec takes the write-only secret parameters out of a stack spec.
func splitStackSpec(raw json.RawMessage) (api.StackSpec, json.RawMessage, map[string]string, error) {
	var spec api.StackSpec
	if err := api.DecodeStrict(raw, &spec); err != nil {
		return spec, nil, nil, api.NewError(api.CodeMalformedRequest, "The spec isn't valid JSON for a stack: "+err.Error())
	}
	secrets := spec.SecretParameters
	spec.SecretParameters = nil
	b, err := json.Marshal(spec)
	return spec, b, secrets, err
}

// createStack makes a stack and all its members (CAT-04).
func (s *Resources) createStack(ctx context.Context, p Principal, req api.CreateResourceRequest) (api.Resource, error) {
	spec, stored, secrets, err := splitStackSpec(req.Spec)
	if err != nil {
		return api.Resource{}, err
	}
	req.Spec = stored
	var st *domain.Resource
	var created []string
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		var err error
		st, created, err = s.createIn(ctx, q, p, req, createOpts{})
		if err != nil {
			return err
		}
		tpl, err := s.stackTemplate(ctx, q, spec)
		if err != nil {
			return err
		}
		rendered, err := tpl.Render(st.Name, st.ProjectName(), spec.Parameters, secrets)
		if err != nil {
			return templateError(err, "spec")
		}
		ids, err := s.createMembers(ctx, q, p, st, tpl, rendered)
		if err != nil {
			return err
		}
		created = append(created, ids...)
		st.Outputs = map[string]any{api.OutputTemplate: tplName(spec, tpl), api.OutputTemplateVersion: tpl.Version, api.OutputStackOutputs: rendered.Outputs,
			api.OutputMembers: []api.StackMember{}, api.OutputReady: 0, api.OutputTotal: len(rendered.Members) + len(rendered.SecretParams)}
		return q.SaveResource(ctx, st)
	})
	if err != nil {
		return api.Resource{}, err
	}
	for _, id := range created {
		s.Queue.Enqueue(id)
	}
	s.Log.Info("stack created", "resource_id", st.ID, "project", st.Project, "members", len(created)-1, "by", p.Key())
	return st.ToAPI(), nil
}

func tplName(spec api.StackSpec, t *catalogue.Template) string {
	if spec.Template != "" {
		return spec.Template
	}
	return t.Name
}

// createMembers creates a stack's secret parameters, then its members in template order. A member that
// names another's managed password gets it from the secret made for it a moment before.
func (s *Resources) createMembers(ctx context.Context, q *store.Queries, p Principal, st *domain.Resource, tpl *catalogue.Template, rendered catalogue.Rendered) ([]string, error) {
	var created []string
	note := " Part of the stack " + st.Name + "."
	opts := createOpts{stack: st.ID, note: note}
	names := make([]string, 0, len(rendered.SecretParams))
	for n := range rendered.SecretParams {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		b, _ := json.Marshal(api.SecretSpec{Value: rendered.SecretParams[n]})
		_, ids, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Kind: api.KindSecret, Name: n, Project: st.ProjectName(), Spec: b}, opts)
		if err != nil {
			return nil, memberError(err, n)
		}
		created = append(created, ids...)
	}
	for _, m := range rendered.Members {
		spec := m.Spec
		if m.Kind == api.KindSecret {
			v, err := s.memberSecretValue(ctx, q, st, m)
			if err != nil {
				return nil, err
			}
			spec, _ = json.Marshal(api.SecretSpec{Value: v})
		}
		_, ids, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Kind: m.Kind, Name: m.Name, Project: st.ProjectName(), Spec: spec}, opts)
		if err != nil {
			return nil, memberError(err, m.Name)
		}
		created = append(created, ids...)
	}
	return created, nil
}

// memberSecretValue is a secret member's value: random, or its template's value with managed passwords in.
func (s *Resources) memberSecretValue(ctx context.Context, q *store.Queries, st *domain.Resource, m catalogue.RenderedMember) (string, error) {
	if m.Generate > 0 {
		return catalogue.Generate(m.Generate), nil
	}
	v, err := catalogue.FillPasswords(m.Value, func(local string) (string, bool) {
		full := st.Name
		if local != "" {
			full += "-" + local
		}
		for _, k := range []api.Kind{api.KindDatabase, api.KindSearch, api.KindEmail, api.KindQueue, api.KindTopic} {
			sec, err := q.GetResourceByName(ctx, st.ProjectName(), api.KindSecret, domain.PasswordSecretFor(k, full))
			if err != nil || sec.OwnerID == nil {
				continue
			}
			if b, err := s.Box.Open(sec.SecretCiphertext, []byte(sec.ID)); err == nil {
				return string(b), true
			}
		}
		return "", false
	})
	if err != nil {
		return "", api.Validation([]api.FieldError{{Field: "members." + m.Name + ".value", Code: api.FieldNotFound, Message: err.Error()}})
	}
	return v, nil
}

// updateStack re-renders a stack and makes its members match (CAT-06): new ones are created, changed ones
// updated, ones the template leaves out now deleted. Generated secrets keep their values.
func (s *Resources) updateStack(ctx context.Context, p Principal, st *domain.Resource, req api.UpdateResourceRequest, ifMatch *int64) (api.Resource, bool, error) {
	patch, _, newSecrets, err := splitStackSpec(req.Spec)
	if err != nil {
		return api.Resource{}, false, err
	}
	var queued []string
	changed := false
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, st.ID)
		if err != nil {
			return err
		}
		if err := p.Require(r.ProjectName(), api.RoleEditor); err != nil {
			return err
		}
		if r.Phase == api.PhaseDeleting {
			return api.NewError(api.CodeResourceBusy, "This stack is being deleted.")
		}
		if err := checkIfMatch(r, ifMatch); err != nil {
			return err
		}
		var before api.StackSpec
		_ = json.Unmarshal(r.Spec, &before)
		after := before
		if patch.Template != "" || patch.TemplateBody != "" {
			after.Template, after.TemplateBody = patch.Template, patch.TemplateBody
		}
		if patch.Version != "" {
			after.Version = patch.Version
		}
		if patch.Parameters != nil {
			if after.Parameters == nil {
				after.Parameters = map[string]any{}
			}
			for k, v := range patch.Parameters {
				if v == nil {
					delete(after.Parameters, k)
				} else {
					after.Parameters[k] = v
				}
			}
		}
		if v := api.ImmutableViolations(api.KindStack, before, after); len(v) > 0 {
			return &api.Error{Code: api.CodeImmutableField, Message: v[0].Message, Details: v}
		}
		tpl, err := s.stackTemplate(ctx, q, after)
		if err != nil {
			return err
		}
		members, err := q.StackMembers(ctx, r.ID)
		if err != nil {
			return err
		}
		// Secret parameters already made count as given (keptSecret marks them, so they aren't rewritten);
		// new values replace them.
		secrets := map[string]string{}
		for _, prm := range tpl.Parameters {
			if prm.Type != api.ParamSecret {
				continue
			}
			name := r.Name + "-" + strings.ToLower(prm.Name)
			if v, ok := newSecrets[prm.Name]; ok {
				secrets[prm.Name] = v
			} else if slices.ContainsFunc(members, func(m *domain.Resource) bool { return m.Kind == api.KindSecret && m.Name == name }) {
				secrets[prm.Name] = keptSecret
			}
		}
		rendered, err := tpl.Render(r.Name, r.ProjectName(), after.Parameters, secrets)
		if err != nil {
			return templateError(err, "spec")
		}
		byName := map[string]*domain.Resource{}
		for _, m := range members {
			byName[string(m.Kind)+"/"+m.Name] = m
		}
		keep := map[string]bool{}
		opts := createOpts{stack: r.ID, note: " Part of the stack " + r.Name + "."}
		for name, v := range rendered.SecretParams {
			keep[string(api.KindSecret)+"/"+name] = true
			if v == keptSecret {
				continue
			}
			b, _ := json.Marshal(api.SecretSpec{Value: v})
			if existing := byName[string(api.KindSecret)+"/"+name]; existing != nil {
				m, ch, err := s.updateIn(ctx, q, p, existing.ID, api.UpdateResourceRequest{Spec: b}, nil, true)
				if err != nil {
					return memberError(err, name)
				}
				if ch {
					queued = append(queued, m.ID)
				}
				continue
			}
			_, ids, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Kind: api.KindSecret, Name: name, Project: r.ProjectName(), Spec: b}, opts)
			if err != nil {
				return memberError(err, name)
			}
			queued = append(queued, ids...)
		}
		for _, m := range rendered.Members {
			key := string(m.Kind) + "/" + m.Name
			keep[key] = true
			existing := byName[key]
			if m.Kind == api.KindSecret {
				if existing != nil && m.Generate > 0 {
					continue // a generated value stays what it was
				}
				v, err := s.memberSecretValue(ctx, q, r, m)
				if err != nil {
					return err
				}
				m.Spec, _ = json.Marshal(api.SecretSpec{Value: v})
			}
			if existing == nil {
				_, ids, err := s.createIn(ctx, q, p, api.CreateResourceRequest{Kind: m.Kind, Name: m.Name, Project: r.ProjectName(), Spec: m.Spec}, opts)
				if err != nil {
					return memberError(err, m.Name)
				}
				queued = append(queued, ids...)
				continue
			}
			mpatch, err := replacingPatch(existing.Spec, m.Spec, m.Kind)
			if err != nil {
				return err
			}
			upd, ch, err := s.updateIn(ctx, q, p, existing.ID, api.UpdateResourceRequest{Spec: mpatch}, nil, true)
			if err != nil {
				return memberError(err, m.Name)
			}
			if ch {
				queued = append(queued, upd.ID)
				pending, err := pendingOwned(ctx, q, upd.ID) // keys the change made (BKA-03)
				if err != nil {
					return err
				}
				queued = append(queued, pending...)
			}
		}
		now := s.Now()
		for _, m := range members {
			if keep[string(m.Kind)+"/"+m.Name] || m.Phase == api.PhaseDeleting {
				continue
			}
			if err := s.markDeleting(ctx, q, m, "Left out of the stack "+r.Name+" by "+p.Actor()+"."); err != nil {
				return err
			}
			queued = append(queued, m.ID)
		}
		after.SecretParameters = nil
		b, _ := json.Marshal(after)
		if string(b) != string(r.Spec) || len(queued) > 0 {
			r.Spec, changed = b, true
			r.Generation++
			if err := r.SetPhase(api.PhaseUpdating, "", now); err != nil {
				return err
			}
			r.Attempts, r.NextAttemptAt, r.UpdatedAt = 0, nil, now
			r.Steps = domain.NewSteps(domain.StepNames[r.Kind]...)
			r.Outputs = withOutputs(r.Outputs, map[string]any{api.OutputTemplateVersion: tpl.Version, api.OutputStackOutputs: rendered.Outputs})
			if err := q.SaveResource(ctx, r); err != nil {
				return err
			}
			if err := q.AddEvent(ctx, r.ID, "updated", fmt.Sprintf("Stack changed to generation %d by %s.", r.Generation, p.Actor()), now); err != nil {
				return err
			}
			queued = append(queued, r.ID)
		}
		st = r
		return nil
	})
	if err != nil {
		return api.Resource{}, false, err
	}
	for _, id := range queued {
		s.Queue.Enqueue(id)
	}
	return st.ToAPI(), changed, nil
}

// replacingPatch is a merge patch that makes the stored spec equal the rendered one: what the template no
// longer sets is cleared.
func replacingPatch(stored, rendered json.RawMessage, kind api.Kind) (json.RawMessage, error) {
	var old, want map[string]any
	_ = json.Unmarshal(stored, &old)
	if err := json.Unmarshal(rendered, &want); err != nil {
		return nil, err
	}
	if want == nil {
		want = map[string]any{}
	}
	norm, _, err := api.NormalizeSpec(kind, rendered)
	if err == nil {
		// Defaults the server fills in aren't changes.
		b, _ := json.Marshal(norm)
		var filled map[string]any
		if json.Unmarshal(b, &filled) == nil {
			for k, v := range filled {
				if _, ok := want[k]; !ok {
					want[k] = v
				}
			}
		}
	}
	for k := range old {
		if _, ok := want[k]; !ok {
			want[k] = nil
		}
	}
	return json.Marshal(want)
}

// markDeleting starts deleting a resource inside a transaction (a stack's member, CAT-07).
func (s *Resources) markDeleting(ctx context.Context, q *store.Queries, r *domain.Resource, msg string) error {
	t := s.Now()
	if err := r.SetPhase(api.PhaseDeleting, "", t); err != nil {
		return err
	}
	r.Attempts, r.NextAttemptAt, r.RequestedAction, r.UpdatedAt = 0, nil, "", t
	if err := q.SaveResource(ctx, r); err != nil {
		return err
	}
	return q.AddEvent(ctx, r.ID, "deleting", msg, t)
}

// deleteStackIn marks a stack and every member deleting; a member something outside the stack uses
// refuses the whole delete (CAT-07, L3-002 §11.10).
func (s *Resources) deleteStackIn(ctx context.Context, q *store.Queries, p Principal, st *domain.Resource) ([]string, error) {
	members, err := q.StackMembers(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	inside := map[string]bool{st.ID: true}
	for _, m := range members {
		inside[m.ID] = true
		owned, err := q.OwnedBy(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		for _, o := range owned {
			inside[o.ID] = true
		}
	}
	e := &api.Error{Code: api.CodeResourceInUse, Message: fmt.Sprintf("The stack %s has members that other resources use. Remove them from these first.", st.Name)}
	for _, m := range members {
		deps, err := q.Dependants(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			if !inside[d.ID] {
				e.Details = append(e.Details, api.FieldError{Field: string(d.Kind) + "/" + d.Name, Code: api.FieldNotAllowed, Message: fmt.Sprintf("Uses the %s %s.", m.Kind, m.Name)})
			}
		}
	}
	if len(e.Details) > 0 {
		return nil, e
	}
	ids := []string{st.ID}
	msg := "Deletion requested by " + p.Actor() + " with the stack " + st.Name + "."
	for _, m := range members {
		if m.Phase == api.PhaseDeleting {
			continue
		}
		if err := s.markDeleting(ctx, q, m, msg); err != nil {
			return nil, err
		}
		ids = append(ids, m.ID)
	}
	return ids, s.markDeleting(ctx, q, st, "Deletion requested by "+p.Actor()+".")
}
