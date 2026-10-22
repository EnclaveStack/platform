package reconciler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/pkg/api"
)

// Queues and topics on the broker (ADR-029, L2-008 QUE-*, TOP-*).

// brokerBcryptCost is the cost of the password hashes in the broker's configuration (DAR-021).
const brokerBcryptCost = 11

// BrokerEndpoint is where the control plane signs in to the broker: its address on the machine and
// the system user's password. Services get a messaging.Pool built on it.
func (r *Reconciler) BrokerEndpoint(ctx context.Context) (messaging.Endpoint, error) {
	st := r.Driver.BrokerStatus(ctx)
	if !st.Available {
		return messaging.Endpoint{}, fmt.Errorf("%w: %s", messaging.ErrUnavailable, st.Message)
	}
	pw, err := r.secretValue(ctx, api.DefaultProject, domain.BrokerCredentialsSecret)
	if err != nil {
		return messaging.Endpoint{}, err
	}
	return messaging.Endpoint{URL: "nats://" + st.Address, User: messaging.SystemUserName, Password: pw}, nil
}

// hashPassword returns a bcrypt hash, the same one for the same password while this process runs, so
// rendering the configuration again doesn't change it (and doesn't reload the broker).
func (r *Reconciler) hashPassword(pw string) (string, error) {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(pw)))
	r.hashMu.Lock()
	defer r.hashMu.Unlock()
	if h, ok := r.hashes[key]; ok {
		return h, nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), brokerBcryptCost)
	if err != nil {
		return "", err
	}
	r.hashes[key] = string(h)
	return string(h), nil
}

// syncBroker renders every queue and topic that isn't being deleted into the broker's configuration and
// applies it (ADR-029); with none left, the broker is removed. Rendering and applying happen under one
// lock, so an older configuration can never overwrite a newer one.
func (r *Reconciler) syncBroker(ctx context.Context) (engine.BrokerStatus, error) {
	r.brokerMu.Lock()
	defer r.brokerMu.Unlock()
	var users []messaging.User
	for _, kind := range []api.Kind{api.KindQueue, api.KindTopic} {
		list, err := r.Store.ResourcesOfKind(ctx, kind)
		if err != nil {
			return engine.BrokerStatus{}, err
		}
		for _, res := range list {
			if res.Phase == api.PhaseDeleting {
				continue
			}
			pw, err := r.secretValue(ctx, res.Project, domain.PasswordSecretFor(kind, res.Name))
			if err != nil {
				return engine.BrokerStatus{}, err
			}
			h, err := r.hashPassword(pw)
			if err != nil {
				return engine.BrokerStatus{}, err
			}
			if kind == api.KindQueue {
				users = append(users, messaging.QueueUser(domain.BrokerKey(res.Project, res.Name), h))
			} else {
				users = append(users, messaging.TopicUser(domain.BrokerKey(res.Project, res.Name), h))
			}
		}
	}
	if len(users) == 0 {
		return r.Driver.ConfigureBroker(ctx, engine.BrokerConfig{Empty: true})
	}
	pw, err := r.secretValue(ctx, api.DefaultProject, domain.BrokerCredentialsSecret)
	if err != nil {
		return engine.BrokerStatus{}, err
	}
	h, err := r.hashPassword(pw)
	if err != nil {
		return engine.BrokerStatus{}, err
	}
	users = append(users, messaging.User{Name: messaging.SystemUserName, PasswordHash: h})
	return r.Driver.ConfigureBroker(ctx, engine.BrokerConfig{Config: messaging.RenderConfig(users)})
}

// broker returns a connection; an unreachable broker is a transient error.
func (r *Reconciler) broker(ctx context.Context) (messaging.Broker, error) {
	if r.Broker == nil {
		return nil, engine.Permanent(errors.New("this control plane has no message broker"))
	}
	b, err := r.Broker.Broker(ctx)
	if err != nil {
		return nil, brokerErr(err)
	}
	return b, nil
}

// brokerErr makes broker errors the reconciler's kind: unavailable is transient, the rest as they are.
func brokerErr(err error) error {
	if errors.Is(err, messaging.ErrUnavailable) {
		return fmt.Errorf("the message broker isn't reachable yet: %w", err)
	}
	return err
}

