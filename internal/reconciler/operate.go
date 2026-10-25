package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/secretstores"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.7 kinds (ADR-037 – ADR-041). Dashboards, alerts, channels and log exports hold no runtime: the
// monitor works from their specs, so reconciling one checks it and keeps the outputs the monitor writes.
// A secret store is checked against its provider; a secret with a source is read from its store.

// operateKinds are reconciled here.
var operateKinds = []api.Kind{api.KindDashboard, api.KindAlert, api.KindChannel, api.KindLogExport, api.KindSecretStore}

// monitorOutputs are the outputs the monitor keeps; a reconcile carries them over.
var monitorOutputs = []string{"state", "since", "firing", "linesSent", "bytesSent", "lastSentAt", "lagSeconds", "lastError"}

func (r *Reconciler) ensureOperate(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	out := engine.Outputs{}
	for _, k := range monitorOutputs {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	steps := domain.StepNames[res.Kind]
	for _, s := range steps {
		report.Step(s, api.StepActive)
		if res.Kind == api.KindSecretStore && s == "connect" {
			if err := r.testStore(ctx, res); err != nil {
				return nil, err
			}
		}
		report.Step(s, api.StepDone)
	}
	if res.Kind == api.KindSecretStore {
		out["checkedAt"] = r.Now().UTC().Format(time.RFC3339)
	}
	if res.Kind == api.KindLogExport { // a new export has sent nothing yet (LOG-07)
		for k, v := range map[string]any{"linesSent": 0, "bytesSent": 0, "lagSeconds": 0, "lastError": ""} {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

// storeClient builds a store's client with its credentials secret.
func (r *Reconciler) storeClient(ctx context.Context, st *domain.Resource) (secretstores.Store, api.SecretStoreSpec, error) {
	var credErr error
	c, spec, err := secretstores.ForSpec(st.Spec, func(name string) (string, error) {
		v, err := r.secretValue(ctx, st.ProjectName(), name)
		credErr = err
		return v, err
	}, r.HTTP, r.Now)
	if err != nil && credErr == nil { // an unreadable spec or credentials of the wrong shape can't fix themselves
		err = engine.Permanent(err)
	}
	return c, spec, err
}

// testStore checks a store answers with its credentials (EXT-07); a refusal can't fix itself.
func (r *Reconciler) testStore(ctx context.Context, st *domain.Resource) error {
	c, spec, err := r.storeClient(ctx, st)
	if err != nil {
		return err
	}
	err = c.Test(ctx)
	r.Metrics.SecretSyncs.WithLabelValues(spec.Provider, result(err)).Inc()
	if err != nil {
		if strings.Contains(err.Error(), "can't be reached") {
			return err // transient: retried with backoff
		}
		return engine.Permanent(err)
	}
	return nil
}

// secretSource returns a secret's source, or nil for a secret with a value.
func secretSource(res *domain.Resource) *api.SecretSource {
	raw, _, err := api.NormalizeSpec(api.KindSecret, res.Spec)
	if err != nil {
		return nil
	}
	return raw.(api.SecretSpec).Source
}

// syncSecret reads a synced secret's value from its store and stores it encrypted (EXT-03, EXT-04). It
// returns the outputs; the next refresh is set on res.NextRunAt.
func (r *Reconciler) syncSecret(ctx context.Context, res *domain.Resource, src api.SecretSource) (engine.Outputs, error) {
	st, err := r.Store.GetResourceByName(ctx, res.ProjectName(), api.KindSecretStore, src.Store)
	if errors.Is(err, store.ErrNotFound) {
		return nil, engine.Permanent(fmt.Errorf("the secret store %s doesn't exist", src.Store))
	}
	if err != nil {
		return nil, err
	}
	c, spec, err := r.storeClient(ctx, st)
	if err != nil {
		return nil, err
	}
	now := r.Now()
	next := now.Add(time.Duration(spec.RefreshSeconds) * time.Second)
	res.NextRunAt = &next
	value, err := c.Fetch(ctx, src.Key, src.Field)
	r.Metrics.SecretSyncs.WithLabelValues(spec.Provider, result(err)).Inc()
	if err != nil {
		if errors.Is(err, secretstores.ErrNotFound) {
			err = engine.Permanent(fmt.Errorf("%s isn't in the store %s: %w", src.Key, src.Store, err))
		}
		return nil, err
	}
	if len(value) > api.SecretValueMaxBytes {
		return nil, engine.Permanent(fmt.Errorf("the value of %s is over 64 KiB", src.Key))
	}
	version, _ := asInt64(res.Outputs["version"])
	updated, _ := res.Outputs["valueUpdatedAt"].(string)
	old, openErr := r.Box.Open(res.SecretCiphertext, []byte(res.ID))
	if len(res.SecretCiphertext) == 0 || openErr != nil || string(old) != value {
		ct, err := r.Box.Seal([]byte(value), []byte(res.ID))
		if err != nil {
			return nil, err
		}
		if err := r.Store.SetSecretCiphertext(ctx, res.ID, ct); err != nil {
			return nil, err
		}
		res.SecretCiphertext = ct
		version++
		updated = now.UTC().Format(time.RFC3339)
		if version > 1 {
			r.event(ctx, res, "secret_synced", fmt.Sprintf("A new value came from %s (version %d). Apps and jobs get it at their next deploy or run.", src.Store, version))
		}
	}
	return engine.Outputs{"version": version, "valueUpdatedAt": updated, "syncedAt": now.UTC().Format(time.RFC3339), "store": src.Store}, nil
}

// refreshSecret re-reads a ready synced secret when its refresh is due (the scheduler). A failure keeps
// the last value and says why in the message (EXT-05).
func (r *Reconciler) refreshSecret(ctx context.Context, res *domain.Resource) {
	if !r.claim(res.ID) {
		return
	}
	defer r.release(res.ID)
	src := secretSource(res)
	if src == nil {
		_ = r.Store.SetNextRun(ctx, res.ID, nil)
		return
	}
	gen := res.Generation
	out, err := r.syncSecret(ctx, res, *src)
	if err != nil {
		res.Message = truncate("Keeping the last value: " + err.Error())
		next := r.Now().Add(time.Minute)
		if res.NextRunAt == nil || res.NextRunAt.After(next) {
			res.NextRunAt = &next
		}
	} else {
		res.Message, res.Outputs = "", out
	}
	res.UpdatedAt = r.Now()
	if _, serr := r.Store.SaveStatus(ctx, res, gen); serr != nil {
		r.Log.Warn("save a secret's refresh failed", "resource_id", res.ID, "error", serr)
	}
}

// isOperateKind reports whether a kind is reconciled by ensureOperate.
func isOperateKind(k api.Kind) bool { return slices.Contains(operateKinds, k) }

// syncProjectStore gives a project's object store user exactly its buckets (ADR-035); except names a
// bucket being deleted. The default project uses the store's own credentials and has no such user.
func (r *Reconciler) syncProjectStore(ctx context.Context, project, except string) error {
	if project == api.DefaultProject {
		return nil
	}
	list, err := r.Store.ProjectResourcesOfKind(ctx, project, api.KindBucket)
	if err != nil {
		return err
	}
	var buckets []string
	for _, b := range list {
		if b.ID != except && b.Phase != api.PhaseDeleting {
			buckets = append(buckets, b.Name)
		}
	}
	raw, err := r.secretValue(ctx, project, domain.ObjectStoreCredentialsSecret)
	if err != nil {
		return err
	}
	var user struct{ AccessKey, SecretKey string }
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return engine.Permanent(fmt.Errorf("the project's object store credentials are unreadable: %w", err))
	}
	root, err := r.objectStore(ctx)
	if err != nil {
		return err
	}
	return r.Driver.SetProjectStoreAccess(ctx, engine.ObjectStore{AccessKey: user.AccessKey, SecretKey: user.SecretKey}, buckets, root)
}
