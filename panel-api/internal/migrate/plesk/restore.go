package plesk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// dbNameRe bounds a MySQL database name to a safe charset before it is used
// as a local filename (<db>.sql) — blocks path traversal / injection from a
// tampered source manifest.
var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func validDBName(db string) bool { return dbNameRe.MatchString(db) }

// dbStreamTimeout bounds a single DB dump stream. A 6.9 GB DB over a slow
// link can take a while; this is generous.
const dbStreamTimeout = 4 * time.Hour

// restore.go — the source-side building blocks that turn the Plesk
// metadata cpmove tarball (databases.txt / domains-paths.txt /
// mail-paths.txt manifests) into a full cpmove tree the existing cpanel
// import writers consume. These are PURE builders/parsers — no execution,
// no destination mutation. The import step (3b-iii, dest-mutating) wires
// them into the pull/restore flow: it streams each DB dump and rsyncs each
// docroot/Maildir using these commands, then dispatches migration.import_run.
//
// Why stream instead of bundle: a real Plesk box carried a 6.9 GB
// WordPress DB — bundling a mysqldump into the /tmp tarball would batter
// the source. The DB is dumped straight to the destination-side staging
// dir via `ssh source '<dbDumpCommand>' > mysql/<db>.sql`.

// ManifestEntry is one row of a TSV manifest (domains-paths.txt /
// mail-paths.txt): a logical name + an absolute source path.
type ManifestEntry struct {
	Name string // domain (domains-paths) or address (mail-paths)
	Path string // absolute source path (docroot / Maildir)
}

// parseTSVManifest parses `name\tpath` lines (domains-paths.txt,
// mail-paths.txt). Blank/comment lines and rows missing a tab are skipped.
func parseTSVManifest(content string) []ManifestEntry {
	out := []ManifestEntry{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		name, path, ok := strings.Cut(line, "\t")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if !ok || name == "" || path == "" {
			continue
		}
		out = append(out, ManifestEntry{Name: name, Path: path})
	}
	return out
}

// parseLinesManifest parses a one-value-per-line manifest (databases.txt).
func parseLinesManifest(content string) []string {
	return splitLines(content)
}

// DBManifestEntry is one line of databases.txt: a database and the engine it
// actually lives in on the source.
type DBManifestEntry struct {
	Name   string
	Engine string
}

// parseDatabasesManifest parses databases.txt as "<name>\t<engine>", falling
// back to mysql for a bare name.
//
// GH #429: the manifest used to be names only, because every Plesk database
// was assumed to be MySQL. A bare line can only have come from the older
// backup script, which ran before any engine was recorded and only ever
// produced MySQL databases — so mysql is the right and safe default for it.
func parseDatabasesManifest(content string) []DBManifestEntry {
	var out []DBManifestEntry
	for _, line := range splitLines(content) {
		name, engine, found := strings.Cut(line, "\t")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		engine = strings.ToLower(strings.TrimSpace(engine))
		if !found || engine == "" {
			engine = "mysql"
		}
		out = append(out, DBManifestEntry{Name: name, Engine: engine})
	}
	return out
}

// dbDumpCommand builds the READ-ONLY mysqldump command to run on the Plesk
// source (over SSH) to stream one database to stdout. Plesk stashes the
// admin DB password at /etc/psa/.psa.shadow; --single-transaction takes a
// consistent snapshot without locking (InnoDB). The db name (from the
// source manifest) is shell-escaped. Caller redirects stdout into the
// destination-side mysql/<db>.sql — the dump is never staged on the source.
func dbDumpCommand(db string) string {
	return fmt.Sprintf(
		`MYSQL_PWD="$(cat /etc/psa/.psa.shadow)" mysqldump -uadmin --skip-lock-tables --single-transaction %s`,
		shellQuote(db))
}

// docrootRemotePath returns the absolute source docroot for a
// domains-paths.txt entry (identity — the manifest already holds the
// absolute path). Exposed as a named helper so the rsync wiring reads
// intently and a future path-rewrite has one home.
func docrootRemotePath(e ManifestEntry) string { return e.Path }

// PopulatePleskDBs streams each database named in the extracted tree's
// databases.txt into cpmove-<slug>/mysql/<db>.sql so the existing cpanel
// ImportDatabases writer finds them. The dump is streamed straight to disk
// (never buffered — see streamCommandToFile) and never staged on the
// source. `stream` is injected so callers pass the live session's streamer
// (and tests pass a fake). Returns the number of DBs dumped. A db name that
// fails the charset guard is skipped (never used as an unchecked filename).
func PopulatePleskDBs(extractDir, slug string, stream func(cmd, dstPath string) error) (int, []string, error) {
	base := filepath.Join(extractDir, "cpmove-"+slug)
	mysqlDir := filepath.Join(base, "mysql")
	if err := os.MkdirAll(mysqlDir, 0o750); err != nil {
		return 0, nil, fmt.Errorf("mkdir mysql dir: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(base, "databases.txt"))
	if err != nil {
		return 0, nil, nil // no manifest → no DBs (not fatal)
	}
	n := 0
	var skipped []string
	for _, entry := range parseDatabasesManifest(string(raw)) {
		if !validDBName(entry.Name) {
			continue // refuse a suspicious name as a filename
		}
		// GH #429: only MySQL/MariaDB can be dumped with mysqldump and read
		// back by the cpanel importer. Pointing mysqldump at a PostgreSQL
		// database is what produced the reported bare `exit status 2`.
		if !isMySQLEngine(entry.Engine) {
			skipped = append(skipped, fmt.Sprintf(
				"database %q (engine %s) was NOT migrated — jabali's migration importer "+
					"can only restore MySQL/MariaDB. Move it across by hand (pg_dump on the "+
					"source, then restore into a PostgreSQL database on this server) before "+
					"decommissioning the source.",
				entry.Name, entry.Engine))
			continue
		}
		dst := filepath.Join(mysqlDir, entry.Name+".sql")
		if err := stream(dbDumpCommand(entry.Name), dst); err != nil {
			return n, skipped, fmt.Errorf("stream db %s: %w", entry.Name, err)
		}
		n++
	}
	return n, skipped, nil
}

// isMySQLEngine reports whether psa's data_bases.type names a MySQL-family
// engine. Plesk writes "mysql"; the family is spelled several ways across
// versions and adapters, so match generously and treat anything unrecognised
// as NOT MySQL — guessing wrong in that direction produces the misleading
// mysqldump failure this fix exists to remove.
func isMySQLEngine(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "mysql", "mariadb", "mysqli":
		return true
	}
	return false
}

// StreamDBDump is the production streamer bound to a live pull session; the
// pull wiring passes a closure over this. Kept here so the SSH primitive and
// the orchestration live together.
func (d *Discoverer) StreamDBDump(ctx context.Context, raw interface{}, cmd, dstPath string) error {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return fmt.Errorf("StreamDBDump: bad session")
	}
	return s.streamCommandToFile(ctx, dbStreamTimeout, cmd, dstPath)
}
