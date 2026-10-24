package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Remote is another machine's engine, reached through its agent (ADR-042). It implements engine.Driver and
// engine.Hypervisor; every call is one HTTPS request over mutual TLS.
type Remote struct {
	MachineID string
	base      string
	http      *http.Client
}

var (
	_ engine.Driver     = (*Remote)(nil)
	_ engine.Hypervisor = (*Remote)(nil)
)

// NewRemote reaches the agent at host:port with tlsConfig (ClientConfig).
func NewRemote(machineID, host string, port int, tlsConfig *tls.Config) *Remote {
	tr := &http.Transport{TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: false}
	return &Remote{MachineID: machineID, base: "https://" + net.JoinHostPort(host, strconv.Itoa(port)), http: &http.Client{Transport: tr}}
}

// Close drops idle connections.
func (m *Remote) Close() { m.http.CloseIdleConnections() }

func (m *Remote) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, body)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		req.Header.Set(headerDeadline, formatDeadline(dl))
	}
	return req, nil
}

// do sends a request; a connection that can't be made is the machine being unreachable.
func (m *Remote) do(req *http.Request) (*http.Response, error) {
	resp, err := m.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("%w: the agent of %s doesn't answer: %v", engine.ErrMachineOffline, m.MachineID, err)
	}
	return resp, nil
}

