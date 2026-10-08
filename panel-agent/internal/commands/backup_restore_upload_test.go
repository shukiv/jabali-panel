package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: an admin restore from an uploaded backup (mode=upload) may only
// write into the target account's own databases, mail domains and docker apps,
// or create new ones in its own namespace. Whoever made the file chose every
// name in it.

// uploadExecRecorder stubs every command and records it as one line. A
// command containing one of existing fails the way MariaDB does for a
// CREATE DATABASE of a database that exists.
func uploadExecRecorder(t *testing.T, existing ...string) *[]string {
	t.Helper()
	var cmds []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		cmds = append(cmds, line)
		for _, e := range existing {
			if strings.Contains(line, "CREATE DATABASE `"+e+"`") {
				return exec.CommandContext(ctx, "sh", "-c", "echo \"ERROR 1007 (HY000) at line 1: Can't create database '"+e+"'; database exists\" >&2; exit 1")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &cmds
}

func currentUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	return u.Username
}

// stageUpload lays out a staged restore tree with one stage per item and
// returns all-OK stage results for them.
func stageUpload(t *testing.T, root string, stages []backup.ManifestStage) []backupRestoreStage {
	t.Helper()
	res := make([]backupRestoreStage, len(stages))
	for i, st := range stages {
		res[i] = backupRestoreStage{Name: st.Name, Status: backup.StageStatusOK}
		switch st.Name {
		case backup.StageDB:
			mustWrite(t, filepath.Join(root, "db", st.Items[0]+".sql"), "SELECT 1;")
		case backup.StageDocker:
			mustWrite(t, filepath.Join(root, backup.StageDocker, dockerAppDataRoot, st.Items[0], "compose.yml"), "services: {}")
		}
	}
	return res
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// createStatements maps each database a CREATE DATABASE named to whether it
// used IF NOT EXISTS.
func createStatements(cmds []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cmds {
		for _, marker := range []string{"CREATE DATABASE IF NOT EXISTS `", "CREATE DATABASE `"} {
			if i := strings.Index(c, marker); i >= 0 {
				rest := c[i+len(marker):]
				out[rest[:strings.Index(rest, "`")]] = strings.Contains(marker, "IF NOT EXISTS")
				break
			}
		}
	}
	return out
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestUploadRestore_LoadsOnlyTheAccountsOwnOrNewDatabases(t *testing.T) {
	me := currentUsername(t)
	// <me>_orphan exists in MariaDB with no panel row: neither owned nor foreign.
	cmds := uploadExecRecorder(t, me+"_orphan")
	root := t.TempDir()
	names := []string{"jabali_panel", "mysql", "bob_shop", "carol_x", me + "_new", me + "_owned", "shopdb", me + "_orphan"}
	var stages []backup.ManifestStage
	for _, n := range names {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{n}})
	}
	results := stageUpload(t, root, stages)
	enf := restoreEnforcement{
		Mode:           restoreModeUpload,
		AllowedDBNames: []string{me + "_owned", "shopdb"}, // shopdb: an admin-named db the account owns
		ForeignDBNames: []string{"bob_shop"},
		DBPrefix:       me + "_",
	}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	got := createStatements(*cmds)
	for n, ifNotExists := range map[string]bool{me + "_owned": true, "shopdb": true, me + "_new": false} {
		if v, ok := got[n]; !ok || v != ifNotExists {
			t.Errorf("database %s: created=%v ifNotExists=%v, want created with ifNotExists=%v (warnings %v)", n, ok, v, ifNotExists, warnings)
		}
	}
	for _, n := range []string{"jabali_panel", "mysql", "bob_shop", "carol_x"} {
		if _, ok := got[n]; ok {
			t.Errorf("database %s was loaded from an uploaded backup", n)
		}
	}
	if hasWarning(warnings, "db "+me+"_orphan: mariadb load") || !hasWarning(warnings, `"`+me+`_orphan": not restored: a database with this name already exists`) {
		t.Errorf("warnings %v: the existing database the account doesn't own must be refused, not loaded", warnings)
	}
	for _, want := range []string{`"jabali_panel": not restored: it is one of this server's own databases`,
		`"bob_shop": not restored: it belongs to another account`, `"carol_x": not restored: a database from an uploaded backup must be`} {
		if !hasWarning(warnings, want) {
			t.Errorf("warnings %v should contain %q", warnings, want)
		}
	}
}

func TestUploadRestore_SkipsMailForAnotherAccountsDomain(t *testing.T) {
	uploadExecRecorder(t)
	me := currentUsername(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "mail", "bob.org", "info", "cur", "1"), "x")
	mustWrite(t, filepath.Join(root, "mail", "alice.org", "info", "cur", "1"), "x")
	stages := []backup.ManifestStage{{Name: backup.StageMail}}
	results := []backupRestoreStage{{Name: backup.StageMail, Status: backup.StageStatusOK}}
	enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{}, AllowedMailDomains: []string{"Alice.org"}}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if _, err := os.Stat(filepath.Join(root, "mail", "bob.org")); !os.IsNotExist(err) {
		t.Errorf("bob.org's mail was left in the import tree (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mail", "alice.org")); err != nil {
		t.Errorf("alice.org's mail was dropped: %v", err)
	}
	if !hasWarning(warnings, `domain "bob.org" is not one of this account's domains`) {
		t.Errorf("warnings %v should name bob.org", warnings)
	}
}

