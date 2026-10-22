package engine

import (
	"strconv"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// Facts shared by every driver about the v0.6 services (ADR-030 – ADR-032), so the Docker and fake
// drivers report the same outputs.

// DatabasePort is an engine's main port (ClickHouse: its HTTP interface).
func DatabasePort(eng string) int {
	switch eng {
	case api.EngineMySQL:
		return MySQLPort
	case api.EngineMongoDB:
		return MongoPort
	case api.EngineClickHouse:
		return ClickHousePort
	}
	return PostgresPort
}

// DumpExt is the file extension of a logical backup of spec (ADR-018, ADR-030).
func DumpExt(spec any) string {
	switch s := spec.(type) {
	case api.CacheSpec:
		return "rdb"
	case api.DatabaseSpec:
		switch s.Engine {
		case api.EngineMySQL:
			return "sql"
		case api.EngineMongoDB:
			return "archive.gz"
		case api.EngineClickHouse:
			return "zip"
		}
	}
	return "pgdump"
}

// DatabaseOutputs are a database's outputs (DBE-03). host is where published ports bind; hostPort and
// nativeHostPort are 0 when nothing is published (a network without internet).
func DatabaseOutputs(r *domain.Resource, s api.DatabaseSpec, host string, hostPort, nativeHostPort int) Outputs {
	ident := domain.DatabaseIdentifier(r.Name)
	out := Outputs{"engine": s.Engine, "internalHost": ContainerName(r), "internalPort": DatabasePort(s.Engine),
		"database": ident, "username": ident, "passwordSecret": domain.ManagedPasswordSecret(r.Name)}
	if hostPort > 0 {
		out["host"], out["port"] = host, hostPort
	}
	if s.Engine == api.EngineClickHouse {
		out["internalNativePort"] = ClickHouseNative
		if nativeHostPort > 0 {
			out[OutputNativePort] = nativeHostPort
		}
	}
	return out
}

// SearchOutputs are a search service's outputs (SRC-03); version is the exact OpenSearch version.
func SearchOutputs(r *domain.Resource, host string, hostPort int, version string) Outputs {
	internal := ContainerName(r)
	out := Outputs{"internalHost": internal, "internalPort": SearchPort, "internalUrl": "http://" + internal + ":9200",
		"username": "admin", "passwordSecret": domain.PasswordSecretFor(api.KindSearch, r.Name), "version": version}
	if hostPort > 0 {
		out["host"], out["port"], out["url"] = host, hostPort, "http://"+host+":"+strconv.Itoa(hostPort)
	}
	return out
}

// EmailOutputs are an email service's outputs (EML-01).
func EmailOutputs(r *domain.Resource, s api.EmailSpec, host string, smtpHostPort, webHostPort int) Outputs {
	out := Outputs{"internalHost": ContainerName(r), "smtpPort": SMTPPort, "username": EmailUser(r),
		"passwordSecret": domain.PasswordSecretFor(api.KindEmail, r.Name), "mode": s.Mode}
	if smtpHostPort > 0 {
		out["host"], out["port"] = host, smtpHostPort
	}
	if webHostPort > 0 {
		out["webUrl"] = "http://" + host + ":" + strconv.Itoa(webHostPort)
		out[OutputWebPort] = webHostPort
	}
	return out
}

// OutputWebPort is the email viewer's port on the machine, which the mail client reads.
const OutputWebPort = "webPort"

// EmailUser is the SMTP user of an email service: its name.
func EmailUser(r *domain.Resource) string { return r.Name }

// SearchVersions pins each search major version to the OpenSearch release EnclaveStack runs (ADR-031).
var SearchVersions = map[string]string{api.SearchVersion2: "2.19.4", api.SearchVersion3: "3.3.2"}
