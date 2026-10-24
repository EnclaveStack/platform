package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Per-app bucket access (L2-011, ADR-049): the service checks the buckets, references them, and makes or
// removes the resource's own keys; the reconciler gives its object store user the policy.

// bucketRefs checks each grant names a live bucket of the project and references it (BKA-07).
func (s *Resources) bucketRefs(ctx context.Context, q *store.Queries, project string, grants []api.BucketGrant) ([]store.Ref, error) {
	var refs []store.Ref
	var missing []api.FieldError
	for i, g := range grants {
		f := fmt.Sprintf("spec.bucketAccess[%d].bucket", i)
		b, err := q.GetResourceByName(ctx, project, api.KindBucket, g.Bucket)
		switch {
		case errors.Is(err, store.ErrNotFound):
			missing = append(missing, api.FieldError{Field: f, Code: api.FieldNotFound, Message: "There's no bucket named " + g.Bucket + "."})
		case err != nil:
			return nil, err
		case b.Phase == api.PhaseDeleting:
			missing = append(missing, api.FieldError{Field: f, Code: api.FieldNotFound, Message: "The bucket " + g.Bucket + " is being deleted."})
		default:
			refs = append(refs, store.Ref{ToID: b.ID, Field: f})
		}
	}
	if len(missing) > 0 {
		return nil, api.Validation(missing)
	}
	return refs, nil
}

// syncBucketCredentials makes r's credentials secret when it gets bucket access and has none, and deletes
// it when its access is gone (BKA-03, BKA-06). Keys made once stay through later changes. It returns the
// secret it made, for the caller to queue.
func (s *Resources) syncBucketCredentials(ctx context.Context, q *store.Queries, r *domain.Resource, spec any, now time.Time) (string, error) {
	name := api.BucketCredentialsSecretFor(r.Name)
	existing, err := q.GetResourceByName(ctx, r.ProjectName(), api.KindSecret, name)
	mine := err == nil && existing.OwnerID != nil && *existing.OwnerID == r.ID
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if len(domain.BucketGrantsOf(spec)) == 0 {
		if !mine {
			return "", nil
		}
		if err := q.DeleteResource(ctx, existing.ID); errors.Is(err, store.ErrConflict) {
			return "", api.Validation([]api.FieldError{{Field: "spec.bucketAccess", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("Another resource uses the secret %s; stop using it before removing %s's bucket access.", name, r.Name)}})
		} else if err != nil {
			return "", err
		}
		return "", nil
	}
	if mine {
		return "", nil
	}
	creds, _ := json.Marshal(map[string]string{"accessKey": domain.StoreUserFor(r.ID), "secretKey": randomPassword()})
	return s.createManagedSecret(ctx, q, r.ProjectName(), name, r.ID, string(creds), now)
}

// bucketAccessText is the event for a change of bucket access (BKA-08).
func bucketAccessText(grants []api.BucketGrant) string {
	if len(grants) == 0 {
		return "Bucket access removed: its keys no longer work."
	}
	text := "Bucket access: "
	for i, g := range grants {
		if i > 0 {
			text += ", "
		}
		level := "read"
		if g.Access == api.BucketReadWrite {
			level = "read and write"
		}
		text += g.Bucket + " " + level
	}
	return text + "."
}

// pendingOwned lists the resources r owns that wait for their first reconcile, such as keys an update made.
func pendingOwned(ctx context.Context, q *store.Queries, ownerID string) ([]string, error) {
	owned, err := q.OwnedBy(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, o := range owned {
		if o.Phase == api.PhasePending {
			ids = append(ids, o.ID)
		}
	}
	return ids, nil
}

// jobKeepsAppAccessVariables refuses a job that runs as an app with bucket access and sets one of the
// variables that access gives (BKA-04, BKA-05): the app's keys would replace it.
func jobKeepsAppAccessVariables(app *domain.Resource, job api.JobSpec) error {
	spec, _, err := api.NormalizeSpec(api.KindApp, app.Spec)
	if err != nil || len(domain.BucketGrantsOf(spec)) == 0 {
		return nil
	}
	var errs []api.FieldError
	for _, k := range api.BucketAccessEnv {
		if _, ok := job.Env[k]; ok {
			errs = append(errs, api.FieldError{Field: "spec.env." + k, Code: api.FieldNotAllowed, Message: "The app " + app.Name + " has bucket access, so EnclaveStack sets this variable."})
		}
		if _, ok := job.SecretEnv[k]; ok {
			errs = append(errs, api.FieldError{Field: "spec.secretEnv." + k, Code: api.FieldNotAllowed, Message: "The app " + app.Name + " has bucket access, so EnclaveStack sets this variable."})
		}
	}
	if len(errs) > 0 {
		return api.Validation(errs)
	}
	return nil
}