func TestUploadRestore_NeverOverwritesAnotherAccountsDockerApp(t *testing.T) {
	cmds := uploadExecRecorder(t)
	me := currentUsername(t)
	root := t.TempDir()
	live := t.TempDir()
	prev := restoreDockerRoot
	restoreDockerRoot = live
	t.Cleanup(func() { restoreDockerRoot = prev })
	for _, existing := range []string{"taken", "mine"} {
		mustWrite(t, filepath.Join(live, existing, "compose.yml"), "services: {}")
	}
	slugs := []string{"taken", "mine", "srv", "fresh", "theirs"}
	var stages []backup.ManifestStage
	for _, s := range slugs {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDocker, Items: []string{s}})
	}
	results := stageUpload(t, root, stages)
	enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{},
		OwnedDockerSlugs: []string{"mine"}, ServerLevelDockerSlugs: []string{"srv"},
		ForeignDockerSlugs: []string{"theirs"}} // theirs: another account's app, its data dir not on disk

	applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	synced := map[string]bool{}
	for _, c := range *cmds {
		if !strings.HasPrefix(c, "rsync ") {
			continue
		}
		for _, s := range slugs {
			if strings.HasSuffix(c, filepath.Join(live, s)+"/") {
				synced[s] = true
			}
		}
	}
	if synced["taken"] || synced["srv"] || synced["theirs"] {
		t.Errorf("synced %v: another account's or a server-level app was overwritten", synced)
	}
	if !synced["mine"] || !synced["fresh"] {
		t.Errorf("synced %v (applied %v, warnings %v): the account's own and a new app should be restored", synced, applied, warnings)
	}
	if !hasWarning(warnings, "docker taken: not restored: an app with this name already exists") ||
		!hasWarning(warnings, "docker srv: not restored: a server-level app") ||
		!hasWarning(warnings, "docker theirs: not restored: an app with this name belongs to another account") {
		t.Errorf("warnings %v should explain both refusals", warnings)
	}
}

func TestUploadRestore_UnreadableMetadataRestoresNoDockerApp(t *testing.T) {
	uploadExecRecorder(t)
	me := currentUsername(t)
	root := t.TempDir()
	prev := restoreDockerRoot
	restoreDockerRoot = t.TempDir()
	t.Cleanup(func() { restoreDockerRoot = prev })
	stages := []backup.ManifestStage{{Name: backup.StageDocker, Items: []string{"fresh"}}}
	results := stageUpload(t, root, stages)
	enf := restoreEnforcement{Mode: restoreModeUpload, AllowedDBNames: []string{}, DockerMetadataMissing: true}

	applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if len(applied) != 0 || !hasWarning(warnings, "docker fresh: not restored: the backup's metadata is unreadable") {
		t.Errorf("applied %v warnings %v: want the app refused", applied, warnings)
	}
}

