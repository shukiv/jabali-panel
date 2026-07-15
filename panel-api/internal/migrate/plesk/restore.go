package plesk

import (
	"fmt"
	"strings"
)

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
