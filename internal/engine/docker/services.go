package docker

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.6 services (ADR-030 – ADR-033): container plans for the new database engines, search, email
// and CDN caches. Images are pinned by digest; the tag is kept for people reading the reference.

// EngineImages pins each database engine version; PostgreSQL keeps its v0.2 tags.
var EngineImages = map[string]map[string]string{
	api.EngineMySQL: {"8.4": "mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364"},
	api.EngineMongoDB: {
		"7.0": "mongo:7.0@sha256:c53852c00c23ca9f23fc39690350ffa9a80e3642fe8f96879726d8da0abe80a7",
		"8.0": "mongo:8.0@sha256:4a4ab5d4834207389e0d7270fac988e21ffedccecba77991a552f755df229907",
	},
	api.EngineClickHouse: {
		"25.8": "clickhouse/clickhouse-server:25.8@sha256:0152dd511befe6a2c2ef53e930726179669b08116da78500b37c51c96ff5ee77",
		"26.3": "clickhouse/clickhouse-server:26.3@sha256:6ffb7138838d70581e867923518e870948eafef7f3b0a073b578cb5df4efe711",
	},
}

// SearchImages pins the OpenSearch release of each search version (ADR-031).
var SearchImages = map[string]string{
	api.SearchVersion2: "opensearchproject/opensearch:2.19.4@sha256:9e0b3b3b6805811bd63d9b9503ffe34a58ba33d03cc346000e318c6ff5c05bd9",
	api.SearchVersion3: "opensearchproject/opensearch:3.3.2@sha256:798cf28e226a32f5c928dd1ed9478dd3a33d2212176aad3679020088ad3afa1a",
}

// System and service images (ADR-029, ADR-032 – ADR-034).
const (
	DefaultBrokerImage    = "nats:2.15.1-alpine@sha256:2ae57dfba3063d5e25fecb75256118c0882a1a2d2cf7d0fba17a7c701df956f6"
	DefaultMailImage      = "axllent/mailpit:v1.28@sha256:c6cf0b06eb516a0b80a8a5251f945c131e6beb8396112964404f62856487f5bd"
	DefaultCDNImage       = "nginx:1.28-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236"
	DefaultTranscodeImage = "linuxserver/ffmpeg:8.0.1@sha256:c72eaf699cb104e84c5292ee9b2a65bf3110738dec113d7cfcef02d7bc21b056"
)

// slowStart is the budget for engines whose first start initialises a lot (ADR-030, ADR-031).
const slowStart = 3 * time.Minute

// ClickHouse's backups go here (ADR-030); the configuration allows BACKUP … TO File() under it.
const (
	clickhouseConfigDir = "/etc/clickhouse-server/config.d"
	clickhouseBackupDir = "/var/lib/clickhouse/es-backups/"
)

const clickhouseConfig = `<clickhouse>
  <backups>
    <allowed_path>` + clickhouseBackupDir + `</allowed_path>
  </backups>
</clickhouse>
`

// mongoAuth signs mongosh in from the container's environment: the password never reaches a command line.
const mongoAuth = "db.getSiblingDB('admin').auth(process.env.MONGO_INITDB_ROOT_USERNAME, process.env.MONGO_INITDB_ROOT_PASSWORD); "

// databasePlan is a database's container for its engine (ADR-030). Passwords reach health checks through
// the container's environment, never the command line (clickhouse-client reads CLICKHOUSE_USER and
// CLICKHOUSE_PASSWORD itself).
func databasePlan(r *domain.Resource, s api.DatabaseSpec, password string, tls *engine.TLSFiles) (plan, error) {
	p, err := engineDatabasePlan(r, s, password)
	if err != nil {
		return p, err
	}
	if s.InitScript != "" { // run once by the image, on an empty data directory (DBX-04)
		ext := ".sql"
		if s.Engine == api.EngineMongoDB {
			ext = ".js"
		}
		if p.files == nil {
			p.files = map[string]map[string][]byte{}
		}
		p.files[engine.InitDir] = map[string][]byte{engine.InitScript + ext: []byte(s.InitScript)}
	}
	if s.TLS {
		if tls == nil {
			return p, engine.Permanent(fmt.Errorf("the database's TLS certificate wasn't issued"))
		}
		files := map[string][]byte{"server.crt": tls.CertPEM, "ca.crt": tls.CAPEM}
		switch s.Engine {
		case api.EnginePostgres:
			// Encrypted connections only over the network; health checks use the local socket (ADR-058).
			files["pg_hba.conf"] = []byte("local all all trust\nhostssl all all all scram-sha-256\n")
			p.cmd = []string{"postgres", "-c", "ssl=on", "-c", "ssl_cert_file=" + engine.TLSDir + "/server.crt", "-c", "ssl_key_file=" + engine.TLSDir + "/server.key",
				"-c", "ssl_ca_file=" + engine.TLSDir + "/ca.crt", "-c", "hba_file=" + engine.TLSDir + "/pg_hba.conf"}
			p.fileOwner = postgresUID
		case api.EngineMySQL:
			p.cmd = []string{"--ssl-ca=" + engine.TLSDir + "/ca.crt", "--ssl-cert=" + engine.TLSDir + "/server.crt", "--ssl-key=" + engine.TLSDir + "/server.key",
				"--require-secure-transport=ON"}
			p.fileOwner = mysqlUID
		default:
			return p, engine.Permanent(fmt.Errorf("TLS is available for PostgreSQL and MySQL"))
		}
		p.privateFiles = map[string]map[string][]byte{engine.TLSDir: {"server.key": tls.KeyPEM}}
		if p.files == nil {
			p.files = map[string]map[string][]byte{}
		}
		p.files[engine.TLSDir] = files
		p.exposed = s.Exposed
	}
	return p, nil
}

