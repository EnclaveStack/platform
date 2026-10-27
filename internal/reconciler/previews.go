package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Previews (ADR-056, L2-016 PRV-01 – PRV-09): previews are apps owned by their parent app, so most of
// their life is an app's. The reconciler keeps their marks in the outputs, gives a parent its webhook URL
// and preview count, waits for owned apps and domains before deleting, and expires old previews.

// previewMarks are the outputs the service sets on a preview, kept across reconciles.
var previewMarks = []string{"previewOf", "previewRef", "pullRequest", "expiresAt"}

// expireEvery is how often previews are checked for expiry.
const expireEvery = time.Minute

// withPreviewOutputs keeps a preview's marks, and gives an app with previews its count and webhook URL.
func (r *Reconciler) withPreviewOutputs(ctx context.Context, res *domain.Resource, prev map[string]any) map[string]any {
	out := res.Outputs
	for _, k := range previewMarks {
		if v, ok := prev[k]; ok {
			out = withOutput(out, k, v)
		}
	}
	spec, _, err := api.NormalizeSpec(api.KindApp, res.Spec)
	if err != nil {
		return out
	}
	a := spec.(api.AppSpec)
	if a.Source != nil && a.Source.WebhookSecret != "" {
		url := api.HookPath + res.ID
		if r.ServerURL != nil {
			if base := strings.TrimRight(r.ServerURL(ctx), "/"); base != "" {
				url = base + url
			}
		}
		out = withOutput(out, "webhookUrl", url)
	}
	if a.Previews.On() {
		owned, err := r.Store.OwnedBy(ctx, res.ID)
		if err == nil {
			n := 0
			for _, o := range owned {
				if o.Kind == api.KindApp && o.Phase != api.PhaseDeleting {
					n++
				}
			}
			out = withOutput(out, "previews", n)
		}
	}
	return out
}

// ownedGone holds a deletion while the resource still owns anything but secrets.
func (r *Reconciler) ownedGone(ctx context.Context, res *domain.Resource) error {
	owned, err := r.Store.OwnedBy(ctx, res.ID)
	if err != nil {
		return err
	}
	for _, o := range owned {
		if o.Kind != api.KindSecret {
			if o.Phase != api.PhaseDeleting { // deleted with it: the service marks these, but make sure
				_ = r.Store.WithTx(ctx, func(q *store.Queries) error { return markGone(ctx, q, o, r.Now(), "Deleted with "+res.Name+".") })
			}
			r.Enqueue(o.ID)
			return fmt.Errorf("waiting for the %s %s to be deleted first", o.Kind, o.Name)
		}
	}
	return nil
}

func markGone(ctx context.Context, q *store.Queries, o *domain.Resource, now time.Time, why string) error {
	cur, err := q.GetResource(ctx, o.ID)
	if err != nil || cur.Phase == api.PhaseDeleting {
		return err
	}
	if err := cur.SetPhase(api.PhaseDeleting, "", now); err != nil {
		return err
	}
	cur.Attempts, cur.NextAttemptAt, cur.RequestedAction, cur.UpdatedAt = 0, nil, "", now
	if err := q.SaveResource(ctx, cur); err != nil {
		return err
	}
	return q.AddEvent(ctx, cur.ID, "deleting", why, now)
}

// expirePreviews deletes previews whose expiry has passed, with their domains (PRV-04).
func (r *Reconciler) expirePreviews(ctx context.Context) {
	now := r.Now()
	if now.Sub(r.lastExpiry) < expireEvery {
		return
	}
	r.lastExpiry = now
	apps, err := r.Store.ResourcesOfKind(ctx, api.KindApp)
	if err != nil {
		return
	}
	for _, p := range apps {
		at, _ := p.Outputs["expiresAt"].(string)
		if p.OwnerID == nil || at == "" || p.Phase == api.PhaseDeleting {
			continue
		}
		if t, err := time.Parse(time.RFC3339, at); err != nil || now.Before(t) {
			continue
		}
		var ids []string
		err := r.Store.WithTx(ctx, func(q *store.Queries) error {
			owned, err := q.OwnedBy(ctx, p.ID)
			if err != nil {
				return err
			}
			for _, o := range owned {
				if o.Kind != api.KindSecret {
					if err := markGone(ctx, q, o, now, "Deleted with its preview."); err != nil {
						return err
					}
					ids = append(ids, o.ID)
				}
			}
			if err := markGone(ctx, q, p, now, "The preview expired."); err != nil {
				return err
			}
			if parent, err := q.GetResource(ctx, *p.OwnerID); err == nil {
				return q.AddEvent(ctx, parent.ID, "preview_deleted", fmt.Sprintf("Preview %s expired and was deleted.", p.Name), now)
			}
			return nil
		})
		if err != nil {
			r.Log.Warn("expire a preview failed", "resource_id", p.ID, "error", err)
			continue
		}
		r.Log.Info("preview expired", "resource_id", p.ID, "name", p.Name)
		for _, id := range append(ids, p.ID) {
			r.Enqueue(id)
		}
	}
}
