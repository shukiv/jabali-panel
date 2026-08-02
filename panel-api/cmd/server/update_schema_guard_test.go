package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMigrations(t *testing.T, repoDir string, names ...string) {
	t.Helper()
	dir := filepath.Join(repoDir, "panel-api", "internal", "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMaxMigrationVersionInDir(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeMigrations(t, repo,
		"000240_a.up.sql", "000240_a.down.sql",
		"000242_b.up.sql", "000242_b.down.sql",
		"000241_c.up.sql",
		"README.md", // not a migration
	)

	got, err := maxMigrationVersionInDir(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 242 {
		t.Errorf("max = %d, want 242 (highest .up.sql, down files and stray "+
			"files ignored)", got)
	}
}

func TestMaxMigrationVersionInDir_EmptyRefusesRatherThanReturningZero(t *testing.T) {
	t.Parallel()

	// Returning 0 would silently compare as "older than everything" or, worse,
	// be treated as unknown-and-fine. Assuming is what caused JAB-210, so an
	// unanswerable question has to be an error.
	repo := t.TempDir()
	writeMigrations(t, repo, "notes.txt")

	if _, err := maxMigrationVersionInDir(repo); err == nil {
		t.Error("expected an error when no migrations are present")
	}
}

func TestMaxMigrationVersionInDir_MissingDirIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := maxMigrationVersionInDir(t.TempDir()); err == nil {
		t.Error("a missing migrations dir must be an error, not version 0")
	}
}

func TestGuardSchemaDowngrade(t *testing.T) {
	t.Parallel()

	t.Run("blocks the reported incident", func(t *testing.T) {
		// The real numbers: release tarball 7085d502 knew migrations to 240,
		// the box's database was already at 242.
		err := guardSchemaDowngrade(240, 242)
		if err == nil {
			t.Fatal("a binary older than the live schema MUST be refused — " +
				"this is the state that leaves a box unsafe to restart")
		}
		msg := err.Error()
		for _, want := range []string{"240", "242", "Nothing has been", "--from-source"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message must contain %q so the operator knows the "+
					"numbers, that the box is untouched, and the way out:\n%s", want, msg)
			}
		}
	})

	t.Run("equal is the normal case", func(t *testing.T) {
		if err := guardSchemaDowngrade(242, 242); err != nil {
			t.Errorf("a same-schema update is the common no-op: %v", err)
		}
	})

	t.Run("newer code is exactly what migrate up is for", func(t *testing.T) {
		if err := guardSchemaDowngrade(250, 242); err != nil {
			t.Errorf("installing newer code must not be blocked: %v", err)
		}
	})

	t.Run("fresh database is not a downgrade", func(t *testing.T) {
		// liveSchema 0 means nothing has been applied yet (fresh install).
		// Blocking there would make first-time installs impossible.
		if err := guardSchemaDowngrade(1, 0); err != nil {
			t.Errorf("a fresh database must not trip the guard: %v", err)
		}
	})
}