// The users the PostgreSQL (Alpine) and MySQL images run their servers as: a TLS key must be theirs.
const (
	postgresUID = 70
	mysqlUID    = 999
)

func engineDatabasePlan(r *domain.Resource, s api.DatabaseSpec, password string) (plan, error) {
	ident := domain.DatabaseIdentifier(r.Name)
	mem := domain.EngineMemoryMiB[s.Engine]
	image := EngineImages[s.Engine][s.Version]
	switch s.Engine {
	case api.EnginePostgres:
		return plan{image: "postgres:" + s.Version + "-alpine", port: engine.PostgresPort, publish: true, mount: "/var/lib/postgresql/data",
			memoryMiB:  mem,
			env:        []string{"POSTGRES_USER=" + ident, "POSTGRES_PASSWORD=" + password, "POSTGRES_DB=" + ident, "PGDATA=/var/lib/postgresql/data/pgdata"},
			healthTest: []string{"CMD-SHELL", "pg_isready -U " + ident + " -d " + ident}}, nil
	case api.EngineMySQL:
		return plan{image: image, port: engine.MySQLPort, publish: true, mount: "/var/lib/mysql", memoryMiB: mem, healthyTimeout: slowStart,
			env: []string{"MYSQL_DATABASE=" + ident, "MYSQL_USER=" + ident, "MYSQL_PASSWORD=" + password, "MYSQL_RANDOM_ROOT_PASSWORD=yes"},
			// Over TCP: during initialisation the server listens on its socket only, so this waits for the real start.
			healthTest: []string{"CMD-SHELL", `MYSQL_PWD="$MYSQL_PASSWORD" mysqladmin ping -h 127.0.0.1 -u"$MYSQL_USER" --silent`}}, nil
	case api.EngineMongoDB:
		return plan{image: image, port: engine.MongoPort, publish: true, mount: "/data/db", memoryMiB: mem, healthyTimeout: slowStart,
			env:        []string{"MONGO_INITDB_ROOT_USERNAME=" + ident, "MONGO_INITDB_ROOT_PASSWORD=" + password, "MONGO_INITDB_DATABASE=" + ident},
			healthTest: []string{"CMD", "mongosh", "--quiet", "--host", "127.0.0.1", "--eval", mongoAuth + "db.adminCommand('ping').ok"}}, nil
	case api.EngineClickHouse:
		return plan{image: image, port: engine.ClickHousePort, extraPorts: []int{engine.ClickHouseNative}, publish: true, mount: "/var/lib/clickhouse",
			memoryMiB: mem, healthyTimeout: slowStart, nofile: 262144,
			env:        []string{"CLICKHOUSE_DB=" + ident, "CLICKHOUSE_USER=" + ident, "CLICKHOUSE_PASSWORD=" + password, "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1"},
			files:      map[string]map[string][]byte{clickhouseConfigDir: {"enclave.xml": []byte(clickhouseConfig)}},
			healthTest: []string{"CMD", "clickhouse-client", "--host", "127.0.0.1", "--query", "SELECT 1"}}, nil
	}
	return plan{}, engine.Permanent(fmt.Errorf("the engine %q isn't supported", s.Engine))
}

// searchPlan is an OpenSearch node with its security plugin on and HTTP without TLS (ADR-031).
func searchPlan(s api.SearchSpec, password string) plan {
	heap := strconv.Itoa(s.MemoryMiB/2) + "m"
	return plan{image: SearchImages[s.Version], port: engine.SearchPort, publish: true, mount: "/usr/share/opensearch/data", memoryMiB: s.MemoryMiB,
		healthyTimeout: slowStart, nofile: 65536,
		env: []string{"discovery.type=single-node", "bootstrap.memory_lock=false", "node.store.allow_mmap=false", "plugins.security.ssl.http.enabled=false",
			"OPENSEARCH_JAVA_OPTS=-Xms" + heap + " -Xmx" + heap, "OPENSEARCH_INITIAL_ADMIN_PASSWORD=" + password, "DISABLE_PERFORMANCE_ANALYZER_AGENT_CLI=true"},
		healthTest: []string{"CMD-SHELL", `printf 'user = "admin:%s"\n' "$OPENSEARCH_INITIAL_ADMIN_PASSWORD" | curl -fs -K - -o /dev/null http://127.0.0.1:9200/_cluster/health`}}
}

