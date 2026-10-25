package reconciler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/enclavestack/platform/internal/build"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Source apps: build the image from Git before the deploy (ADR-019, L2-005 BLD-01 – BLD-05).

// Outputs of a source app.
const (
	OutputBuiltCommit = "builtCommit"
	OutputBuiltImage  = "builtImage"
	OutputBuiltInputs = "builtInputs" // bookkeeping: the serving image's inputs hash (BLD-02)
	OutputLastBuild   = "lastBuild"
)

// BuildLogLimit is how much of a build's log is kept (the end).
const BuildLogLimit = 256 << 10

// built is the image a source app deploys, and how it came about.
type built struct {
	image, commit, inputs string
	status                string // succeeded, skipped, or "" when the serving image was reused (drift repair)
}

// sourceImage returns the image a source app's next revision runs. fresh (a new generation or a
// redeploy) resolves the ref again; otherwise the serving image is reused, so drift repair never
// deploys a new commit by itself.
func (r *Reconciler) sourceImage(ctx context.Context, res *domain.Resource, s api.AppSpec, fresh bool, revision int64, rep engine.Reporter) (*built, error) {
	g := *s.Source
	prevImage, _ := res.Outputs[OutputBuiltImage].(string)
	prevInputs, _ := res.Outputs[OutputBuiltInputs].(string)
	prevCommit, _ := res.Outputs[OutputBuiltCommit].(string)
	if !fresh && prevImage != "" {
		return &built{image: prevImage, commit: prevCommit, inputs: prevInputs}, nil
	}
	if r.Git == nil {
		return nil, engine.Permanent(errors.New("builds from Git aren't available on this machine"))
	}
	repo := build.Repo{URL: g.Git, Ref: g.Ref}
	if g.CredentialsSecret != "" {
		raw, err := r.secretValue(ctx, res.Project, g.CredentialsSecret)
		if err != nil {
			return nil, err
		}
		creds, err := build.ParseCredentials(raw)
		if err != nil {
			return nil, engine.Permanent(err)
		}
		repo.Creds = &creds
	}
	engine.Note(rep, fmt.Sprintf("Resolving %s in %s.", g.Ref, g.Git))
	commit, err := r.Git.Resolve(ctx, repo)
	if err != nil {
		return nil, buildErr(err)
	}
	inputs := build.InputsHash(g, commit)
	if inputs == prevInputs && prevImage != "" {
		if ok, err := r.Driver.HasImage(ctx, res, prevImage); err != nil {
			return nil, err
		} else if ok {
			msg := fmt.Sprintf("Commit %s and the build settings are unchanged; build skipped.", short(commit))
			now := r.Now()
			_ = r.Store.InsertBuild(ctx, &store.Build{ID: domain.NewID("bld"), ResourceID: res.ID, Status: store.BuildSkipped, Commit: commit,
				InputsHash: inputs, Image: prevImage, Message: msg, StartedAt: now, FinishedAt: &now})
			r.event(ctx, res, "build_skipped", msg)
			engine.Note(rep, msg)
			return &built{image: prevImage, commit: commit, inputs: inputs, status: store.BuildSkipped}, nil
		}
	}

	b := &store.Build{ID: domain.NewID("bld"), ResourceID: res.ID, Status: store.BuildRunning, Commit: commit, InputsHash: inputs, StartedAt: r.Now()}
	if err := r.Store.InsertBuild(ctx, b); err != nil {
		return nil, err
	}
	logw := &buildLog{rep: rep}
	out, err := r.runBuild(ctx, res, g, repo, revision, b, logw)
	fin := r.Now()
	b.FinishedAt, b.Log = &fin, logw.tail()
	if err != nil {
		b.Status, b.Message = store.BuildFailed, truncate(err.Error())
		_ = r.Store.FinishBuild(context.WithoutCancel(ctx), b)
		res.Outputs = withOutput(res.Outputs, OutputLastBuild, store.BuildFailed)
		msg := fmt.Sprintf("Build of %s failed: %s", short(b.Commit), err.Error())
		if last := logw.lastLines(3); last != "" {
			msg += "\n" + last
		}
		r.event(ctx, res, "build_failed", truncate(msg))
		suffix := ""
		if res.ServingRevision > 0 {
			suffix = ". The serving revision keeps running"
		}
		if engine.IsPermanent(err) {
			return nil, engine.Permanent(fmt.Errorf("build of %s failed: %w%s", short(b.Commit), errors.Unwrap(err), suffix))
		}
		return nil, fmt.Errorf("build of %s failed: %w%s", short(b.Commit), err, suffix)
	}
	b.Status, b.Image, b.Message = store.BuildSucceeded, out.image, fmt.Sprintf("Built %s from %s.", short(out.commit), g.Ref)
	b.Commit, b.InputsHash = out.commit, out.inputs
	if err := r.Store.FinishBuild(ctx, b); err != nil {
		return nil, err
	}
	r.event(ctx, res, "built", b.Message)
	return out, nil
}

