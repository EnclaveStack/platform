package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Per-app bucket access (L2-011, ADR-049): before an app's revision or a job's run starts, its object
// store user gets exactly its grants, and the container gets its keys in the standard AWS variables.

// grantBucketAccess gives owner's object store user grants and returns the variables its containers get
// (BKA-02, BKA-04). owner is the app or job whose keys these are: a job that runs as an app passes the app.
func (r *Reconciler) grantBucketAccess(ctx context.Context, owner *domain.Resource, grants []api.BucketGrant) (map[string]string, error) {
	raw, err := r.secretValue(ctx, owner.Project, api.BucketCredentialsSecretFor(owner.Name))
	if err != nil {
		return nil, err
	}
	var keys struct{ AccessKey, SecretKey string }
	if err := json.Unmarshal([]byte(raw), &keys); err != nil || keys.AccessKey == "" || keys.SecretKey == "" {
		return nil, engine.Permanent(fmt.Errorf("the keys of %s are unreadable", owner.Name))
	}
	root, err := r.objectStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.Driver.SetStoreUserAccess(ctx, engine.ObjectStore{AccessKey: keys.AccessKey, SecretKey: keys.SecretKey}, grants, root); err != nil {
		return nil, fmt.Errorf("giving %s its bucket access: %w", owner.Name, err)
	}
	endpoint := "http://" + engine.ObjectStoreName + ":" + strconv.Itoa(engine.ObjectStorePort)
	values := []string{keys.AccessKey, keys.SecretKey, api.BucketAccessRegion, endpoint, endpoint}
	env := make(map[string]string, len(values))
	for i, k := range api.BucketAccessEnv {
		env[k] = values[i]
	}
	return env, nil
}

// revokeBucketAccess removes res's object store user if it had one, which makes its keys fail at once
// (BKA-06). A resource that never had access doesn't touch the object store.
func (r *Reconciler) revokeBucketAccess(ctx context.Context, res *domain.Resource) error {
	if _, had := res.Outputs[api.OutputBucketCredentialsSecret]; !had {
		return nil
	}
	root, err := r.objectStore(ctx)
	if err != nil {
		return nil // no object store left, so no user either
	}
	return r.Driver.SetStoreUserAccess(ctx, engine.ObjectStore{AccessKey: domain.StoreUserFor(res.ID)}, nil, root)
}

// withBucketAccess records the credentials secret's name in the outputs while res has access (BKA-08).
func withBucketAccess(res *domain.Resource, spec any) map[string]any {
	if len(domain.BucketGrantsOf(spec)) > 0 {
		return withOutput(res.Outputs, api.OutputBucketCredentialsSecret, api.BucketCredentialsSecretFor(res.Name))
	}
	if _, had := res.Outputs[api.OutputBucketCredentialsSecret]; !had {
		return res.Outputs
	}
	out := withOutput(res.Outputs, api.OutputBucketCredentialsSecret, nil)
	delete(out, api.OutputBucketCredentialsSecret)
	return out
}

// bucketAccessEnv gives res its grants, or takes away access it no longer has, before a revision starts.
func (r *Reconciler) bucketAccessEnv(ctx context.Context, res *domain.Resource, spec any) (map[string]string, error) {
	if grants := domain.BucketGrantsOf(spec); len(grants) > 0 {
		return r.grantBucketAccess(ctx, res, grants)
	}
	return nil, r.revokeBucketAccess(ctx, res)
}