// emailPlan is a Mailpit with SMTP and its viewer behind the service's credentials, relaying when asked
// (ADR-032). Managed passwords are URL-safe base64, so they never hold the ":" or " " the format splits on.
func emailPlan(r *domain.Resource, s api.EmailSpec, password string, relay *engine.Relay) plan {
	login := engine.EmailUser(r) + ":" + password
	env := []string{"MP_SMTP_AUTH=" + login, "MP_SMTP_AUTH_ALLOW_INSECURE=true", "MP_UI_AUTH=" + login,
		"MP_DATABASE=/data/mailpit.db", "MP_MAX_MESSAGES=" + strconv.Itoa(s.KeepMessages)}
	if relay != nil {
		env = append(env, "MP_SMTP_RELAY_ALL=true", "MP_SMTP_RELAY_HOST="+relay.Host, "MP_SMTP_RELAY_PORT="+strconv.Itoa(relay.Port),
			"MP_SMTP_RELAY_STARTTLS="+strconv.FormatBool(relay.TLS == api.RelaySTARTTLS), "MP_SMTP_RELAY_TLS="+strconv.FormatBool(relay.TLS == api.RelayTLS),
			"MP_SMTP_RELAY_AUTH="+relayAuth(relay.TLS), "MP_SMTP_RELAY_USERNAME="+relay.Username, "MP_SMTP_RELAY_PASSWORD="+relay.Password)
	}
	return plan{image: DefaultMailImage, port: engine.SMTPPort, extraPorts: []int{engine.MailWebPort}, publish: true, mount: "/data",
		memoryMiB: domain.EmailMemoryMiB, env: env, healthTest: []string{"CMD", "/mailpit", "readyz"}}
}

const cdnConfigDir = "/etc/nginx/conf.d"

// cdnPlan is an nginx cache in front of the origin (ADR-033). Its port isn't published: the edge reaches
// it by name.
func cdnPlan(c *engine.CDNConfig) (plan, error) {
	if c == nil {
		return plan{}, engine.Permanent(fmt.Errorf("the CDN's origin isn't resolved"))
	}
	return plan{image: DefaultCDNImage, port: engine.CDNPort, mount: "/cache", memoryMiB: domain.CDNMemoryMiB,
		files:      map[string]map[string][]byte{cdnConfigDir: {"default.conf": RenderCDN(*c)}},
		healthTest: []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/__enclave/health"}}, nil
}

// RenderCDN writes the cache's nginx configuration. The origin is resolved through Docker's DNS on each
// request, so an app's recreated containers are found without a reload.
func RenderCDN(c engine.CDNConfig) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by EnclaveStack (ADR-033); changes are overwritten.\n")
	fmt.Fprintf(&b, "proxy_cache_path /cache levels=1:2 keys_zone=hc:16m max_size=%dm inactive=7d use_temp_path=off;\n", c.MaxSizeMiB)
	b.WriteString("server {\n  listen 8080;\n  server_tokens off;\n  resolver 127.0.0.11 valid=10s ipv6=off;\n")
	b.WriteString("  location = /__enclave/health {\n    access_log off;\n    return 204;\n  }\n")
	b.WriteString("  location / {\n")
	fmt.Fprintf(&b, "    set $origin http://%s;\n", c.Upstream)
	fmt.Fprintf(&b, "    proxy_pass $origin%s$request_uri;\n", c.PathPrefix)
	if c.HostHeader != "" {
		fmt.Fprintf(&b, "    proxy_set_header Host %s;\n", c.HostHeader)
	} else {
		b.WriteString("    proxy_set_header Host $host;\n")
	}
	b.WriteString(`    proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;
    proxy_set_header X-Forwarded-For $http_x_forwarded_for;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_cache hc;
    proxy_cache_key $request_uri;
    proxy_cache_methods GET HEAD;
    proxy_cache_lock on;
    proxy_cache_background_update on;
    proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;
    proxy_cache_bypass $http_authorization;
    proxy_no_cache $http_authorization;
`)
	if c.TTLSeconds > 0 { // applies only when the origin says nothing about caching
		fmt.Fprintf(&b, "    proxy_cache_valid 200 203 204 206 301 404 410 %ds;\n", c.TTLSeconds)
	}
	b.WriteString("    add_header X-Cache $upstream_cache_status always;\n  }\n}\n")
	return []byte(b.String())
}

// relayAuth is how Mailpit signs in to the relay: PLAIN over TLS, LOGIN without it, because Mailpit's
// SMTP client refuses PLAIN on an unencrypted connection (found on real Docker, 2026-10-21).
func relayAuth(tls string) string {
	if tls == api.RelayNone {
		return "login"
	}
	return "plain"
}

// exposedAddresses are "ip:port" for each non-loopback IPv4 address of this machine (DBX-05).
func exposedAddresses(port int) []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
			out = append(out, net.JoinHostPort(n.IP.To4().String(), strconv.Itoa(port)))
		}
	}
	return out
}
