package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

// Server serves one machine's engine to the control plane (ADR-042). It keeps no state but uploaded files
// and the callbacks of calls in flight.
type Server struct {
	Driver     engine.Driver
	Hypervisor engine.Hypervisor
	MachineID  string
	// FilesDir holds files the control plane uploaded (snapshot copies), by digest.
	FilesDir string
	// TunnelPublicKey reads the machine's WireGuard public key; "" before the mesh made one (ADR-045).
	TunnelPublicKey func(context.Context) string
	// Sweep removes the containers of resources not in keep (ADR-046); nil when the driver has none to remove.
	Sweep func(ctx context.Context, keep []string) ([]string, error)
	Log   *slog.Logger
	// StateDir keeps the highest epoch seen and, on a standby, the control plane's copies (ADR-057); ""
	// keeps the epoch in memory only (tests).
	StateDir string
	// Standby serves the copy endpoints: this machine holds copies of the control plane.
	Standby bool

	certs       *certs // nil in tests that serve a fixed certificate
	mu          sync.Mutex
	calls       map[string]chan continueBody
	epoch       int64
	epochRead   bool
	lastContact *time.Time
}

type handler func(w http.ResponseWriter, r *http.Request)

// Handler routes the agent protocol (L3-002 §11.11).
func (s *Server) Handler() http.Handler {
	s.calls = map[string]chan continueBody{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+pathInfo, s.info)
	for name, h := range s.driverCalls() {
		mux.HandleFunc("POST "+pathDriver+name, s.withDeadline(h))
	}
	for name, h := range s.hypervisorCalls() {
		mux.HandleFunc("POST "+pathHyper+name, s.withDeadline(h))
	}
	mux.HandleFunc("POST "+pathContinue+"{id}/continue", s.continueCall)
	mux.HandleFunc("HEAD "+pathFiles+"{sha}", s.hasFile)
	mux.HandleFunc("PUT "+pathFiles+"{sha}", s.putFile)
	mux.HandleFunc("POST "+pathSweep, s.withDeadline(s.sweep))
	if s.certs != nil {
		mux.HandleFunc("POST "+pathRenew, s.certs.renew)
		mux.HandleFunc("POST "+pathCert, s.certs.install)
	}
	if s.Standby {
		mux.HandleFunc("PUT "+pathStandbyCopy, s.putStandbyCopy)
		mux.HandleFunc("GET "+pathStandbyState, s.standbyState)
	}
	return s.fence(mux)
}

// withDeadline gives the call the control plane's deadline; the request's context already ends when the
// control plane hangs up.
func (s *Server) withDeadline(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, ok := parseDeadline(r.Header.Get(headerDeadline)); ok {
			ctx, cancel := context.WithDeadline(r.Context(), t)
			defer cancel()
			r = r.WithContext(ctx)
		}
		h(w, r)
	}
}

// AgentInfo describes this machine.
func (s *Server) AgentInfo(ctx context.Context) Info {
	info := Info{Version: version.Version, Protocol: Protocol, MachineID: s.MachineID, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Engine: api.Engine{Driver: s.Driver.Name()}}
	if ei, err := s.Driver.Info(ctx); err != nil {
		info.Engine.Message = err.Error()
	} else {
		info.Engine.Available, info.Engine.Version = true, ei.Version
		info.CPUs, info.MemoryTotalBytes = ei.CPUs, ei.MemoryTotalBytes
	}
	if s.Hypervisor != nil {
		vs := s.Hypervisor.Check(ctx)
		info.VMs = vs.VMSupport
		if vs.CPUs > 0 && info.CPUs == 0 {
			info.CPUs = vs.CPUs
		}
	} else {
		info.VMs = api.VMSupport{Message: "VMs aren't set up on this machine."}
	}
	if s.TunnelPublicKey != nil {
		info.TunnelPublicKey = s.TunnelPublicKey(ctx)
	}
	return info
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeResult(w, s.AgentInfo(ctx))
}

