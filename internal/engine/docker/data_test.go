//go:build docker

package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/bcrypt"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/messaging/natsbroker"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.6 on real Docker (L5-002 §9): database engines with dumps and restores, the broker, OpenSearch,
// Mailpit capture and relay, nginx caches and FFmpeg runs.

func run(t *testing.T, d *Driver, name string, env []string, cmd ...string) string {
	t.Helper()
	var out bytes.Buffer
	code, stderr, err := d.execStream(context.Background(), name, cmd, env, nil, &out)
	if err != nil || code != 0 {
		t.Fatalf("%v: exit %d %v %s %s", cmd, code, err, stderr, out.String())
	}
	return out.String()
}

// engineSQL runs a statement as the database's own user, with the password from the environment.
func engineSQL(t *testing.T, d *Driver, r *domain.Resource, s api.DatabaseSpec, password, stmt string) string {
	t.Helper()
	ident := domain.DatabaseIdentifier(r.Name)
	env := []string{"ES_USER=" + ident, "ES_PW=" + password, "ES_DB=" + ident, "ES_Q=" + stmt}
	name := engine.ContainerName(r)
	switch s.Engine {
	case api.EngineMySQL:
		return run(t, d, name, env, "sh", "-c", `MYSQL_PWD="$ES_PW" mysql -h 127.0.0.1 -u"$ES_USER" -N -e "$ES_Q" "$ES_DB"`)
	case api.EngineMongoDB:
		return run(t, d, name, env, "sh", "-c", `mongosh --quiet --host 127.0.0.1 -u "$ES_USER" -p "$ES_PW" --authenticationDatabase admin "$ES_DB" --eval "$ES_Q"`)
	case api.EngineClickHouse:
		return run(t, d, name, env, "sh", "-c", `clickhouse-client --host 127.0.0.1 --database "$ES_DB" --query "$ES_Q"`)
	}
	t.Fatalf("no SQL for %s", s.Engine)
	return ""
}

func TestDatabaseEnginesStartDumpAndRestore(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	cases := []struct {
		spec                   api.DatabaseSpec
		setup, insert, wipe, q string
	}{
		{api.DatabaseSpec{Engine: api.EngineMySQL, Version: "8.4"}, "CREATE TABLE kept (v VARCHAR(20))", "INSERT INTO kept VALUES ('survives')", "DELETE FROM kept", "SELECT v FROM kept"},
		{api.DatabaseSpec{Engine: api.EngineMongoDB, Version: "7.0"}, "db.createCollection('kept')", "db.kept.insertOne({v: 'survives'})", "db.kept.deleteMany({})", "db.kept.findOne().v"},
		{api.DatabaseSpec{Engine: api.EngineClickHouse, Version: "26.3"}, "CREATE TABLE kept (v String) ENGINE = MergeTree ORDER BY v", "INSERT INTO kept VALUES ('survives')", "TRUNCATE TABLE kept", "SELECT v FROM kept"},
	}
	for _, c := range cases {
		t.Run(c.spec.Engine, func(t *testing.T) {
			r := resource(api.KindDatabase, "it-"+c.spec.Engine+"-"+suffix())
			cleanup(t, d, r, engine.ObjectStore{})
			in := engine.EnsureInput{Resource: r, Spec: c.spec, Password: "Pw-" + suffix() + suffix()}
			out := ensure(t, d, in)
			if out["engine"] != c.spec.Engine || out["port"] == nil || out["internalPort"] != engine.DatabasePort(c.spec.Engine) {
				t.Errorf("outputs: %v", out)
			}
			if c.spec.Engine == api.EngineClickHouse {
				// The HTTP interface from the machine, with the password; the default user is off.
				url := fmt.Sprintf("http://127.0.0.1:%d/?query=SELECT%%201", out["port"].(int))
				req, _ := http.NewRequest(http.MethodGet, url, nil)
				req.SetBasicAuth(out["username"].(string), in.Password)
				res, err := http.DefaultClient.Do(req)
				if err != nil || res.StatusCode != 200 {
					t.Fatalf("ClickHouse over HTTP: %v %v", res, err)
				}
				res.Body.Close()
				if anon, err := http.Get(url); err == nil && anon.StatusCode == 200 {
					t.Error("ClickHouse answered without a password")
				}
				if out[engine.OutputNativePort] == nil {
					t.Error("no native port")
				}
			}
			engineSQL(t, d, r, c.spec, in.Password, c.setup)
			engineSQL(t, d, r, c.spec, in.Password, c.insert)
			var dump bytes.Buffer
			info, err := d.Dump(ctx, in, &dump)
			if err != nil || dump.Len() == 0 || info.Ext != engine.DumpExt(c.spec) {
				t.Fatalf("dump: %+v %d bytes %v", info, dump.Len(), err)
			}
			engineSQL(t, d, r, c.spec, in.Password, c.wipe)
			if err := d.Restore(ctx, in, &dump); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if got := strings.TrimSpace(engineSQL(t, d, r, c.spec, in.Password, c.q)); got != "survives" {
				t.Errorf("after restore: %q", got)
			}
		})
	}
}

