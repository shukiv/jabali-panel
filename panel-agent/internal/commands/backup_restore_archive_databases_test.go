package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: an upload restore names the databases whose data, after it, is
// all the archive's: new or empty before the restore, and loaded without an
// error. The panel lets the archive grant access only to those. A database
// that already had tables still holds them after an overwrite, and one whose
// load failed holds whatever was there.

// archiveLoads swaps the dump load: it fails for the databases in fail.
func archiveLoads(t *testing.T, fail ...string) {
	t.Helper()
	prev := loadRestoredMariaDBDump
	loadRestoredMariaDBDump = func(_ context.Context, db string, _ io.Reader) error {
		if containsString(fail, db) {
			return errors.New("ERROR 1064 at line 1")
		}
		return nil
	}
	t.Cleanup(func() { loadRestoredMariaDBDump = prev })
}

func TestUploadRestore_NamesTheDatabasesWhoseDataIsAllTheArchives(t *testing.T) {
	me := currentUsername(t)
	full, empty, fresh, broken := me+"_full", me+"_empty", me+"_fresh", me+"_broken"
	run := func(keep bool) *restoreClaims {
		keepExecRecorder(t, []string{full}, []string{fresh, broken}, nil)
		archiveLoads(t, broken)
		root := t.TempDir()
		var stages []backup.ManifestStage
		for _, n := range []string{full, empty, fresh, broken} {
			stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{n}})
		}
		results := stageUpload(t, root, stages)
		claims := &restoreClaims{}
		enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{full, empty},
			ForeignDBNames: []string{}, Claims: claims, KeepExisting: keep}
		applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)
		return claims
	}

	for _, keep := range []bool{true, false} {
		claims := run(keep)
		if got := strings.Join(claims.ArchiveMariaDBs, ","); got != empty+","+fresh {
			t.Errorf("keep=%v: archive databases %q, want only the empty and the new one (loaded: %v)", keep, got, claims.Databases)
		}
	}
}

// objectsAnswer makes the "does it hold anything" check run the shell script
// answer. The table check finds no tables, so keep-existing loads too.
func objectsAnswer(t *testing.T, answer string) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "mariadb" && strings.Contains(strings.Join(args, " "), "information_schema.ROUTINES") {
			return exec.CommandContext(ctx, "sh", "-c", answer)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
}

// restoreOneDB restores db from an upload and returns what it claimed.
func restoreOneDB(t *testing.T, me, db string, keep bool) *restoreClaims {
	t.Helper()
	archiveLoads(t)
	root := t.TempDir()
	stages := []backup.ManifestStage{{Name: backup.StageDB, Items: []string{db}}}
	results := stageUpload(t, root, stages)
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{db},
		ForeignDBNames: []string{}, Claims: claims, KeepExisting: keep}
	applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)
	return claims
}

// A database that can't be checked counts as one that holds something.
func TestUploadRestore_ADatabaseItCantCheckIsNotTheArchives(t *testing.T) {
	me := currentUsername(t)
	db := me + "_db"
	for _, keep := range []bool{true, false} {
		objectsAnswer(t, "echo 'ERROR 2002 (HY000): Can'\\''t connect' >&2; exit 1")
		claims := restoreOneDB(t, me, db, keep)
		if len(claims.Databases) != 1 || len(claims.ArchiveMariaDBs) != 0 {
			t.Errorf("keep=%v: loaded %v, archive databases %v; want it loaded and none", keep, claims.Databases, claims.ArchiveMariaDBs)
		}
	}
}

// A stored routine or an event outlives the load and can run with its
// definer's rights: a database with only those isn't empty.
func TestUploadRestore_ADatabaseWithOnlyARoutineIsNotTheArchives(t *testing.T) {
	me := currentUsername(t)
	db := me + "_db"
	for _, keep := range []bool{true, false} {
		objectsAnswer(t, "echo 1")
		claims := restoreOneDB(t, me, db, keep)
		if len(claims.Databases) != 1 || len(claims.ArchiveMariaDBs) != 0 {
			t.Errorf("keep=%v: loaded %v, archive databases %v; want it loaded and none", keep, claims.Databases, claims.ArchiveMariaDBs)
		}
	}
}

// A PostgreSQL restore claims no MariaDB database, even a new one restored
// cleanly: the account's MariaDB database of the same name can hold its data.
func TestUploadRestore_APostgresRestoreClaimsNoMariaDBDatabase(t *testing.T) {
	me := currentUsername(t)
	withData, fresh := me+"_pgfull", me+"_pgnew"
	keepExecRecorder(t, nil, nil, []string{withData})
	pgLoads(t)
	root := t.TempDir()
	var stages []backup.ManifestStage
	for _, db := range []string{withData, fresh} {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{db}})
		mustWrite(t, filepath.Join(root, "db", db+".pgdump"), "PGDMP")
	}
	results := []backupRestoreStage{{Name: backup.StageDB, Status: backup.StageStatusOK}, {Name: backup.StageDB, Status: backup.StageStatusOK}}
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{withData},
		ForeignDBNames: []string{}, Claims: claims}

	applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if len(claims.Databases) != 2 || len(claims.ArchiveMariaDBs) != 0 {
		t.Fatalf("loaded %v, archive MariaDB databases %v; want both loaded and none", claims.Databases, claims.ArchiveMariaDBs)
	}
}

// The reply always names the archive's databases in upload mode, an empty
// list when there are none: the panel reads a missing list as an agent too
// old to say.
func TestRestoreFromTarResult_CarriesTheArchiveMariaDBs(t *testing.T) {
	for _, c := range []struct {
		claims *restoreClaims
		want   string
	}{
		{&restoreClaims{Databases: []string{"a_x"}, ArchiveMariaDBs: []string{"a_x"}}, `"archive_mariadb_databases":["a_x"]`},
		{&restoreClaims{Databases: []string{"a_x"}}, `"archive_mariadb_databases":[]`},
	} {
		var out backupRestoreFromTarResult
		out.setClaims(c.claims)
		raw, err := json.Marshal(out)
		if err != nil || !strings.Contains(string(raw), c.want) {
			t.Errorf("reply %s (%v), want %s", raw, err, c.want)
		}
	}
}