func TestBackupRestoreFromTar_UploadRequiresEveryList(t *testing.T) {
	call := func(p map[string]any) error {
		raw, _ := json.Marshal(p)
		_, err := backupRestoreFromTarHandler(context.Background(), raw)
		return err
	}
	base := func() map[string]any {
		return map[string]any{"mode": "upload", "job_id": "x", "tar_path": "/x", "target_username": "alice",
			"allowed_db_names": []string{}, "foreign_db_names": []string{}, "allowed_mail_domains": []string{},
			"owned_docker_slugs": []string{}, "foreign_docker_slugs": []string{}}
	}
	for _, missing := range []string{"allowed_db_names", "foreign_db_names", "allowed_mail_domains", "owned_docker_slugs", "foreign_docker_slugs"} {
		p := base()
		delete(p, missing)
		if err := call(p); err == nil || !strings.Contains(err.Error(), "mode=upload requires") {
			t.Errorf("without %s: got %v, want the upload guard", missing, err)
		}
	}
	if err := call(base()); err != nil && strings.Contains(err.Error(), "mode=upload requires") {
		t.Errorf("all lists present (empty): got %v, want past the guard", err)
	}
}

func TestServerLevelDockerSlugs(t *testing.T) {
	meta := `{"user":{"id":"u"},"docker_apps":[{"slug":"gitea","instance_slug":"gitea-2","server_level":true},{"slug":"uptime","server_level":true},{"slug":"n8n","instance_slug":"n8n-1"}]}`
	got, missing := serverLevelDockerSlugs([]byte(meta))
	if missing || strings.Join(got, ",") != "gitea-2,uptime" {
		t.Fatalf("got %v missing=%v, want [gitea-2 uptime]", got, missing)
	}
	if _, missing := serverLevelDockerSlugs(nil); !missing {
		t.Fatal("no metadata must count as missing")
	}
	if _, missing := serverLevelDockerSlugs([]byte("{")); !missing {
		t.Fatal("unparseable metadata must count as missing")
	}
}

func TestAgentVersion_ReportsUploadConfinement(t *testing.T) {
	out, err := agentVersionHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	caps := out.(agentVersionResponse).Capabilities
	if !containsString(caps, capRestoreUploadConfinement) {
		t.Fatalf("capabilities %v should include %s", caps, capRestoreUploadConfinement)
	}
}

// Every panel caller passes a mode; a call without one comes from a panel older
// than this agent and must not restore unconfined.
func TestBackupRestoreFromTar_RequiresAMode(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"job_id": "x", "tar_path": "/x", "target_username": "alice"})
	_, err := backupRestoreFromTarHandler(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "mode must be tenant or upload") {
		t.Fatalf("got %v, want the missing mode refused", err)
	}
}