func readError(resp *http.Response) error {
	var we wireError
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(b, &we) != nil || we.Class == "" {
		return fmt.Errorf("the agent answered %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return we.err()
}

// call is one unary call: JSON arguments, a JSON result.
func call[R any](ctx context.Context, m *Remote, path string, args any) (R, error) {
	var out R
	b, err := json.Marshal(args)
	if err != nil {
		return out, err
	}
	req, err := m.request(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, readError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("read the agent's answer: %w", err)
	}
	return out, nil
}

func (m *Remote) driverCall(ctx context.Context, method string, args any) error {
	_, err := call[none](ctx, m, pathDriver+method, args)
	return err
}

// streamCall reads a callback stream, forwarding steps, notes, events and log lines, running started
// callbacks, and decoding the final result into out.
func (m *Remote) streamCall(ctx context.Context, req *http.Request, report engine.Reporter, log io.Writer, started func(context.Context) error, out any) error {
	resp, err := m.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fmt.Errorf("read the agent's stream: %w", err)
		}
		switch {
		case e.Error != nil:
			return e.Error.err()
		case e.Result != nil:
			if out == nil {
				return nil
			}
			return json.Unmarshal(e.Result, out)
		case e.Started != "":
			var cb continueBody
			if started != nil {
				if err := started(ctx); err != nil {
					cb.Error = err.Error()
				}
			}
			b, _ := json.Marshal(cb)
			creq, err := m.request(ctx, http.MethodPost, pathContinue+e.Started+"/continue", bytes.NewReader(b))
			if err != nil {
				return err
			}
			cresp, err := m.do(creq)
			if err != nil {
				return err
			}
			cresp.Body.Close()
		case e.Step != "":
			if report != nil {
				report.Step(e.Step, e.State)
			}
		case e.Note != nil:
			engine.Note(report, *e.Note)
		case e.Event != "":
			engine.Event(report, e.Event, e.Message)
		case e.Log != nil:
			if log != nil {
				_, _ = io.WriteString(log, *e.Log)
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: the agent of %s stopped answering: %v", engine.ErrMachineOffline, m.MachineID, err)
	}
	return fmt.Errorf("the agent of %s ended the call without a result", m.MachineID)
}

func (m *Remote) ensureLike(ctx context.Context, method string, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	wi, err := toWireInput(in)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(wi)
	if err != nil {
		return nil, err
	}
	req, err := m.request(ctx, http.MethodPost, pathDriver+method, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var out engine.Outputs
	if err := m.streamCall(ctx, req, report, nil, in.Started, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// readStream reads a call whose body is data (Dump, SnapshotVolume, SnapshotDisk) into w; the result
// comes in a trailer.
func (m *Remote) readStream(ctx context.Context, path string, args any, w io.Writer, result any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := m.request(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := m.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("copy from %s: %w", m.MachineID, err)
	}
	if t := resp.Trailer.Get(trailerError); t != "" {
		var we wireError
		if json.Unmarshal([]byte(t), &we) == nil {
			return we.err()
		}
		return errors.New(t)
	}
	t := resp.Trailer.Get(trailerResult)
	if t == "" {
		return fmt.Errorf("the copy from %s ended early", m.MachineID)
	}
	if result != nil {
		return json.Unmarshal([]byte(t), result)
	}
	return nil
}

// writeStream sends data as the body of a call (Restore, RestoreVolume, Build's context).
func (m *Remote) writeStream(ctx context.Context, path string, args any, src io.Reader) (*http.Response, error) {
	enc, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	req, err := m.request(ctx, http.MethodPost, path, src)
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerArgs, enc)
	req.Header.Set("Content-Type", "application/octet-stream")
	return m.do(req)
}

func (m *Remote) writeCall(ctx context.Context, path string, args any, src io.Reader) error {
	resp, err := m.writeStream(ctx, path, args, src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return nil
}

// AgentInfo reads the agent's facts (health checks, ADR-046).
func (m *Remote) AgentInfo(ctx context.Context) (Info, error) {
	req, err := m.request(ctx, http.MethodGet, pathInfo, nil)
	if err != nil {
		return Info{}, err
	}
	resp, err := m.do(req)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{}, readError(resp)
	}
	var info Info
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// Sweep removes the containers of every resource not in keep (ADR-046).
func (m *Remote) Sweep(ctx context.Context, keep []string) ([]string, error) {
	res, err := call[sweepResult](ctx, m, pathSweep, sweepArgs{Keep: keep})
	return res.Removed, err
}

// Upload sends a file to the agent's cache, by digest, unless it is there already; it returns the digest.
func (m *Remote) Upload(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	head, err := m.request(ctx, http.MethodHead, pathFiles+digest, nil)
	if err != nil {
		return "", err
	}
	if resp, err := m.do(head); err != nil {
		return "", err
	} else {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return digest, nil
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	req, err := m.request(ctx, http.MethodPut, pathFiles+digest, f)
	if err != nil {
		return "", err
	}
	resp, err := m.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return "", readError(resp)
	}
	return digest, nil
}

// engine.Driver.

func (m *Remote) Name() string { return "agent" }

func (m *Remote) Ping(ctx context.Context) error { return m.driverCall(ctx, "Ping", none{}) }

func (m *Remote) Info(ctx context.Context) (engine.Info, error) {
	return call[engine.Info](ctx, m, pathDriver+"Info", none{})
}

func (m *Remote) Ensure(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	return m.ensureLike(ctx, "Ensure", in, report)
}

func (m *Remote) Delete(ctx context.Context, r *domain.Resource, store engine.ObjectStore) error {
	return m.driverCall(ctx, "Delete", resourceStoreArgs{R: bare(r), Store: store})
}

func (m *Remote) Health(ctx context.Context, r *domain.Resource, store engine.ObjectStore) (engine.Health, error) {
	return call[engine.Health](ctx, m, pathDriver+"Health", resourceStoreArgs{R: bare(r), Store: store})
}

func (m *Remote) Restart(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "Restart", resourceArgs{R: bare(r)})
}

func (m *Remote) Logs(ctx context.Context, r *domain.Resource, tail int) ([]api.LogLine, error) {
	return call[[]api.LogLine](ctx, m, pathDriver+"Logs", logsArgs{R: bare(r), Tail: tail})
}

func (m *Remote) ConfigureEdge(ctx context.Context, cfg engine.EdgeConfig) error {
	return m.driverCall(ctx, "ConfigureEdge", edgeArgs{Cfg: cfg})
}

func (m *Remote) Certificate(ctx context.Context, hostname string) (engine.Certificate, error) {
	return call[engine.Certificate](ctx, m, pathDriver+"Certificate", hostnameArgs{Hostname: hostname})
}

func (m *Remote) EdgeStatus(ctx context.Context) engine.EdgeStatus {
	st, err := call[engine.EdgeStatus](ctx, m, pathDriver+"EdgeStatus", none{})
	if err != nil {
		return engine.EdgeStatus{Message: err.Error()}
	}
	return st
}

func (m *Remote) RootCA(ctx context.Context) ([]byte, error) {
	return call[[]byte](ctx, m, pathDriver+"RootCA", none{})
}

func (m *Remote) StartJob(ctx context.Context, run engine.JobRun) error {
	return m.driverCall(ctx, "StartJob", jobArgs{Run: run})
}

func (m *Remote) WaitJob(ctx context.Context, runID string, timeout time.Duration) (engine.JobResult, error) {
	return call[engine.JobResult](ctx, m, pathDriver+"WaitJob", waitArgs{RunID: runID, Timeout: timeout})
}

// Build sends the checked build context as a tar stream and returns the agent's build log as it comes.
func (m *Remote) Build(ctx context.Context, in engine.BuildInput, log io.Writer) (engine.BuildResult, error) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarDir(in.ContextDir, pw)) }()
	enc, err := encodeArgs(buildArgs{Resource: bare(in.Resource), Tag: in.Tag, Dockerfile: in.Dockerfile, Excludes: in.Excludes, BuildArgs: in.BuildArgs})
	if err != nil {
		pr.Close()
		return engine.BuildResult{}, err
	}
	req, err := m.request(ctx, http.MethodPost, pathDriver+"Build", pr)
	if err != nil {
		pr.Close()
		return engine.BuildResult{}, err
	}
	req.Header.Set(headerArgs, enc)
	var out engine.BuildResult
	err = m.streamCall(ctx, req, nil, log, nil, &out)
	pr.Close()
	return out, err
}

func (m *Remote) HasImage(ctx context.Context, r *domain.Resource, ref string) (bool, error) {
	return call[bool](ctx, m, pathDriver+"HasImage", refArgs{R: bare(r), Ref: ref})
}

func (m *Remote) RemoveBuiltImages(ctx context.Context, r *domain.Resource, keep []string) error {
	return m.driverCall(ctx, "RemoveBuiltImages", keepArgs{R: bare(r), Keep: keep})
}

func (m *Remote) EnsureNetwork(ctx context.Context, in engine.NetworkInput) (engine.Outputs, error) {
	return call[engine.Outputs](ctx, m, pathDriver+"EnsureNetwork", networkArgs{R: bare(in.Resource), Spec: in.Spec})
}

func (m *Remote) DeleteNetwork(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "DeleteNetwork", resourceArgs{R: bare(r)})
}