func writeResult(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	we := toWire(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(classStatus[we.Class])
	_ = json.NewEncoder(w).Encode(we)
}

// unary turns a typed call into a handler: JSON arguments in, JSON result or a classed error out.
func unary[A, R any](f func(ctx context.Context, a A) (R, error)) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		var a A
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&a); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, engine.Permanent(fmt.Errorf("arguments: %w", err)))
			return
		}
		res, err := f(r.Context(), a)
		if err != nil {
			writeError(w, err)
			return
		}
		writeResult(w, res)
	}
}

// none is the result of a call that returns only an error.
type none struct{}

func done(err error) (none, error) { return none{}, err }

// stream writes the lines of a callback stream; Started waits for the control plane's answer.
type stream struct {
	s   *Server
	w   http.ResponseWriter
	mu  sync.Mutex
	enc *json.Encoder
	ctx context.Context
}

func (s *Server) newStream(w http.ResponseWriter, r *http.Request) *stream {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	return &stream{s: s, w: w, enc: json.NewEncoder(w), ctx: r.Context()}
}

func (st *stream) send(e event) {
	st.mu.Lock()
	defer st.mu.Unlock()
	_ = st.enc.Encode(e)
	if f, ok := st.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (st *stream) Step(name string, state api.StepState) { st.send(event{Step: name, State: state}) }
func (st *stream) Note(message string)                   { st.send(event{Note: &message}) }
func (st *stream) Event(typ, message string)             { st.send(event{Event: typ, Message: message}) }

// Write sends build log output as log lines.
func (st *stream) Write(p []byte) (int, error) {
	line := string(p)
	st.send(event{Log: &line})
	return len(p), nil
}

// started asks the control plane to run the Started hook and waits for its answer.
func (st *stream) started(ctx context.Context) error {
	id := randomID()
	ch := make(chan continueBody, 1)
	st.s.mu.Lock()
	st.s.calls[id] = ch
	st.s.mu.Unlock()
	defer func() {
		st.s.mu.Lock()
		delete(st.s.calls, id)
		st.s.mu.Unlock()
	}()
	st.send(event{Started: id})
	select {
	case c := <-ch:
		if c.Error != "" {
			return errors.New(c.Error)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (st *stream) finish(result any, err error) {
	if err != nil {
		we := toWire(err)
		st.send(event{Error: &we})
		return
	}
	b, merr := json.Marshal(result)
	if merr != nil {
		we := toWire(merr)
		st.send(event{Error: &we})
		return
	}
	st.send(event{Result: b})
}

func (s *Server) continueCall(w http.ResponseWriter, r *http.Request) {
	var c continueBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ch, ok := s.calls[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such call", http.StatusNotFound)
		return
	}
	ch <- c
	w.WriteHeader(http.StatusNoContent)
}

// ensureLike serves Ensure and EnsureVMLink: a callback stream, with Started when the caller has a hook.
func (s *Server) ensureLike(f func(ctx context.Context, in engine.EnsureInput, rep engine.Reporter) (engine.Outputs, error)) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		var wi wireInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&wi); err != nil {
			writeError(w, engine.Permanent(fmt.Errorf("arguments: %w", err)))
			return
		}
		in, err := wi.input()
		if err != nil {
			writeError(w, err)
			return
		}
		st := s.newStream(w, r)
		if wi.HasStarted {
			in.Started = st.started
		}
		out, err := f(r.Context(), in, st)
		st.finish(out, err)
	}
}

// writeTo serves a call that writes its result into a stream: the body is the data, the result and any
// error come in trailers (Dump, SnapshotVolume).
func writeTo[A any](f func(ctx context.Context, a A, w io.Writer) (any, error)) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		var a A
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&a); err != nil {
			writeError(w, engine.Permanent(fmt.Errorf("arguments: %w", err)))
			return
		}
		w.Header().Set("Trailer", trailerResult+", "+trailerError)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		res, err := f(r.Context(), a, w)
		if err != nil {
			b, _ := json.Marshal(toWire(err))
			w.Header().Set(trailerError, string(b))
			return
		}
		b, _ := json.Marshal(res)
		w.Header().Set(trailerResult, string(b))
	}
}