func (r *Reconciler) runBuild(ctx context.Context, res *domain.Resource, g api.GitSource, repo build.Repo, revision int64, b *store.Build, logw *buildLog) (*built, error) {
	dir, err := os.MkdirTemp(r.TempDir, "build-*")
	if err != nil {
		return nil, err
	}
	defer build.CleanDir(dir)
	engine.Note(logw.rep, fmt.Sprintf("Cloning %s at %s.", g.Git, short(b.Commit)))
	if !build.IsCommit(repo.Ref) {
		repo.Ref = g.Ref // a branch or tag: shallow; the checkout reports what it got
	}
	commit, err := r.Git.Checkout(ctx, repo, dir)
	if err != nil {
		return nil, buildErr(err)
	}
	inputs := build.InputsHash(g, commit) // the ref may have moved since it was resolved
	b.Commit, b.InputsHash = commit, inputs
	bc, err := build.PrepareContext(dir, g.Context, g.Dockerfile)
	if err != nil {
		return nil, buildErr(err)
	}
	r.event(ctx, res, "build_started", fmt.Sprintf("Building %s from %s.", short(commit), g.Ref))
	tag := build.Tag(res.Name, revision, commit)
	if _, err := r.Driver.Build(ctx, engine.BuildInput{Resource: res, Tag: tag, ContextDir: bc.Dir, Dockerfile: bc.Dockerfile, Excludes: bc.Excludes, BuildArgs: g.BuildArgs}, logw); err != nil {
		return nil, err
	}
	return &built{image: tag, commit: commit, inputs: inputs, status: store.BuildSucceeded}, nil
}

// afterSourceDeploy records what a successful deploy runs and keeps only the serving and previous built
// images (BLD-05). An app that moved from Git to an image drops all of its built images.
func (r *Reconciler) afterSourceDeploy(ctx context.Context, res *domain.Resource, outputs engine.Outputs, src *built, prevImage string, log *slog.Logger) {
	var keep []string
	if src != nil {
		outputs[OutputBuiltImage], outputs[OutputBuiltCommit], outputs[OutputBuiltInputs] = src.image, src.commit, src.inputs
		if src.status != "" {
			outputs[OutputLastBuild] = src.status
		} else if lb, ok := res.Outputs[OutputLastBuild]; ok {
			outputs[OutputLastBuild] = lb
		}
		keep = []string{src.image}
		if prevImage != "" && prevImage != src.image {
			keep = append(keep, prevImage)
		}
	} else if prevImage == "" {
		return // an image app that never built anything
	}
	if err := r.Driver.RemoveBuiltImages(ctx, res, keep); err != nil {
		log.Warn("old built images couldn't be removed", "error", err)
	}
}

// buildErr maps a build package error onto the engine's retry rules.
func buildErr(err error) error {
	if build.IsPermanent(err) {
		return engine.Permanent(err)
	}
	return err
}

func short(commit string) string { return commit[:min(7, len(commit))] }

// buildLog keeps the end of a build's log and reports step lines ("#3 [2/5] RUN …") as progress.
type buildLog struct {
	mu   sync.Mutex
	buf  []byte
	part []byte
	rep  engine.Reporter
}

func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if over := len(l.buf) - BuildLogLimit; over > 0 {
		l.buf = l.buf[over:]
	}
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		line := string(l.part[:i])
		l.part = l.part[i+1:]
		if _, rest, ok := strings.Cut(line, " "); ok && strings.HasPrefix(line, "#") && strings.HasPrefix(rest, "[") {
			engine.Note(l.rep, "Building: "+truncate(rest))
		}
	}
	return len(p), nil
}

func (l *buildLog) tail() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf...)
}

func (l *buildLog) lastLines(n int) string {
	lines := strings.Split(strings.TrimRight(string(l.tail()), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
