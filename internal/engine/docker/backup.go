package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Logical backups and restores (ADR-018, DAR-011).

const (
	bgsaveTimeout  = 10 * time.Minute
	rewriteTimeout = 10 * time.Minute
	stderrLimit    = 4 << 10
)

// noReport discards step progress for internal Ensure calls.
type noReport struct{}

func (noReport) Step(string, api.StepState) {}

// execStream runs an argument array in a container, streaming src to its stdin (when not nil) and its
// stdout to dst. It returns the exit code and the end of stderr.
func (d *Driver) execStream(ctx context.Context, name string, cmd, env []string, src io.Reader, dst io.Writer) (int, string, error) {
	created, err := d.cli.ContainerExecCreate(ctx, name, container.ExecOptions{Cmd: cmd, Env: env, AttachStdin: src != nil, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return 0, "", d.classify(fmt.Errorf("exec in %s: %w", name, err))
	}
	att, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return 0, "", d.classify(fmt.Errorf("attach exec in %s: %w", name, err))
	}
	defer att.Close()
	inErr := make(chan error, 1)
	if src != nil {
		go func() {
			_, err := io.Copy(att.Conn, src)
			_ = att.CloseWrite()
			inErr <- err
		}()
	} else {
		inErr <- nil
	}
	var stderr tailBuffer
	outErr := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(dst, &stderr, att.Reader)
		outErr <- err
	}()
	select {
	case <-ctx.Done():
		return 0, "", ctx.Err()
	case err := <-outErr:
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, "", fmt.Errorf("stream exec output: %w", err)
		}
	}
	if err := <-inErr; err != nil {
		return 0, "", fmt.Errorf("stream exec input: %w", err)
	}
	insp, err := d.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return 0, "", d.classify(fmt.Errorf("inspect exec: %w", err))
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > stderrLimit {
		msg = "…" + msg[len(msg)-stderrLimit:]
	}
	return insp.ExitCode, msg, nil
}

// Dump implements engine.Driver.
func (d *Driver) Dump(ctx context.Context, in engine.EnsureInput, w io.Writer) (engine.DumpInfo, error) {
	r := in.Resource
	name := engine.ContainerName(r)
	switch s := in.Spec.(type) {
	case api.DatabaseSpec:
		if s.Engine != api.EnginePostgres {
			return d.dumpEngine(ctx, in, s, w)
		}
		ident := domain.DatabaseIdentifier(r.Name)
		// The password goes through the exec environment, never argv (L3-006 §5).
		code, stderr, err := d.execStream(ctx, name, []string{"pg_dump", "--format=custom", "--no-owner", "--no-acl", "-U", ident, "-d", ident},
			[]string{"PGPASSWORD=" + in.Password}, nil, w)
		if err != nil {
			return engine.DumpInfo{}, err
		}
		if code != 0 {
			return engine.DumpInfo{}, fmt.Errorf("pg_dump exited with code %d: %s", code, stderr)
		}
		return engine.DumpInfo{Ext: "pgdump", Engine: "postgres " + s.Version}, nil
	case api.CacheSpec:
		if err := d.bgsave(ctx, name); err != nil {
			return engine.DumpInfo{}, err
		}
		code, stderr, err := d.execStream(ctx, name, []string{"cat", "/data/dump.rdb"}, nil, nil, w)
		if err != nil {
			return engine.DumpInfo{}, err
		}
		if code != 0 {
			return engine.DumpInfo{}, fmt.Errorf("reading the snapshot failed (exit %d): %s", code, stderr)
		}
		return engine.DumpInfo{Ext: "rdb", Engine: "redis " + s.Version}, nil
	}
	return engine.DumpInfo{}, engine.Permanent(fmt.Errorf("a %s can't be backed up", r.Kind))
}

// redisInfo reads one field of INFO persistence.
func (d *Driver) redisInfo(ctx context.Context, name, field string) (string, error) {
	res, err := d.exec(ctx, name, []string{"redis-cli", "INFO", "persistence"}, nil)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Output, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == field {
			return v, nil
		}
	}
	return "", fmt.Errorf("redis INFO has no %s", field)
}