func TestBrokerStartsReloadsUsersAndGoesAway(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	if c, _ := d.inspect(ctx, engine.BrokerName); c != nil {
		t.Skip("a broker is already running on this machine; not touching it")
	}
	t.Cleanup(func() { _, _ = d.ConfigureBroker(context.Background(), engine.BrokerConfig{Empty: true}) })
	hash := func(pw string) string {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		return string(h)
	}
	users := []messaging.User{{Name: messaging.SystemUserName, PasswordHash: hash("sys")}, messaging.QueueUser("jobs", hash("q1"))}
	st, err := d.ConfigureBroker(ctx, engine.BrokerConfig{Config: messaging.RenderConfig(users)})
	if err != nil || !st.Available {
		t.Fatalf("configure: %+v %v", st, err)
	}
	b, err := natsbroker.Open(ctx, messaging.Endpoint{URL: "nats://" + st.Address, User: messaging.SystemUserName, Password: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.EnsureQueue(ctx, messaging.Queue{Name: "jobs", VisibilityTimeout: time.Minute, Retention: time.Hour, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(ctx, "jobs", messaging.Message{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	// A new user arrives by reload, the system connection stays, and messages survive a restart.
	users = append(users, messaging.TopicUser("events", hash("t1")))
	st2, err := d.ConfigureBroker(ctx, engine.BrokerConfig{Config: messaging.RenderConfig(users)})
	if err != nil || st2.Address != st.Address {
		t.Fatalf("reload: %+v %v", st2, err)
	}
	if t2, err := natsbroker.Open(ctx, messaging.Endpoint{URL: "nats://" + st.Address, User: "t-events", Password: "t1"}); err != nil {
		t.Errorf("the reloaded user: %v", err)
	} else {
		t2.Close()
	}
	if err := d.cli.ContainerRestart(ctx, engine.BrokerName, containerStop()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	got, err := b.Receive(ctx, "jobs", 1, 5*time.Second)
	if err != nil || len(got) != 1 || got[0].Body != "hello" {
		t.Errorf("after a broker restart: %+v %v", got, err)
	}
	if st, _ := d.ConfigureBroker(ctx, engine.BrokerConfig{Empty: true}); st.Available {
		t.Error("the broker stayed")
	}
	if c, _ := d.inspect(ctx, engine.BrokerName); c != nil {
		t.Error("the broker container stayed")
	}
}

func TestSearchNeedsItsPassword(t *testing.T) {
	d := newDriver(t)
	r := resource(api.KindSearch, "it-search-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	in := engine.EnsureInput{Resource: r, Spec: api.SearchSpec{Version: api.SearchVersion3, MemoryMiB: 1024}, Password: "Pw-" + suffix() + suffix() + "-Hc7"}
	out := ensure(t, d, in)
	url := out["url"].(string)
	req, _ := http.NewRequest(http.MethodPut, url+"/products/_doc/1?refresh=true", strings.NewReader(`{"name":"red shoe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", in.Password)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode/100 != 2 {
		t.Fatalf("index: %v %v", res, err)
	}
	res.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, url+"/products/_search?q=shoe", nil)
	req.SetBasicAuth("admin", in.Password)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "red shoe") {
		t.Errorf("search: %s", body)
	}
	if anon, err := http.Get(url); err != nil || anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a password: %v %v", anon, err)
	}
	if out["version"] != "3.3.2" {
		t.Errorf("version %v", out["version"])
	}

	// Half the memory is the Java heap (SRC-02); a memory change recreates the container and the index
	// is still there (SRC-04).
	c, _ := d.inspect(context.Background(), engine.ContainerName(r))
	if !slices.Contains(c.Config.Env, "OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m") {
		t.Errorf("heap: %v", c.Config.Env)
	}
	in.Spec = api.SearchSpec{Version: api.SearchVersion3, MemoryMiB: 1536}
	r.Generation++ // an update
	out = ensure(t, d, in)
	after, _ := d.inspect(context.Background(), engine.ContainerName(r))
	if after.ID == c.ID || !slices.Contains(after.Config.Env, "OPENSEARCH_JAVA_OPTS=-Xms768m -Xmx768m") {
		t.Errorf("the memory change didn't recreate the container: %v", after.Config.Env)
	}
	req, _ = http.NewRequest(http.MethodGet, out["url"].(string)+"/products/_doc/1", nil)
	req.SetBasicAuth("admin", in.Password)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "red shoe") {
		t.Errorf("after recreation: %s", body)
	}
}

func TestEmailCapturesAndRelays(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	sink := resource(api.KindEmail, "it-sink-"+suffix())
	cleanup(t, d, sink, engine.ObjectStore{})
	sinkPw := "sink" + suffix() + suffix()
	sinkOut := ensure(t, d, engine.EnsureInput{Resource: sink, Spec: api.EmailSpec{Mode: api.EmailCapture, KeepMessages: 50}, Password: sinkPw})
	ep := func(r *domain.Resource, out engine.Outputs, pw string, relaying bool) mail.Endpoint {
		return mail.Endpoint{Key: r.ID, SMTPAddr: fmt.Sprintf("127.0.0.1:%d", out["port"].(int)), APIURL: out["webUrl"].(string), User: r.Name, Password: pw, Relaying: relaying}
	}
	c := mail.Mailpit{}
	id, err := c.Send(ctx, ep(sink, sinkOut, sinkPw, false), api.SendEmail{From: "shop@example.com", To: []string{"a@b.test"}, Bcc: []string{"audit@b.test"}, Subject: "Grüße", Text: "Hi", HTML: "<p>Hi</p>"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(ctx, ep(sink, sinkOut, sinkPw, false), 10)
	if err != nil || list.Total != 1 || list.Items[0].Subject != "Grüße" || list.Items[0].ID != id || list.Items[0].From != "shop@example.com" {
		t.Fatalf("list: %+v %v", list, err)
	}
	msg, err := c.Get(ctx, ep(sink, sinkOut, sinkPw, false), id)
	if err != nil || msg.HTML != "<p>Hi</p>" || !strings.Contains(msg.Text, "Hi") || !slices.Contains(msg.Bcc, "audit@b.test") || len(msg.Headers) == 0 {
		t.Errorf("get: %+v %v", msg, err)
	}
	if _, err := c.Send(ctx, ep(sink, sinkOut, "wrong", false), api.SendEmail{From: "a@b.test", To: []string{"c@d.test"}, Subject: "x", Text: "x"}); err == nil {
		t.Error("SMTP accepted a wrong password")
	}

	// A relaying service hands every message to the sink, over the enclave network.
	relay := resource(api.KindEmail, "it-relay-"+suffix())
	cleanup(t, d, relay, engine.ObjectStore{})
	relayPw := "relay" + suffix() + suffix()
	relayOut := ensure(t, d, engine.EnsureInput{Resource: relay, Spec: api.EmailSpec{Mode: api.EmailRelaying, KeepMessages: 50}, Password: relayPw,
		Relay: &engine.Relay{Host: engine.ContainerName(sink), Port: engine.SMTPPort, TLS: api.RelayNone, Username: sink.Name, Password: sinkPw}})
	if _, err := c.Send(ctx, ep(relay, relayOut, relayPw, true), api.SendEmail{From: "shop@example.com", To: []string{"x@y.test"}, Subject: "Relayed", Text: "via relay"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		l, _ := c.List(ctx, ep(sink, sinkOut, sinkPw, false), 10)
		if l.Total == 2 && l.Items[0].Subject == "Relayed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the relayed message didn't reach the sink: %+v; relay logs: %s", l, d.tail(ctx, engine.ContainerName(relay), 10))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestCDNCachesPurgesAndServesStale(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	app := resource(api.KindApp, "it-origin-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	ensure(t, d, engine.EnsureInput{Resource: app, Spec: api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Replicas: 1, Strategy: api.StrategyRolling, MemoryMiB: 64}, Revision: 1})
	cdn := resource(api.KindCDN, "it-cdn-"+suffix())
	cleanup(t, d, cdn, engine.ObjectStore{})
	cfg := &engine.CDNConfig{Upstream: engine.ContainerName(app) + ":80", TTLSeconds: 60, MaxSizeMiB: 64, OriginIsApp: true}
	ensure(t, d, engine.EnsureInput{Resource: cdn, Spec: api.CDNSpec{Hostname: "cdn.localhost", Origin: "app/" + app.Name}, CDN: cfg})
	name := engine.ContainerName(cdn)
	// The cache is bounded and kept on the CDN's volume, least recently used out first (CDN-04).
	if conf, err := exec.Command("docker", "exec", name, "cat", "/etc/nginx/conf.d/default.conf").Output(); err != nil || !strings.Contains(string(conf), "max_size=64m") {
		t.Errorf("cache bound: %v\n%s", err, conf)
	}
	get := func() string {
		return run(t, d, name, nil, "sh", "-c", "wget -S -q -O /dev/null http://127.0.0.1:8080/ 2>&1 | grep -i x-cache | tr -d ' \\r' | cut -d: -f2")
	}
	if first, second := strings.TrimSpace(get()), strings.TrimSpace(get()); first != "MISS" || second != "HIT" {
		t.Fatalf("X-Cache: %q then %q", first, second)
	}
	if n, err := d.CacheUsage(ctx, cdn); err != nil || n == 0 {
		t.Errorf("cache usage: %d %v", n, err)
	}
	if err := d.PurgeCache(ctx, cdn); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(get()); got != "MISS" {
		t.Errorf("after purge: %q", got)
	}
	// The origin goes away: the cached copy is still served (CDN-06).
	if err := d.remove(ctx, revisionReplica(app)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	out := run(t, d, name, nil, "sh", "-c", "wget -S -q -O /dev/null http://127.0.0.1:8080/ 2>&1 | grep -ci 'HTTP/1.1 200'")
	if strings.TrimSpace(out) != "1" {
		t.Errorf("without its origin: %s", out)
	}
}

func revisionReplica(app *domain.Resource) string { return replicaName(app, 1, 1) }

func TestTranscodeConvertsAClipWithEveryPreset(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	// A two-second test clip with sound, made by the same FFmpeg image.
	clip, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "ffmpeg", d.opts.TranscodeImage, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=15", "-f", "lavfi", "-i", "sine=duration=2", "-shortest", "-c:v", "libx264", "-c:a", "aac",
		"-f", "matroska", "-").Output()
	if err != nil || len(clip) == 0 {
		t.Fatalf("make a clip: %v", err)
	}
	files := map[string]int64{}
	res, err := d.Transcode(ctx, engine.TranscodeRun{RunID: "run_it" + suffix(), ResourceID: "tc_it", Name: "it", InputName: "input.mkv", InputSize: int64(len(clip)),
		Presets: api.Presets, MemoryMiB: 1024, Timeout: 5 * time.Minute}, bytes.NewReader(clip), func(name string, size int64, r io.Reader) error {
		n, err := io.Copy(io.Discard, r)
		files[name] = n
		if n != size {
			return fmt.Errorf("%s: read %d of %d", name, n, size)
		}
		return err
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("transcode: %v exit %d\n%s", err, res.ExitCode, res.Output)
	}
	for _, want := range []string{"mp4-480p.mp4", "mp4-720p.mp4", "mp4-1080p.mp4", "webm-720p.webm", "hls-720p/index.m3u8", "mp3.mp3", "thumbnail.jpg"} {
		if files[want] == 0 {
			t.Errorf("missing %s in %v", want, files)
		}
	}
	// Not media: the run fails with FFmpeg's reason and writes nothing.
	bad := []byte("NOT MEDIA, just text")
	res, err = d.Transcode(ctx, engine.TranscodeRun{RunID: "run_it" + suffix(), ResourceID: "tc_it", Name: "it", InputName: "input.mp4", InputSize: int64(len(bad)),
		Presets: []string{api.PresetMP3}, MemoryMiB: 256, Timeout: time.Minute}, bytes.NewReader(bad), func(string, int64, io.Reader) error {
		t.Error("a failed run handed out a file")
		return nil
	})
	if err != nil || res.ExitCode == 0 || !strings.Contains(string(res.Output), "Invalid data") {
		t.Errorf("not media: %v exit %d %s", err, res.ExitCode, res.Output)
	}
}

// L2-012 TRX-02, TRX-03 with the real FFmpeg image: a portrait clip becomes a ladder whose master
// playlist lists every rung at its real size, whose rungs start each segment on a keyframe at the same
// time, and a rendition; a small landscape clip is never scaled up.
func TestTranscodeWritesALadderAndRenditions(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	clip := func(size string) []byte {
		b, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "ffmpeg", d.opts.TranscodeImage, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=duration=13:size="+size+":rate=30", "-f", "lavfi", "-i", "sine=duration=13:sample_rate=44100", "-shortest",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-f", "matroska", "-").Output()
		if err != nil || len(b) == 0 {
			t.Fatalf("make a %s clip: %v", size, err)
		}
		return b
	}
	convert := func(in []byte, o engine.TranscodeRun) string {
		dir := t.TempDir()
		o.RunID, o.ResourceID, o.Name, o.InputName, o.InputSize, o.MemoryMiB, o.Timeout = "run_it"+suffix(), "tc_it", "it", "input.mkv", int64(len(in)), 2048, 10*time.Minute
		res, err := d.Transcode(ctx, o, bytes.NewReader(in), func(name string, size int64, r io.Reader) error {
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
				return err
			}
			b, err := io.ReadAll(r)
			if err == nil && int64(len(b)) != size {
				err = fmt.Errorf("%s: read %d of %d", name, len(b), size)
			}
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, name), b, 0o644)
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("transcode: %v exit %d\n%s", err, res.ExitCode, res.Output)
		}
		return dir
	}
	probe := func(dir string, args ...string) string {
		out, err := exec.CommandContext(ctx, "docker", append([]string{"run", "--rm", "-v", dir + ":/w", "--entrypoint", "ffprobe", d.opts.TranscodeImage, "-v", "error"}, args...)...).Output()
		if err != nil {
			t.Fatalf("ffprobe %v: %v", args, err)
		}
		return strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	}
	size := func(dir, file string) string {
		return probe(dir, "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=s=x:p=0", "/w/"+file)
	}

	ladder := &api.HLSLadder{Name: "hls", SegmentSeconds: 6, GOPSeconds: 2, AudioKbps: 128, Rungs: []api.HLSRung{
		{Name: "360p", MaxWidth: 360, MaxHeight: 640, VideoKbps: 600}, {Name: "720p", MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2000}}}
	dir := convert(clip("1080x1920"), engine.TranscodeRun{HLS: ladder,
		Renditions: []api.Rendition{{Name: "selfie", Container: api.ContainerMP4, MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2500, AudioKbps: 128, GOPSeconds: 2}}})
	master, err := os.ReadFile(filepath.Join(dir, "hls/master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-STREAM-INF:BANDWIDTH=728000,RESOLUTION=360x640\n360p/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2128000,RESOLUTION=720x1280\n720p/index.m3u8\n"; string(master) != want {
		t.Errorf("master playlist:\n%s", master)
	}
	if got := size(dir, "selfie.mp4"); got != "720x1280" {
		t.Errorf("rendition size %s", got)
	}
	if got := probe(dir, "-select_streams", "a:0", "-show_entries", "stream=codec_name,sample_rate,channels", "-of", "csv=p=0", "/w/selfie.mp4"); got != "aac,48000,2" {
		t.Errorf("rendition audio %s", got)
	}
	var starts []string
	for _, r := range ladder.Rungs {
		if _, err := os.Stat(filepath.Join(dir, "hls", r.Name, "segment-002.ts")); err != nil {
			t.Errorf("%s: 13 s in 6 s segments makes 3: %v", r.Name, err)
		}
		starts = append(starts, probe(dir, "-select_streams", "v:0", "-show_entries", "frame=key_frame,pts_time", "-of", "csv=p=0", "-read_intervals", "%+#1", "/w/hls/"+r.Name+"/segment-001.ts"))
	}
	if !strings.HasPrefix(starts[0], "1,") || starts[0] != starts[1] {
		t.Errorf("segment 1 must start on a keyframe at the same time in every rung: %v", starts)
	}

	// A small landscape clip in a portrait box: fitted, never scaled up.
	dir = convert(clip("640x360"), engine.TranscodeRun{Renditions: []api.Rendition{
		{Name: "big", Container: api.ContainerMP4, MaxWidth: 1920, MaxHeight: 1080, VideoKbps: 4000, AudioKbps: 128, GOPSeconds: 2},
		{Name: "tall", Container: api.ContainerWebM, MaxWidth: 360, MaxHeight: 640, VideoKbps: 800, AudioKbps: 96, GOPSeconds: 2}}})
	if got := size(dir, "big.mp4"); got != "640x360" {
		t.Errorf("scaled up: %s", got)
	}
	if got := size(dir, "tall.webm"); got != "360x202" {
		t.Errorf("fitted: %s", got)
	}
}

func containerStop() container.StopOptions {
	timeout := 5
	return container.StopOptions{Timeout: &timeout}
}

// Docker picks a new host port on every restart unless the binding names one; EnclaveStack names it, so a
// restart keeps every address it reported (found while testing the broker, 2026-10-21).
func TestARestartKeepsThePublishedPort(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	r := resource(api.KindCache, "it-port-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	out := ensure(t, d, engine.EnsureInput{Resource: r, Spec: api.CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 64}})
	if err := d.Restart(ctx, r); err != nil {
		t.Fatal(err)
	}
	c, err := d.inspect(ctx, engine.ContainerName(r))
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if got := hostPort(c, engine.RedisPort); got != out["port"].(int) {
		t.Errorf("port %v before the restart, %d after", out["port"], got)
	}
}
