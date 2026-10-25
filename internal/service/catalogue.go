package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/enclavestack/platform/internal/catalogue"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The catalogue (L2-010 CAT-01 – CAT-03, L3-002 §11.5): built-in entries, then the machine's own templates.

// Catalogue lists every entry.
func (s *Resources) Catalogue(ctx context.Context) (api.CatalogueList, error) {
	out := api.CatalogueList{Items: []api.CatalogueEntry{}}
	for _, t := range catalogue.Builtins() {
		out.Items = append(out.Items, t.Entry(true, []string{t.Version}))
	}
	rows, err := s.Store.Templates(ctx)
	if err != nil {
		return out, err
	}
	byName := map[string][]store.CatalogueTemplate{}
	var names []string
	for _, r := range rows {
		if byName[r.Name] == nil {
			names = append(names, r.Name)
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	slices.Sort(names)
	for _, n := range names {
		if e, err := ownEntry(byName[n], ""); err == nil {
			out.Items = append(out.Items, e)
		}
	}
	return out, nil
}

func ownEntry(versions []store.CatalogueTemplate, version string) (api.CatalogueEntry, error) {
	row := versions[len(versions)-1]
	if version != "" {
		i := slices.IndexFunc(versions, func(v store.CatalogueTemplate) bool { return v.Version == version })
		if i < 0 {
			return api.CatalogueEntry{}, api.NewError(api.CodeNotFound, row.Name+" has no version "+version+".")
		}
		row = versions[i]
	}
	t, err := catalogue.Parse([]byte(row.Body))
	if err != nil {
		return api.CatalogueEntry{}, err
	}
	vs := make([]string, len(versions))
	for i, v := range versions {
		vs[i] = v.Version
	}
	return t.Entry(false, vs), nil
}

// CatalogueEntry returns one entry, at a version ("" for the newest).
func (s *Resources) CatalogueEntry(ctx context.Context, name, version string) (api.CatalogueEntry, error) {
	if t, ok := catalogue.Builtin(name); ok {
		if version != "" && version != t.Version {
			return api.CatalogueEntry{}, api.NewError(api.CodeNotFound, t.Title+" has no version "+version+".")
		}
		return t.Entry(true, []string{t.Version}), nil
	}
	versions, err := s.Store.TemplateVersions(ctx, name)
	if err != nil {
		return api.CatalogueEntry{}, err
	}
	if len(versions) == 0 {
		return api.CatalogueEntry{}, api.NewError(api.CodeNotFound, "There's no catalogue entry named "+name+".")
	}
	return ownEntry(versions, version)
}

// CatalogueSource returns an entry's template as written.
func (s *Resources) CatalogueSource(ctx context.Context, name, version string) (string, error) {
	if t, ok := catalogue.Builtin(name); ok {
		return t.Source, nil
	}
	versions, err := s.Store.TemplateVersions(ctx, name)
	if err != nil || len(versions) == 0 {
		return "", api.NewError(api.CodeNotFound, "There's no catalogue entry named "+name+".")
	}
	row := versions[len(versions)-1]
	if version != "" {
		i := slices.IndexFunc(versions, func(v store.CatalogueTemplate) bool { return v.Version == version })
		if i < 0 {
			return "", api.NewError(api.CodeNotFound, name+" has no version "+version+".")
		}
		row = versions[i]
	}
	return row.Body, nil
}

// AddTemplate adds one of the machine's own templates, or a newer version of one (CAT-03).
func (s *Resources) AddTemplate(ctx context.Context, p Principal, req api.AddTemplate) (api.CatalogueEntry, error) {
	if !p.IsMachineAdmin() {
		return api.CatalogueEntry{}, api.NewError(api.CodeForbidden, "Only machine admins add templates to the catalogue.")
	}
	t, err := catalogue.Parse([]byte(req.Body))
	if err != nil {
		return api.CatalogueEntry{}, templateError(err, "body")
	}
	if _, ok := catalogue.Builtin(t.Name); ok {
		return api.CatalogueEntry{}, api.NewError(api.CodeNameTaken, t.Name+" is a built-in entry. Give your template another name.")
	}
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		versions, err := q.TemplateVersions(ctx, t.Name)
		if err != nil {
			return err
		}
		if len(versions) >= api.TemplateVersionsMax {
			return api.Validation([]api.FieldError{{Field: "body", Code: api.FieldOutOfRange, Message: fmt.Sprintf("%s keeps at most %d versions. Remove it and add it again.", t.Name, api.TemplateVersionsMax)}})
		}
		if len(versions) > 0 && !newer(t.Version, versions[len(versions)-1].Version) && !slices.ContainsFunc(versions, func(v store.CatalogueTemplate) bool { return v.Version == t.Version }) {
			return api.Validation([]api.FieldError{{Field: "body", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("%s has the version %s already; a new one needs a higher version.", t.Name, versions[len(versions)-1].Version)}})
		}
		err = q.PutTemplate(ctx, store.CatalogueTemplate{Name: t.Name, Version: t.Version, Title: t.Title, Body: req.Body, CreatedAt: s.Now(), CreatedBy: p.Actor()})
		if errors.Is(err, store.ErrConflict) {
			return api.NewError(api.CodeNameTaken, fmt.Sprintf("%s %s is in the catalogue already. Raise the version to add a new one.", t.Name, t.Version))
		}
		return err
	})
	if err != nil {
		return api.CatalogueEntry{}, err
	}
	writeAudit(ctx, s.Store, s.Now(), s.Log, p, "template_added", t.Name, t.Version)
	return s.CatalogueEntry(ctx, t.Name, "")
}

// RemoveTemplate removes one of the machine's own templates, refused while stacks use it.
func (s *Resources) RemoveTemplate(ctx context.Context, p Principal, name string) error {
	if !p.IsMachineAdmin() {
		return api.NewError(api.CodeForbidden, "Only machine admins remove templates from the catalogue.")
	}
	if _, ok := catalogue.Builtin(name); ok {
		return api.NewError(api.CodeActionNotSupported, name+" is built in and can't be removed.")
	}
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		stacks, err := q.ResourcesOfKind(ctx, api.KindStack)
		if err != nil {
			return err
		}
		e := &api.Error{Code: api.CodeResourceInUse, Message: name + " is still used. Delete these stacks first."}
		for _, st := range stacks {
			var spec api.StackSpec
			if json.Unmarshal(st.Spec, &spec) == nil && spec.Template == name {
				e.Details = append(e.Details, api.FieldError{Field: qualifiedRef(st), Code: api.FieldNotAllowed, Message: "Made from " + name + "."})
			}
		}
		if len(e.Details) > 0 {
			return e
		}
		if err := q.DeleteTemplate(ctx, name); errors.Is(err, store.ErrNotFound) {
			return api.NewError(api.CodeNotFound, "There's no catalogue entry named "+name+".")
		} else if err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		writeAudit(ctx, s.Store, s.Now(), s.Log, p, "template_removed", name, "")
	}
	return err
}

// newer compares MAJOR.MINOR.PATCH versions.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		var x, y int
		fmt.Sscanf(pa[i], "%d", &x)
		fmt.Sscanf(pb[i], "%d", &y)
		if x != y {
			return x > y
		}
	}
	return false
}
