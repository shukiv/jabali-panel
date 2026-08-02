package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// JAB-210. A box installed from a build of 372bdd25 (DB schema at version 242)
// ran `jabali update` and ended up with an OLDER binary on disk than the schema
// it has to serve:
//
//	git fetch/reset failed  → swallowed; the updater reported the pre-existing
//	                          stale HEAD as though the reset had succeeded
//	release tarball 7085d50 → installed OVER the newer on-disk build
//	migrate up              → no migration found for version 242: read down for
//	                          version 242 .: file does not exist
//
// The update aborted only AFTER the binary swap, leaving a disk binary that
// predates the live schema. Any service restart from that state runs old code
// against a newer database — the failure mode is unbounded, because old code
// does not know which columns exist.
//
// The check below runs BEFORE the swap. Verifying afterwards would be no help:
// by then the box is already in the state we are trying to prevent.

// migrationFileRe matches a golang-migrate filename: 000242_name.up.sql.
var migrationFileRe = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// maxMigrationVersionInDir returns the highest migration version present in a
// checked-out repo tree — that is, the newest schema the code we are about to
// install knows how to serve.
//
// Reads the working tree rather than asking the candidate binary, because the
// question has to be answered before that binary is on disk.
func maxMigrationVersionInDir(repoDir string) (uint, error) {
	dir := filepath.Join(repoDir, "panel-api", "internal", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read migrations dir: %w", err)
	}
	var max uint
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, convErr := strconv.ParseUint(m[1], 10, 64)
		if convErr != nil {
			continue
		}
		if uint(n) > max {
			max = uint(n)
		}
	}
	if max == 0 {
		// An empty or unreadable migrations dir means we cannot answer the
		// question. Refuse rather than assume the candidate is fine — assuming
		// is what produced the incident.
		return 0, fmt.Errorf("no migrations found under %s", dir)
	}
	return max, nil
}

// guardSchemaDowngrade refuses an install whose migrations stop short of the
// schema already live in the database.
//
// Equal is fine (the normal no-op update) and higher is fine (that is what
// `migrate up` is for). Only strictly-lower is refused, and the message has to
// name both numbers plus a way out: an operator hitting this at 3am needs to
// know the box is still healthy and that --from-source rebuilds current code.
func guardSchemaDowngrade(candidateMax, liveSchema uint) error {
	if liveSchema == 0 || candidateMax >= liveSchema {
		return nil
	}
	return fmt.Errorf(
		"refusing to install: the code being installed only knows migrations up to %d, "+
			"but this server's database is already at schema version %d. Installing it "+
			"would leave a binary older than the schema it has to serve, and any restart "+
			"from that state runs old code against a newer database. Nothing has been "+
			"changed. This usually means the release tarball is behind your checkout — "+
			"re-run with --from-source to build the current code instead",
		candidateMax, liveSchema)
}