func queueConfig(name string, s api.QueueSpec) messaging.Queue {
	return messaging.Queue{Name: name, VisibilityTimeout: time.Duration(s.VisibilityTimeoutSeconds) * time.Second,
		Retention: time.Duration(s.RetentionHours) * time.Hour, MaxBytes: s.MaxMessageBytes, MaxReceives: s.MaxReceives}
}

func topicConfig(name string, s api.TopicSpec) messaging.Topic {
	t := messaging.Topic{Name: name, Retention: time.Duration(s.RetentionHours) * time.Hour, MaxBytes: api.MessageBytesMax}
	for _, sub := range s.Subscriptions {
		t.Subscriptions = append(t.Subscriptions, messaging.SubscriptionName(sub.Target()))
	}
	return t
}

// brokerOutputs are the connection facts every queue and topic reports. Broker names come from its key
// (ADR-035); the password secret is named in its project.
func brokerOutputs(st engine.BrokerStatus, res *domain.Resource) engine.Outputs {
	key := domain.BrokerKey(res.Project, res.Name)
	out := engine.Outputs{"url": "nats://" + st.Address, "internalUrl": engine.BrokerInternalURL, "passwordSecret": domain.PasswordSecretFor(res.Kind, res.Name)}
	if res.Kind == api.KindQueue {
		out["subject"], out["stream"], out["consumer"], out["username"] = messaging.QueueSubject(key), messaging.QueueStream(key), messaging.QueueConsumer, messaging.QueueUserName(key)
	} else {
		out["subject"], out["stream"], out["username"] = messaging.TopicSubject(key), messaging.TopicStream(key), messaging.TopicUserName(key)
	}
	return out
}

// ensureBrokerRunning applies the broker's configuration and returns its status and a connection.
func (r *Reconciler) ensureBrokerRunning(ctx context.Context, report engine.Reporter) (engine.BrokerStatus, messaging.Broker, error) {
	report.Step("broker", api.StepActive)
	st, err := r.syncBroker(ctx)
	if err != nil {
		return st, nil, err
	}
	if !st.Available {
		return st, nil, fmt.Errorf("the message broker isn't running: %s", st.Message)
	}
	b, err := r.broker(ctx)
	if err != nil {
		return st, nil, err
	}
	report.Step("broker", api.StepDone)
	return st, b, nil
}

// ensureQueue creates or updates a queue's stream and consumer (QUE-01 – QUE-04, QUE-08).
func (r *Reconciler) ensureQueue(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindQueue, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	s := raw.(api.QueueSpec)
	st, b, err := r.ensureBrokerRunning(ctx, report)
	if err != nil {
		return nil, err
	}
	report.Step("queue", api.StepActive)
	if err := b.EnsureQueue(ctx, queueConfig(domain.BrokerKey(res.Project, res.Name), s)); err != nil {
		return nil, brokerErr(err)
	}
	report.Step("queue", api.StepDone)
	out := brokerOutputs(st, res)
	if qs, err := b.QueueStats(ctx, domain.BrokerKey(res.Project, res.Name)); err == nil {
		out["messages"], out["inFlight"] = qs.Messages, qs.InFlight
	}
	return out, nil
}

// ensureTopic creates or updates a topic's stream and its subscriptions' consumers (TOP-01, TOP-04).
func (r *Reconciler) ensureTopic(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindTopic, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	s := raw.(api.TopicSpec)
	st, b, err := r.ensureBrokerRunning(ctx, report)
	if err != nil {
		return nil, err
	}
	report.Step("topic", api.StepActive)
	if err := b.EnsureTopic(ctx, topicConfig(domain.BrokerKey(res.Project, res.Name), s)); err != nil {
		return nil, brokerErr(err)
	}
	report.Step("topic", api.StepDone)
	report.Step("subscriptions", api.StepActive)
	targets := make([]string, len(s.Subscriptions))
	for i, sub := range s.Subscriptions {
		targets[i] = sub.Target()
	}
	if err := r.Store.PruneSubscriptionCounts(ctx, res.ID, targets); err != nil {
		return nil, err
	}
	report.Step("subscriptions", api.StepDone)
	out := brokerOutputs(st, res)
	out["subscriptions"] = len(s.Subscriptions)
	return out, nil
}