func (m *Remote) ApplyFirewall(ctx context.Context, r *domain.Resource, p *firewall.Policy) (int, error) {
	return call[int](ctx, m, pathDriver+"ApplyFirewall", firewallArgs{R: bare(r), Policy: p})
}

func (m *Remote) Endpoints(ctx context.Context, r *domain.Resource) ([]netip.Addr, error) {
	raw, err := call[[]string](ctx, m, pathDriver+"Endpoints", resourceArgs{R: bare(r)})
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(raw))
	for _, s := range raw {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *Remote) LoadBalancerStatus(ctx context.Context, r *domain.Resource) (lb.Status, error) {
	return call[lb.Status](ctx, m, pathDriver+"LoadBalancerStatus", resourceArgs{R: bare(r)})
}

func (m *Remote) Dump(ctx context.Context, in engine.EnsureInput, w io.Writer) (engine.DumpInfo, error) {
	wi, err := toWireInput(in)
	if err != nil {
		return engine.DumpInfo{}, err
	}
	var info engine.DumpInfo
	return info, m.readStream(ctx, pathDriver+"Dump", wi, w, &info)
}

func (m *Remote) Restore(ctx context.Context, in engine.EnsureInput, src io.Reader) error {
	wi, err := toWireInput(in)
	if err != nil {
		return err
	}
	return m.writeCall(ctx, pathDriver+"Restore", wi, src)
}

func (m *Remote) EnsureVolume(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "EnsureVolume", resourceArgs{R: bare(r)})
}

func (m *Remote) EnsureVMLink(ctx context.Context, in engine.EnsureInput) (engine.Outputs, error) {
	return m.ensureLike(ctx, "EnsureVMLink", in, nil)
}

func (m *Remote) DeleteVMLink(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "DeleteVMLink", resourceArgs{R: bare(r)})
}

func (m *Remote) VolumeUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	return call[int64](ctx, m, pathDriver+"VolumeUsage", resourceArgs{R: bare(r)})
}

func (m *Remote) SnapshotVolume(ctx context.Context, r *domain.Resource, w io.Writer) error {
	return m.readStream(ctx, pathDriver+"SnapshotVolume", resourceArgs{R: bare(r)}, w, nil)
}