// bgsave takes a fresh snapshot and waits for it: done when no save runs and the last save is no older
// than the request (LASTSAVE has one-second resolution, so compare with the request's second).
func (d *Driver) bgsave(ctx context.Context, name string) error {
	asked := time.Now().Unix()
	res, err := d.exec(ctx, name, []string{"redis-cli", "BGSAVE"}, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("BGSAVE failed: %s", res.Output)
	}
	deadline := time.Now().Add(bgsaveTimeout)
	for {
		busy, err := d.redisInfo(ctx, name, "rdb_bgsave_in_progress")
		if err != nil {
			return err
		}
		last, err := d.redisInfo(ctx, name, "rdb_last_save_time")
		if err != nil {
			return err
		}
		lastSave, _ := strconv.ParseInt(last, 10, 64)
		if busy == "0" && lastSave >= asked {
			status, _ := d.redisInfo(ctx, name, "rdb_last_bgsave_status")
			if status != "ok" {
				return fmt.Errorf("the snapshot failed (rdb_last_bgsave_status:%s)", status)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the snapshot didn't finish within %s", bgsaveTimeout)
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

// Restore implements engine.Driver.
func (d *Driver) Restore(ctx context.Context, in engine.EnsureInput, src io.Reader) error {
	r := in.Resource
	switch s := in.Spec.(type) {
	case api.DatabaseSpec:
		if s.Engine != api.EnginePostgres {
			return d.restoreEngine(ctx, in, s, src)
		}
		if _, err := d.Ensure(ctx, in, noReport{}); err != nil {
			return err
		}
		ident := domain.DatabaseIdentifier(r.Name)
		var out bytes.Buffer
		code, stderr, err := d.execStream(ctx, engine.ContainerName(r), []string{"pg_restore", "--clean", "--if-exists", "--no-owner", "--role=" + ident,
			"-U", ident, "-d", ident}, []string{"PGPASSWORD=" + in.Password}, src, &out)
		if err != nil {
			return err
		}
		if code != 0 {
			return engine.Permanent(fmt.Errorf("pg_restore exited with code %d: %s", code, stderr))
		}
		return nil
	case api.CacheSpec:
		return d.restoreCache(ctx, in, src)
	}
	return engine.Permanent(fmt.Errorf("a %s can't be restored", r.Kind))
}

// restoreCache replaces a cache's data with an RDB snapshot. Redis 7 with appendonly ignores dump.rdb
// once an AOF exists and also when none does (it starts empty), so the snapshot is loaded by a temporary
// Redis with appendonly off, which then rewrites the AOF from it (ADR-018, verified on redis:7-alpine).
func (d *Driver) restoreCache(ctx context.Context, in engine.EnsureInput, src io.Reader) error {
	r := in.Resource
	vol := engine.VolumeName(r)
	if err := d.remove(ctx, engine.ContainerName(r)); err != nil {
		return err
	}
	// 1. Write the snapshot into the volume and drop the old AOF, through a helper that mounts it.
	helper := "es-restore-" + r.ID
	_ = d.remove(ctx, helper)
	if err := d.ensureImage(ctx, d.opts.ProbeImage, false); err != nil {
		return err
	}
	if err := d.runHelper(ctx, helper, d.opts.ProbeImage, vol, []string{"sleep", "3600"}); err != nil {
		return err
	}
	defer func() { _ = d.remove(context.WithoutCancel(ctx), helper) }()
	code, stderr, err := d.execStream(ctx, helper, []string{"sh", "-c", "rm -rf /data/appendonlydir /data/appendonly.aof && cat > /data/dump.rdb"}, nil, src, io.Discard)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("writing the snapshot failed (exit %d): %s", code, stderr)
	}
	_ = d.remove(ctx, helper)
	// 2. Load it with appendonly off, then turn appendonly on: Redis rewrites the AOF from the data.
	loader := "es-restore-load-" + r.ID
	_ = d.remove(ctx, loader)
	if err := d.ensureImage(ctx, redisImage, false); err != nil {
		return err
	}
	if err := d.runHelper(ctx, loader, redisImage, vol, []string{"redis-server", "--appendonly", "no"}); err != nil {
		return err
	}
	defer func() { _ = d.remove(context.WithoutCancel(ctx), loader) }()
	deadline := time.Now().Add(rewriteTimeout)
	for {
		if res, err := d.exec(ctx, loader, []string{"redis-cli", "PING"}, nil); err == nil && strings.TrimSpace(res.Output) == "PONG" {
			break
		}
		if err := d.stillRunning(ctx, loader); err != nil {
			return fmt.Errorf("the snapshot couldn't be loaded: %w. Last output: %s", err, d.tail(ctx, loader, 5))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("loading the snapshot took longer than %s", rewriteTimeout)
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	if res, err := d.exec(ctx, loader, []string{"redis-cli", "CONFIG", "SET", "appendonly", "yes"}, nil); err != nil || strings.TrimSpace(res.Output) != "OK" {
		return fmt.Errorf("turning the AOF back on failed: %v %s", err, res.Output)
	}
	for {
		enabled, _ := d.redisInfo(ctx, loader, "aof_enabled")
		rewriting, _ := d.redisInfo(ctx, loader, "aof_rewrite_in_progress")
		status, _ := d.redisInfo(ctx, loader, "aof_last_bgrewrite_status")
		if enabled == "1" && rewriting == "0" && status == "ok" {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rewriting the AOF took longer than %s", rewriteTimeout)
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	_, _ = d.exec(ctx, loader, []string{"redis-cli", "SHUTDOWN", "NOSAVE"}, nil) // fsyncs the AOF; the exec ends with the server
	_ = d.remove(ctx, loader)
	// 3. Start the cache as usual: it loads the new AOF.
	_, err = d.Ensure(ctx, in, noReport{})
	return err
}

// runHelper starts a short-lived container that mounts a resource's volume at /data.
func (d *Driver) runHelper(ctx context.Context, name, image, vol string, cmd []string) error {
	cfg := &container.Config{Image: image, Cmd: cmd, Labels: map[string]string{LabelManaged: "true", LabelKind: "system"}}
	host := &container.HostConfig{Binds: []string{vol + ":/data"}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, nil, nil, name); err != nil {
		return d.classify(fmt.Errorf("create %s: %w", name, err))
	}
	return d.start(ctx, name)
}