// The panel registers database and docker app rows from an uploaded file only
// for what the restore created or wrote for the account. A database or app
// folder it refused (one that exists here and isn't the account's) must not
// be reported, or the panel would hand the account that data as a row.
func TestUploadRestore_ReportsWhatItRestoredForTheAccount(t *testing.T) {
	me := currentUsername(t)
	uploadExecRecorder(t, me+"_orphan")
	root := t.TempDir()
	live := t.TempDir()
	prev := restoreDockerRoot
	restoreDockerRoot = live
	t.Cleanup(func() { restoreDockerRoot = prev })
	mustWrite(t, filepath.Join(live, "taken", "compose.yml"), "services: {}")
	var stages []backup.ManifestStage
	for _, n := range []string{me + "_new", me + "_owned", me + "_orphan", "bob_shop"} {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{n}})
	}
	for _, s := range []string{"mine", "fresh", "taken"} {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDocker, Items: []string{s}})
	}
	results := stageUpload(t, root, stages)
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_",
		AllowedDBNames: []string{me + "_owned"}, ForeignDBNames: []string{"bob_shop"},
		OwnedDockerSlugs: []string{"mine"}, ForeignDockerSlugs: []string{}, Claims: claims}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if strings.Join(claims.Databases, ",") != me+"_new,"+me+"_owned" {
		t.Errorf("restored databases %v (warnings %v), want [%s_new %s_owned]", claims.Databases, warnings, me, me)
	}
	if strings.Join(claims.DockerSlugs, ",") != "mine,fresh" {
		t.Errorf("restored docker slugs %v (warnings %v), want [mine fresh]", claims.DockerSlugs, warnings)
	}
}

// The panel restores mail in a second pass, after it rebuilt the account's
// domains: the first pass skips the mail stage.
func TestStageSelected(t *testing.T) {
	skip := componentFilter([]string{"mail"})
	for _, c := range []struct {
		want  []string
		name  string
		apply bool
	}{
		{nil, "home", true},
		{nil, "mail", false},
		{[]string{"home", "mail"}, "mail", false},
		{[]string{"home", "mail"}, "db", false},
		{[]string{"home", "mail"}, "home", true},
	} {
		if got := stageSelected(componentFilter(c.want), skip, c.name); got != c.apply {
			t.Errorf("components %v skip [mail]: stage %s applied=%v, want %v", c.want, c.name, got, c.apply)
		}
	}
	if !stageSelected(nil, nil, "mail") {
		t.Error("no filter and no skip must apply every stage")
	}
}

// An upload restore always removes its staging: it is a full extracted copy of
// the uploaded file, and the panel's mail pass applies nothing for an archive
// without mail.
func TestRemoveRestoreStaging(t *testing.T) {
	if !removeRestoreStaging(nil, restoreEnforcement{Mode: restoreModeUpload}) {
		t.Error("an upload restore that applied nothing must still remove its staging")
	}
	if removeRestoreStaging(nil, restoreEnforcement{Mode: "tenant"}) {
		t.Error("a tenant restore that applied nothing keeps its staging, as before")
	}
	if !removeRestoreStaging([]string{"home → /home/x"}, restoreEnforcement{Mode: "tenant"}) {
		t.Error("a restore that applied something removes its staging")
	}
}

// Same for PostgreSQL: a database the probe finds that isn't the account's is
// refused and not reported; a new one is created and reported.
func TestUploadRestore_ReportsTheNewPostgresDatabase(t *testing.T) {
	me := currentUsername(t)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if strings.Contains(strings.Join(args, " "), "datname = '"+me+"_pgorphan'") {
			return exec.CommandContext(ctx, "echo", "1")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	pgLoads(t)
	root := t.TempDir()
	var stages []backup.ManifestStage
	results := []backupRestoreStage{}
	for _, n := range []string{me + "_pgnew", me + "_pgorphan"} {
		stages = append(stages, backup.ManifestStage{Name: backup.StageDB, Items: []string{n}})
		results = append(results, backupRestoreStage{Name: backup.StageDB, Status: backup.StageStatusOK})
		mustWrite(t, filepath.Join(root, "db", n+".pgdump"), "PGDMP")
	}
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{}, ForeignDBNames: []string{}, Claims: claims}

	_, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if strings.Join(claims.Databases, ",") != me+"_pgnew" {
		t.Errorf("restored databases %v (warnings %v), want [%s_pgnew]", claims.Databases, warnings, me)
	}
	if !hasWarning(warnings, `"`+me+`_pgorphan": not restored: a database with this name already exists`) {
		t.Errorf("warnings %v should refuse the existing PostgreSQL database", warnings)
	}
}