func (m *Remote) RestoreVolume(ctx context.Context, r *domain.Resource, src io.Reader) error {
	return m.writeCall(ctx, pathDriver+"RestoreVolume", resourceArgs{R: bare(r)}, src)
}

func (m *Remote) ConfigureBroker(ctx context.Context, cfg engine.BrokerConfig) (engine.BrokerStatus, error) {
	return call[engine.BrokerStatus](ctx, m, pathDriver+"ConfigureBroker", brokerArgs{Cfg: cfg})
}

func (m *Remote) BrokerStatus(ctx context.Context) engine.BrokerStatus {
	st, err := call[engine.BrokerStatus](ctx, m, pathDriver+"BrokerStatus", none{})
	if err != nil {
		return engine.BrokerStatus{Message: err.Error()}
	}
	return st
}

func (m *Remote) PurgeCache(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "PurgeCache", resourceArgs{R: bare(r)})
}

func (m *Remote) CacheUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	return call[int64](ctx, m, pathDriver+"CacheUsage", resourceArgs{R: bare(r)})
}

func (m *Remote) SetBucketPublicRead(ctx context.Context, bucket string, public bool, store engine.ObjectStore) error {
	return m.driverCall(ctx, "SetBucketPublicRead", publicReadArgs{Bucket: bucket, Public: public, Store: store})
}

// Transcode never runs on another machine: transcoders stay on the main machine (PLC-06).
func (m *Remote) Transcode(context.Context, engine.TranscodeRun, io.Reader, func(string, int64, io.Reader) error) (engine.JobResult, error) {
	return engine.JobResult{}, engine.Permanent(errors.New("transcoders run on the main machine"))
}

func (m *Remote) SetProjectStoreAccess(ctx context.Context, user engine.ObjectStore, buckets []string, root engine.ObjectStore) error {
	return m.driverCall(ctx, "SetProjectStoreAccess", storeAccessArgs{User: user, Buckets: buckets, Root: root})
}

func (m *Remote) SetStoreUserAccess(ctx context.Context, user engine.ObjectStore, grants []api.BucketGrant, root engine.ObjectStore) error {
	return m.driverCall(ctx, "SetStoreUserAccess", storeUserArgs{User: user, Grants: grants, Root: root})
}

func (m *Remote) Stats(ctx context.Context, r *domain.Resource) ([]engine.ContainerStats, error) {
	return call[[]engine.ContainerStats](ctx, m, pathDriver+"Stats", resourceArgs{R: bare(r)})
}

func (m *Remote) EdgeRequests(ctx context.Context) (map[string]engine.RequestCounters, error) {
	return call[map[string]engine.RequestCounters](ctx, m, pathDriver+"EdgeRequests", none{})
}

func (m *Remote) LogsSince(ctx context.Context, r *domain.Resource, since map[string]time.Time, fallback time.Time, limit int) ([]engine.ContainerLogLine, error) {
	return call[[]engine.ContainerLogLine](ctx, m, pathDriver+"LogsSince", logsSinceArgs{R: bare(r), Since: since, Fallback: fallback, Limit: limit})
}

// engine.Hypervisor.

func (m *Remote) Check(ctx context.Context) engine.VMSupport {
	vs, err := call[engine.VMSupport](ctx, m, pathHyper+"Check", none{})
	if err != nil {
		return engine.VMSupport{VMSupport: api.VMSupport{Message: err.Error()}}
	}
	return vs
}

// EnsureVM uploads a snapshot used as the VM's image first, so the agent reads its own copy.
func (m *Remote) EnsureVM(ctx context.Context, in engine.VMInput, report engine.Reporter) (engine.VMState, error) {
	if in.Image.SnapshotFile != "" {
		digest, err := m.Upload(ctx, in.Image.SnapshotFile)
		if err != nil {
			return engine.VMState{}, err
		}
		in.Image.SnapshotFile = digest
	}
	b, err := json.Marshal(in)
	if err != nil {
		return engine.VMState{}, err
	}
	req, err := m.request(ctx, http.MethodPost, pathHyper+"EnsureVM", bytes.NewReader(b))
	if err != nil {
		return engine.VMState{}, err
	}
	var st engine.VMState
	return st, m.streamCall(ctx, req, report, nil, nil, &st)
}