// readFrom serves a call that reads a stream: the arguments come in a header, the body is the data.
func readFrom[A any](f func(ctx context.Context, a A, src io.Reader) error) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		var a A
		if err := decodeArgs(r.Header.Get(headerArgs), &a); err != nil {
			writeError(w, engine.Permanent(err))
			return
		}
		if err := f(r.Context(), a, r.Body); err != nil {
			writeError(w, err)
			return
		}
		writeResult(w, none{})
	}
}

func (s *Server) driverCalls() map[string]handler {
	d := s.Driver
	return map[string]handler{
		"Ping":   unary(func(ctx context.Context, _ none) (none, error) { return done(d.Ping(ctx)) }),
		"Info":   unary(func(ctx context.Context, _ none) (engine.Info, error) { return d.Info(ctx) }),
		"Ensure": s.ensureLike(d.Ensure),
		"EnsureVMLink": s.ensureLike(func(ctx context.Context, in engine.EnsureInput, _ engine.Reporter) (engine.Outputs, error) {
			return d.EnsureVMLink(ctx, in)
		}),
		"Delete": unary(func(ctx context.Context, a resourceStoreArgs) (none, error) { return done(d.Delete(ctx, a.R, a.Store)) }),
		"Health": unary(func(ctx context.Context, a resourceStoreArgs) (engine.Health, error) {
			return d.Health(ctx, a.R, a.Store)
		}),
		"Restart":       unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.Restart(ctx, a.R)) }),
		"Logs":          unary(func(ctx context.Context, a logsArgs) ([]api.LogLine, error) { return d.Logs(ctx, a.R, a.Tail) }),
		"ConfigureEdge": unary(func(ctx context.Context, a edgeArgs) (none, error) { return done(d.ConfigureEdge(ctx, a.Cfg)) }),
		"Certificate": unary(func(ctx context.Context, a hostnameArgs) (engine.Certificate, error) {
			return d.Certificate(ctx, a.Hostname)
		}),
		"EdgeStatus": unary(func(ctx context.Context, _ none) (engine.EdgeStatus, error) { return d.EdgeStatus(ctx), nil }),
		"RootCA":     unary(func(ctx context.Context, _ none) ([]byte, error) { return d.RootCA(ctx) }),
		"StartJob":   unary(func(ctx context.Context, a jobArgs) (none, error) { return done(d.StartJob(ctx, a.Run)) }),
		"WaitJob": unary(func(ctx context.Context, a waitArgs) (engine.JobResult, error) {
			return d.WaitJob(ctx, a.RunID, a.Timeout)
		}),
		"Build":    s.build,
		"HasImage": unary(func(ctx context.Context, a refArgs) (bool, error) { return d.HasImage(ctx, a.R, a.Ref) }),
		"RemoveBuiltImages": unary(func(ctx context.Context, a keepArgs) (none, error) {
			return done(d.RemoveBuiltImages(ctx, a.R, a.Keep))
		}),
		"EnsureNetwork": unary(func(ctx context.Context, a networkArgs) (engine.Outputs, error) {
			return d.EnsureNetwork(ctx, engine.NetworkInput{Resource: a.R, Spec: a.Spec})
		}),
		"DeleteNetwork": unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.DeleteNetwork(ctx, a.R)) }),
		"ApplyFirewall": unary(func(ctx context.Context, a firewallArgs) (int, error) { return d.ApplyFirewall(ctx, a.R, a.Policy) }),
		"Endpoints": unary(func(ctx context.Context, a resourceArgs) ([]string, error) {
			addrs, err := d.Endpoints(ctx, a.R)
			out := make([]string, len(addrs))
			for i, a := range addrs {
				out[i] = a.String()
			}
			return out, err
		}),
		"LoadBalancerStatus": unary(func(ctx context.Context, a resourceArgs) (lb.Status, error) { return d.LoadBalancerStatus(ctx, a.R) }),
		"Dump": writeTo(func(ctx context.Context, a wireInput, w io.Writer) (any, error) {
			in, err := a.input()
			if err != nil {
				return nil, err
			}
			return d.Dump(ctx, in, w)
		}),
		"Restore": readFrom(func(ctx context.Context, a wireInput, src io.Reader) error {
			in, err := a.input()
			if err != nil {
				return err
			}
			return d.Restore(ctx, in, src)
		}),
		"EnsureVolume": unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.EnsureVolume(ctx, a.R)) }),
		"DeleteVMLink": unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.DeleteVMLink(ctx, a.R)) }),
		"VolumeUsage":  unary(func(ctx context.Context, a resourceArgs) (int64, error) { return d.VolumeUsage(ctx, a.R) }),
		"SnapshotVolume": writeTo(func(ctx context.Context, a resourceArgs, w io.Writer) (any, error) {
			return none{}, d.SnapshotVolume(ctx, a.R, w)
		}),
		"RestoreVolume": readFrom(func(ctx context.Context, a resourceArgs, src io.Reader) error { return d.RestoreVolume(ctx, a.R, src) }),
		"ConfigureBroker": unary(func(ctx context.Context, a brokerArgs) (engine.BrokerStatus, error) {
			return d.ConfigureBroker(ctx, a.Cfg)
		}),
		"BrokerStatus": unary(func(ctx context.Context, _ none) (engine.BrokerStatus, error) { return d.BrokerStatus(ctx), nil }),
		"PurgeCache":   unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.PurgeCache(ctx, a.R)) }),
		"CacheUsage":   unary(func(ctx context.Context, a resourceArgs) (int64, error) { return d.CacheUsage(ctx, a.R) }),
		"SetBucketPublicRead": unary(func(ctx context.Context, a publicReadArgs) (none, error) {
			return done(d.SetBucketPublicRead(ctx, a.Bucket, a.Public, a.Store))
		}),
		"SetProjectStoreAccess": unary(func(ctx context.Context, a storeAccessArgs) (none, error) {
			return done(d.SetProjectStoreAccess(ctx, a.User, a.Buckets, a.Root))
		}),
		"SetStoreUserAccess": unary(func(ctx context.Context, a storeUserArgs) (none, error) {
			return done(d.SetStoreUserAccess(ctx, a.User, a.Grants, a.Root))
		}),
		"Stats": unary(func(ctx context.Context, a resourceArgs) ([]engine.ContainerStats, error) { return d.Stats(ctx, a.R) }),
		"EdgeRequests": unary(func(ctx context.Context, _ none) (map[string]engine.RequestCounters, error) {
			return d.EdgeRequests(ctx)
		}),
		"LogsSince": unary(func(ctx context.Context, a logsSinceArgs) ([]engine.ContainerLogLine, error) {
			return d.LogsSince(ctx, a.R, a.Since, a.Fallback, a.Limit)
		}),
		"StopResource": unary(func(ctx context.Context, a resourceArgs) (none, error) { return done(d.StopResource(ctx, a.R)) }),
		"ExportData": writeTo(func(ctx context.Context, a resourceArgs, w io.Writer) (any, error) {
			return none{}, d.ExportData(ctx, a.R, w)
		}),
		"ImportData":       readFrom(func(ctx context.Context, a resourceArgs, src io.Reader) error { return d.ImportData(ctx, a.R, src) }),
		"ConfigureMesh":    unary(func(ctx context.Context, a meshArgs) (engine.MeshStatus, error) { return d.ConfigureMesh(ctx, a.Cfg) }),
		"MeshStatus":       unary(func(ctx context.Context, _ none) (engine.MeshStatus, error) { return d.MeshStatus(ctx) }),
		"NetworkEndpoints": unary(func(ctx context.Context, _ none) ([]engine.NetworkInfo, error) { return d.NetworkEndpoints(ctx) }),
		"EnsureConnector": unary(func(ctx context.Context, a connectorArgs) (engine.ConnectorState, error) {
			return d.EnsureConnector(ctx, a.C)
		}),
		"RemoveConnector": unary(func(ctx context.Context, a connectorArgs) (none, error) { return done(d.RemoveConnector(ctx, a.C)) }),
		"ConnectorState": unary(func(ctx context.Context, a connectorArgs) (engine.ConnectorState, error) {
			return d.ConnectorState(ctx, a.C)
		}),
		"ConnectorExec": unary(func(ctx context.Context, a connectorExecArgs) (connectorExecResult, error) {
			out, code, err := d.ConnectorExec(ctx, a.C, a.Cmd)
			return connectorExecResult{Output: out, ExitCode: code}, err
		}),
		"ConnectorFetch": unary(func(ctx context.Context, a connectorFetchArgs) ([]byte, error) {
			return d.ConnectorFetch(ctx, a.C, a.Port, a.Path)
		}),
		"GateBindAddress": unary(func(ctx context.Context, _ none) (string, error) { return d.GateBindAddress(ctx) }),
		"EdgeAddress":     unary(func(ctx context.Context, a edgeNetworkArgs) (string, error) { return d.EdgeAddress(ctx, a.Network) }),
	}
}

