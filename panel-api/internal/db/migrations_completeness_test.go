package db

import (
	"io/fs"
	"regexp"
	"sort"
	"testing"
)

// migrationFilePattern matches an embedded migration filename, e.g.
// "000205_crowdsec_login_allowlist.up.sql" → version 205, dir "up".
var migrationFilePattern = regexp.MustCompile(`^(\d+)_.+\.(up|down)\.sql$`)

// TestEmbeddedMigrationsComplete guards the class of failure behind GH #352
// ("no migration found for version N: read down for version N: file does not
// exist"). golang-migrate reads BOTH the up and down file for every version
// it discovers, so a version shipped with only one half — or a gap in the
// numbering — makes `migrate up` hard-fail on boot, which surfaces to the
// user as an unreachable panel / identity service.
//
// This runs against the go:embed'd FS (the exact bytes baked into the
// binary), not the on-disk tree, so it verifies what actually deploys.
func TestEmbeddedMigrationsComplete(t *testing.T) {
	root, err := migrationsFSRoot()
	if err != nil {
		t.Fatalf("migrationsFSRoot: %v", err)
	}

	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatalf("read embedded migrations dir: %v", err)
	}

	type pair struct{ up, down bool }
	versions := map[int]*pair{}
	names := map[int]string{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("embedded migration file %q does not match the NNNNNN_name.(up|down).sql convention", e.Name())
			continue
		}
		// Version is a zero-padded integer; Atoi via the regexp group.
		var v int
		for _, r := range m[1] {
			v = v*10 + int(r-'0')
		}
		p := versions[v]
		if p == nil {
			p = &pair{}
			versions[v] = p
			names[v] = m[1]
		}
		switch m[2] {
		case "up":
			p.up = true
		case "down":
			p.down = true
		}
	}

	if len(versions) == 0 {
		t.Fatal("no embedded migrations found — check the go:embed directive in db.go")
	}

	// Every discovered version must have BOTH halves.
	sorted := make([]int, 0, len(versions))
	for v, p := range versions {
		sorted = append(sorted, v)
		if !p.up {
			t.Errorf("migration version %s is missing its .up.sql", names[v])
		}
		if !p.down {
			t.Errorf("migration version %s is missing its .down.sql (this is exactly the GH #352 failure)", names[v])
		}
	}
	sort.Ints(sorted)

	// Versions must be contiguous — a gap means golang-migrate can't walk
	// the chain and `migrate up` fails partway.
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1]+1 {
			t.Errorf("migration numbering gap: %d follows %d (expected %d) — versions must be contiguous", sorted[i], sorted[i-1], sorted[i-1]+1)
		}
	}
}
