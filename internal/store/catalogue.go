package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CatalogueTemplate is one version of an own template (ADR-048).
type CatalogueTemplate struct {
	Name      string
	Version   string
	Title     string
	Body      string
	CreatedAt time.Time
	CreatedBy string
}

// PutTemplate adds a version of an own template; the same name and version returns ErrConflict.
func (q *Queries) PutTemplate(ctx context.Context, t CatalogueTemplate) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO catalogue_templates (name, version, title, body, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		t.Name, t.Version, t.Title, t.Body, ts(t.CreatedAt), t.CreatedBy)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// Templates returns every version of every own template, by name and then by when it was added.
func (q *Queries) Templates(ctx context.Context) ([]CatalogueTemplate, error) {
	return q.templates(ctx, `SELECT name, version, title, body, created_at, created_by FROM catalogue_templates ORDER BY name, created_at`)
}

// TemplateVersions returns an own template's versions, oldest first; none means it doesn't exist.
func (q *Queries) TemplateVersions(ctx context.Context, name string) ([]CatalogueTemplate, error) {
	return q.templates(ctx, `SELECT name, version, title, body, created_at, created_by FROM catalogue_templates WHERE name = ? ORDER BY created_at`, name)
}

// GetTemplate returns one version, or ErrNotFound.
func (q *Queries) GetTemplate(ctx context.Context, name, version string) (CatalogueTemplate, error) {
	var t CatalogueTemplate
	var created string
	err := q.q.QueryRowContext(ctx, `SELECT name, version, title, body, created_at, created_by FROM catalogue_templates WHERE name = ? AND version = ?`, name, version).
		Scan(&t.Name, &t.Version, &t.Title, &t.Body, &created, &t.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.CreatedAt = parseTS(created)
	return t, err
}

// DeleteTemplate removes every version of an own template.
func (q *Queries) DeleteTemplate(ctx context.Context, name string) error {
	res, err := q.q.ExecContext(ctx, `DELETE FROM catalogue_templates WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (q *Queries) templates(ctx context.Context, query string, args ...any) ([]CatalogueTemplate, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogueTemplate
	for rows.Next() {
		var t CatalogueTemplate
		var created string
		if err := rows.Scan(&t.Name, &t.Version, &t.Title, &t.Body, &created, &t.CreatedBy); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTS(created)
		out = append(out, t)
	}
	return out, rows.Err()
}