// build unpacks the uploaded context into a temporary directory, builds, and streams the log.
func (s *Server) build(w http.ResponseWriter, r *http.Request) {
	var a buildArgs
	if err := decodeArgs(r.Header.Get(headerArgs), &a); err != nil {
		writeError(w, engine.Permanent(err))
		return
	}
	dir, err := os.MkdirTemp(s.FilesDir, "build-")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.RemoveAll(dir)
	if err := untar(r.Body, dir); err != nil {
		writeError(w, engine.Permanent(fmt.Errorf("unpack the build context: %w", err)))
		return
	}
	st := s.newStream(w, r)
	res, err := s.Driver.Build(r.Context(), engine.BuildInput{Resource: a.Resource, Tag: a.Tag, ContextDir: dir, Dockerfile: a.Dockerfile,
		Excludes: a.Excludes, BuildArgs: a.BuildArgs}, st)
	st.finish(res, err)
}

func (s *Server) hypervisorCalls() map[string]handler {
	h := s.Hypervisor
	if h == nil {
		return map[string]handler{}
	}
	return map[string]handler{
		"Check": unary(func(ctx context.Context, _ none) (engine.VMSupport, error) { return h.Check(ctx), nil }),
		"EnsureVM": func(w http.ResponseWriter, r *http.Request) {
			var in engine.VMInput
			if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&in); err != nil {
				writeError(w, engine.Permanent(fmt.Errorf("arguments: %w", err)))
				return
			}
			if in.Image.SnapshotFile != "" {
				p, err := s.filePath(in.Image.SnapshotFile)
				if err != nil {
					writeError(w, err)
					return
				}
				in.Image.SnapshotFile = p
			}
			st := s.newStream(w, r)
			res, err := h.EnsureVM(r.Context(), in, st)
			st.finish(res, err)
		},
		"StopVM":   unary(func(ctx context.Context, a vmArgs) (none, error) { return done(h.StopVM(ctx, a.VMID)) }),
		"DeleteVM": unary(func(ctx context.Context, a vmArgs) (none, error) { return done(h.DeleteVM(ctx, a.VMID)) }),
		"VMStatus": unary(func(ctx context.Context, a vmArgs) (engine.VMStatus, error) { return h.VMStatus(ctx, a.VMID) }),
		"ConsoleLog": unary(func(ctx context.Context, a consoleArgs) ([]api.LogLine, error) {
			return h.ConsoleLog(ctx, a.VMID, a.Tail)
		}),
		"EnsureDisk": unary(func(ctx context.Context, a diskArgs) (none, error) { return done(h.EnsureDisk(ctx, a.D, a.SizeGiB)) }),
		"DeleteDisk": unary(func(ctx context.Context, a diskIDArgs) (none, error) { return done(h.DeleteDisk(ctx, a.DiskID)) }),
		"SnapshotDisk": writeTo(func(ctx context.Context, a diskArgs, w io.Writer) (any, error) {
			f, err := os.CreateTemp(s.FilesDir, "snapshot-")
			if err != nil {
				return nil, err
			}
			path := f.Name()
			f.Close()
			defer os.Remove(path)
			size, err := h.SnapshotDisk(ctx, a.D, path)
			if err != nil {
				return nil, err
			}
			src, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer src.Close()
			if _, err := io.Copy(w, src); err != nil {
				return nil, err
			}
			return size, nil
		}),
		"CopyDisk": unary(func(ctx context.Context, a copyDiskArgs) (none, error) {
			p, err := s.filePath(a.File)
			if err != nil {
				return none{}, err
			}
			return done(h.CopyDisk(ctx, p, a.DiskID, a.SizeGiB))
		}),
		"ExportDisk": writeTo(func(ctx context.Context, a diskArgs, w io.Writer) (any, error) {
			return none{}, h.ExportDisk(ctx, a.D, w)
		}),
		"ImportDisk":   readFrom(func(ctx context.Context, a diskArgs, src io.Reader) error { return h.ImportDisk(ctx, a.D, src) }),
		"CachedImages": unary(func(ctx context.Context, _ none) ([]api.CachedImage, error) { return h.CachedImages(ctx), nil }),
		"VMStats": unary(func(ctx context.Context, a vmArgs) (vmStatsResult, error) {
			st, ok, err := h.VMStats(ctx, a.VMID)
			return vmStatsResult{Stats: st, OK: ok}, err
		}),
	}
}