// deleteFromBroker removes a queue's or topic's stream, then its user. When it was the last, removing
// the broker removes everything at once.
func (r *Reconciler) deleteFromBroker(ctx context.Context, res *domain.Resource) error {
	others := 0
	for _, kind := range []api.Kind{api.KindQueue, api.KindTopic} {
		list, err := r.Store.ResourcesOfKind(ctx, kind)
		if err != nil {
			return err
		}
		for _, o := range list {
			if o.ID != res.ID && o.Phase != api.PhaseDeleting {
				others++
			}
		}
	}
	if others > 0 {
		b, err := r.broker(ctx)
		if err != nil {
			if _, serr := r.syncBroker(ctx); serr != nil { // a stopped broker is started to delete from it
				return serr
			}
			return err
		}
		if res.Kind == api.KindQueue {
			err = b.DeleteQueue(ctx, domain.BrokerKey(res.Project, res.Name))
		} else {
			err = b.DeleteTopic(ctx, domain.BrokerKey(res.Project, res.Name))
		}
		if err != nil {
			return brokerErr(err)
		}
	}
	_, err := r.syncBroker(ctx)
	return err
}

// purge carries out a requested purge: a queue's messages or a CDN's cache (QUE-07, CDN-05).
func (r *Reconciler) purge(ctx context.Context, res *domain.Resource) error {
	switch res.Kind {
	case api.KindQueue:
		b, err := r.broker(ctx)
		if err != nil {
			return err
		}
		if err := b.PurgeQueue(ctx, domain.BrokerKey(res.Project, res.Name)); err != nil {
			return brokerErr(err)
		}
		r.event(ctx, res, "purged", "Every message was deleted.")
	case api.KindCDN:
		if err := r.Driver.PurgeCache(ctx, res); err != nil {
			return err
		}
		res.Outputs = withOutput(res.Outputs, engine.OutputCachedBytes, 0)
		r.event(ctx, res, "purged", "The cache was emptied.")
	default:
		return engine.Permanent(fmt.Errorf("a %s can't be purged", res.Kind))
	}
	r.Metrics.Purges.WithLabelValues(string(res.Kind)).Inc()
	res.RequestedAction, res.Attempts, res.NextAttemptAt, res.UpdatedAt = "", 0, nil, r.Now()
	_, err := r.Store.SaveStatus(ctx, res, res.Generation)
	return err
}

// refreshBroker is a ready queue's or topic's drift check: a broker that stopped or a stream that is
// gone sends it back to provisioning (the reconcile starts the broker on its volume); otherwise a queue's
// counts are refreshed.
func (r *Reconciler) refreshBroker(ctx context.Context, res *domain.Resource) {
	problem := ""
	if st := r.Driver.BrokerStatus(ctx); !st.Available {
		problem = "the message broker stopped"
	} else if b, err := r.broker(ctx); err != nil {
		problem = "the message broker isn't reachable"
	} else if res.Kind == api.KindQueue {
		qs, err := b.QueueStats(ctx, domain.BrokerKey(res.Project, res.Name))
		switch {
		case errors.Is(err, messaging.ErrNotFound):
			problem = "its stream is missing on the broker"
		case err == nil && (asIntOr(res.Outputs["messages"]) != qs.Messages || asIntOr(res.Outputs["inFlight"]) != qs.InFlight):
			res.Outputs = withOutput(withOutput(res.Outputs, "messages", qs.Messages), "inFlight", qs.InFlight)
			_, _ = r.Store.SaveStatus(ctx, res, res.Generation)
		}
	} else if _, err := b.TopicStats(ctx, domain.BrokerKey(res.Project, res.Name)); errors.Is(err, messaging.ErrNotFound) {
		problem = "its stream is missing on the broker"
	}
	if problem == "" {
		return
	}
	if err := res.SetPhase(api.PhaseProvisioning, "Restoring: "+problem+".", r.Now()); err != nil {
		return
	}
	res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), r.Now()
	if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err != nil || !ok {
		return
	}
	r.event(ctx, res, "drift_detected", strings.ToUpper(problem[:1])+problem[1:]+"; restoring it.")
	r.Enqueue(res.ID)
}

func asIntOr(v any) int64 {
	n, _ := asInt64(v)
	return n
}
