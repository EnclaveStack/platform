package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Logical dumps of MySQL, MongoDB and ClickHouse (ADR-030). Every command reads its password from the
// exec environment (ES_PW), never from its arguments.

// mongoTool runs a MongoDB tool with its password in a config file only the tool reads, removed after.
func mongoTool(tool string) string {
	return `umask 077; f=$(mktemp); printf 'password: "%s"\n' "$ES_PW" > "$f"; ` + tool + ` --config="$f" --username="$ES_USER" --authenticationDatabase=admin ` +
		`"$@"; rc=$?; rm -f "$f"; exit $rc`
}

// engineDump is the command and environment that write a database's dump to standard output.
func engineDump(s api.DatabaseSpec, ident, password, run string) ([]string, []string) {
	env := []string{"ES_USER=" + ident, "ES_PW=" + password, "ES_DB=" + ident}
	switch s.Engine {
	case api.EngineMySQL:
		return []string{"sh", "-c", `MYSQL_PWD="$ES_PW" exec mysqldump -h 127.0.0.1 -u"$ES_USER" --single-transaction --routines --triggers --events --no-tablespaces --set-gtid-purged=OFF "$ES_DB"`}, env
	case api.EngineMongoDB:
		return []string{"sh", "-c", mongoTool("mongodump"), "mongodump", "--host=127.0.0.1", "--archive", "--gzip", "--db=" + ident}, env
	case api.EngineClickHouse:
		file := clickhouseBackupDir + run + ".zip"
		return []string{"sh", "-c", `set -e; f="` + file + `"; rm -f "$f"; ` +
			`clickhouse-client --host 127.0.0.1 --query "BACKUP DATABASE $ES_DB TO File('$f')" >/dev/null; cat "$f"; rm -f "$f"`}, env
	}
	return nil, nil
}

// engineRestore is the command that replaces a database's data with a dump read from standard input.
func engineRestore(s api.DatabaseSpec, ident, password, run string) ([]string, []string) {
	env := []string{"ES_USER=" + ident, "ES_PW=" + password, "ES_DB=" + ident}
	switch s.Engine {
	case api.EngineMySQL:
		return []string{"sh", "-c", `set -e; export MYSQL_PWD="$ES_PW"; ` +
			`mysql -h 127.0.0.1 -u"$ES_USER" -e "DROP DATABASE IF EXISTS ` + "\\`$ES_DB\\`" + `; CREATE DATABASE ` + "\\`$ES_DB\\`" + `"; exec mysql -h 127.0.0.1 -u"$ES_USER" "$ES_DB"`}, env
	case api.EngineMongoDB:
		return []string{"sh", "-c", mongoTool("mongorestore"), "mongorestore", "--host=127.0.0.1", "--archive", "--gzip", "--drop", "--nsInclude=" + ident + ".*"}, env
	case api.EngineClickHouse:
		file := clickhouseBackupDir + run + ".zip"
		return []string{"sh", "-c", `set -e; f="` + file + `"; mkdir -p "` + clickhouseBackupDir + `"; cat > "$f"; ` +
			`clickhouse-client --host 127.0.0.1 --query "DROP DATABASE IF EXISTS $ES_DB SYNC"; ` +
			`clickhouse-client --host 127.0.0.1 --query "RESTORE DATABASE $ES_DB FROM File('$f')" >/dev/null; rm -f "$f"`}, env
	}
	return nil, nil
}

// dumpEngine streams a MySQL, MongoDB or ClickHouse dump into w.
func (d *Driver) dumpEngine(ctx context.Context, in engine.EnsureInput, s api.DatabaseSpec, w io.Writer) (engine.DumpInfo, error) {
	r := in.Resource
	cmd, env := engineDump(s, domain.DatabaseIdentifier(r.Name), in.Password, "dump-"+r.ID)
	code, stderr, err := d.execStream(ctx, engine.ContainerName(r), cmd, env, nil, w)
	if err != nil {
		return engine.DumpInfo{}, err
	}
	if code != 0 {
		return engine.DumpInfo{}, fmt.Errorf("the %s dump exited with code %d: %s", s.Engine, code, stderr)
	}
	return engine.DumpInfo{Ext: engine.DumpExt(s), Engine: s.Engine + " " + s.Version}, nil
}

// restoreEngine starts the database as needed and replaces its data with src.
func (d *Driver) restoreEngine(ctx context.Context, in engine.EnsureInput, s api.DatabaseSpec, src io.Reader) error {
	if _, err := d.Ensure(ctx, in, noReport{}); err != nil {
		return err
	}
	r := in.Resource
	cmd, env := engineRestore(s, domain.DatabaseIdentifier(r.Name), in.Password, "restore-"+r.ID)
	code, stderr, err := d.execStream(ctx, engine.ContainerName(r), cmd, env, src, io.Discard)
	if err != nil {
		return err
	}
	if code != 0 {
		return engine.Permanent(fmt.Errorf("the %s restore exited with code %d: %s", s.Engine, code, stderr))
	}
	return nil
}
