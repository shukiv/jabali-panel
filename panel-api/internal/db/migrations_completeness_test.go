package db

import (
	"io/fs"
	"regexp"
	"sort"
	"testing"
)

// migrationFilePattern matches an embedded migration filename, e.g.
// "000205_crowdsec_login_allowlist.up.sql" → version 205, name
// "crowdsec_login_allowlist", dir "up".
var migrationFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

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
	// stems tracks the distinct "NNNNNN_name" prefixes seen per version. Two
	// different names sharing one version number is the GH #526/#615 scar: the
	// binary embeds both, golang-migrate's iofs source rejects it at init with
	// "duplicate migration file: <name>.down.sql", and EVERY `jabali update`
	// then fails `migrate up` — the panel is stranded on old code. The map-by-
	// version accounting above can't see it (both files just set up/down), so
	// track stems explicitly.
	stems := map[int]map[string]bool{}

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
		if stems[v] == nil {
			stems[v] = map[string]bool{}
		}
		stems[v][m[1]+"_"+m[2]] = true
		switch m[3] {
		case "up":
			p.up = true
		case "down":
			p.down = true
		}
	}

	// No two migration names may share a version number.
	for v, s := range stems {
		if len(s) > 1 {
			dupes := make([]string, 0, len(s))
			for n := range s {
				dupes = append(dupes, n)
			}
			sort.Strings(dupes)
			t.Errorf("duplicate migration version %d shared by %v — golang-migrate rejects this at init (\"duplicate migration file\") and `migrate up` fails on every host; renumber one to the next free version", v, dupes)
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