func (m *Remote) StopVM(ctx context.Context, vmID string) error {
	_, err := call[none](ctx, m, pathHyper+"StopVM", vmArgs{VMID: vmID})
	return err
}

func (m *Remote) DeleteVM(ctx context.Context, vmID string) error {
	_, err := call[none](ctx, m, pathHyper+"DeleteVM", vmArgs{VMID: vmID})
	return err
}

func (m *Remote) VMStatus(ctx context.Context, vmID string) (engine.VMStatus, error) {
	return call[engine.VMStatus](ctx, m, pathHyper+"VMStatus", vmArgs{VMID: vmID})
}

func (m *Remote) ConsoleLog(ctx context.Context, vmID string, tail int) ([]api.LogLine, error) {
	return call[[]api.LogLine](ctx, m, pathHyper+"ConsoleLog", consoleArgs{VMID: vmID, Tail: tail})
}

func (m *Remote) EnsureDisk(ctx context.Context, d engine.DiskRef, sizeGiB int) error {
	_, err := call[none](ctx, m, pathHyper+"EnsureDisk", diskArgs{D: d, SizeGiB: sizeGiB})
	return err
}

func (m *Remote) DeleteDisk(ctx context.Context, diskID string) error {
	_, err := call[none](ctx, m, pathHyper+"DeleteDisk", diskIDArgs{DiskID: diskID})
	return err
}

// SnapshotDisk streams the agent's copy into dest on this machine.
func (m *Remote) SnapshotDisk(ctx context.Context, d engine.DiskRef, dest string) (int64, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	var size int64
	err = m.readStream(ctx, pathHyper+"SnapshotDisk", diskArgs{D: d}, f, &size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return 0, err
	}
	return size, nil
}

// CopyDisk uploads the snapshot file first, then fills the disk from the agent's copy.
func (m *Remote) CopyDisk(ctx context.Context, src string, diskID string, sizeGiB int) error {
	digest, err := m.Upload(ctx, src)
	if err != nil {
		return err
	}
	_, err = call[none](ctx, m, pathHyper+"CopyDisk", copyDiskArgs{File: digest, DiskID: diskID, SizeGiB: sizeGiB})
	return err
}

func (m *Remote) CachedImages(ctx context.Context) []api.CachedImage {
	imgs, _ := call[[]api.CachedImage](ctx, m, pathHyper+"CachedImages", none{})
	return imgs
}

func (m *Remote) VMStats(ctx context.Context, vmID string) (engine.ProcessStats, bool, error) {
	res, err := call[vmStatsResult](ctx, m, pathHyper+"VMStats", vmArgs{VMID: vmID})
	return res.Stats, res.OK, err
}

func (m *Remote) ConfigureMesh(ctx context.Context, cfg engine.MeshConfig) (engine.MeshStatus, error) {
	return call[engine.MeshStatus](ctx, m, pathDriver+"ConfigureMesh", meshArgs{Cfg: cfg})
}

func (m *Remote) MeshStatus(ctx context.Context) (engine.MeshStatus, error) {
	return call[engine.MeshStatus](ctx, m, pathDriver+"MeshStatus", none{})
}

func (m *Remote) NetworkEndpoints(ctx context.Context) ([]engine.NetworkInfo, error) {
	return call[[]engine.NetworkInfo](ctx, m, pathDriver+"NetworkEndpoints", none{})
}

func (m *Remote) StopResource(ctx context.Context, r *domain.Resource) error {
	return m.driverCall(ctx, "StopResource", resourceArgs{R: bare(r)})
}

func (m *Remote) ExportData(ctx context.Context, r *domain.Resource, w io.Writer) error {
	return m.readStream(ctx, pathDriver+"ExportData", resourceArgs{R: bare(r)}, w, nil)
}

func (m *Remote) ImportData(ctx context.Context, r *domain.Resource, src io.Reader) error {
	return m.writeCall(ctx, pathDriver+"ImportData", resourceArgs{R: bare(r)}, src)
}

func (m *Remote) ExportDisk(ctx context.Context, d engine.DiskRef, w io.Writer) error {
	return m.readStream(ctx, pathHyper+"ExportDisk", diskArgs{D: d}, w, nil)
}

func (m *Remote) ImportDisk(ctx context.Context, d engine.DiskRef, src io.Reader) error {
	return m.writeCall(ctx, pathHyper+"ImportDisk", diskArgs{D: d}, src)
}