func (s *Server) sweep(w http.ResponseWriter, r *http.Request) {
	unary(func(ctx context.Context, a sweepArgs) (sweepResult, error) {
		if s.Sweep == nil {
			return sweepResult{}, nil
		}
		removed, err := s.Sweep(ctx, a.Keep)
		return sweepResult{Removed: removed}, err
	})(w, r)
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// filePath is an uploaded file's path, by digest.
func (s *Server) filePath(digest string) (string, error) {
	if !digestPattern.MatchString(digest) {
		return "", engine.Permanent(fmt.Errorf("%q isn't a file digest", digest))
	}
	p := filepath.Join(s.FilesDir, digest)
	if _, err := os.Stat(p); err != nil {
		return "", engine.Permanent(fmt.Errorf("the file %s wasn't uploaded to this machine", digest[:12]))
	}
	return p, nil
}

func (s *Server) hasFile(w http.ResponseWriter, r *http.Request) {
	if _, err := s.filePath(r.PathValue("sha")); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// putFile stores an upload under its digest, refusing a body whose digest differs.
func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	want := r.PathValue("sha")
	if !digestPattern.MatchString(want) {
		writeError(w, engine.Permanent(errors.New("name the file by its SHA-256")))
		return
	}
	f, err := os.CreateTemp(s.FilesDir, "upload-")
	if err != nil {
		writeError(w, err)
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hash), r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		writeError(w, engine.Permanent(fmt.Errorf("the upload's digest is %s, not %s", got[:12], want[:12])))
		return
	}
	if err := os.Rename(tmp, filepath.Join(s.FilesDir, want)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(hex.EncodeToString(b))
}
