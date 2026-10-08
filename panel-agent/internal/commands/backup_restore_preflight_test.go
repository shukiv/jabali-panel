package commands

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: the restore preflight. The inspect step reads the archive's
// metadata and hands the panel a summary of it, never the metadata itself;
// a restore can leave PostgreSQL out; and a backup records the PHP
// extensions its PHP versions had.

// zstdTar writes entries as a zstd tar and returns its path.
func zstdTar(t *testing.T, entries []tentry) string {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	withRealExec(t) // zstd -dc only reads the archive
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.tar")
	if err := os.WriteFile(plain, buildTar(t, entries).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("zstd", "-q", plain, "-o", plain+".zst").CombinedOutput(); err != nil {
		t.Fatalf("zstd: %v %s", err, out)
	}
	return plain + ".zst"
}

func testManifest(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(backup.AccountManifest{SchemaVersion: backup.ManifestSchemaVersion, Kind: backup.KindAccountBackup,
		User:   backup.ManifestUser{Username: "alice"},
		Stages: []backup.ManifestStage{{Name: "home"}, {Name: "db", Items: []string{"alice_shop"}}, {Name: "meta"}, {Name: "manifest"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Archive members come in the order the backup's directories were read, so
// the metadata may come after the whole home tree.
func TestReadBundleFromZstdTar_ReadsBothWhereverTheyAre(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	for _, c := range []struct {
		name    string
		entries []tentry
	}{
		{"metadata last", []tentry{
			{typ: tar.TypeReg, name: "job/manifest/manifest.json", body: "M"},
			{typ: tar.TypeReg, name: "job/home/alice/big", body: big},
			{typ: tar.TypeReg, name: "job/meta/metadata.json", body: "D"},
		}},
		{"manifest last", []tentry{
			{typ: tar.TypeReg, name: "job/meta/metadata.json", body: "D"},
			{typ: tar.TypeReg, name: "job/home/alice/big", body: big},
			{typ: tar.TypeReg, name: "job/manifest/manifest.json", body: "M"},
		}},
		{"decoys", []tentry{
			{typ: tar.TypeReg, name: "job/home/alice/meta/metadata.json", body: "home"},
			{typ: tar.TypeReg, name: "job/manifest/manifest.json", body: "M"},
			{typ: tar.TypeReg, name: "other/meta/metadata.json", body: "other"},
			{typ: tar.TypeReg, name: "job/meta/metadata.json", body: "D"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, d, err := readBundleFromZstdTar(context.Background(), zstdTar(t, c.entries))
			if err != nil || string(m) != "M" || string(d) != "D" {
				t.Errorf("manifest %q metadata %q err %v, want M and D", m, d, err)
			}
		})
	}
}

func TestReadBundleFromZstdTar_NoMetadataIsNil(t *testing.T) {
	m, d, err := readBundleFromZstdTar(context.Background(), zstdTar(t, []tentry{{typ: tar.TypeReg, name: "job/manifest/manifest.json", body: "M"}}))
	if err != nil || string(m) != "M" || d != nil {
		t.Errorf("manifest %q metadata %q err %v, want M and nil", m, d, err)
	}
	if _, _, err := readBundleFromZstdTar(context.Background(), zstdTar(t, []tentry{{typ: tar.TypeReg, name: "job/meta/metadata.json", body: "D"}})); err == nil {
		t.Error("an archive with no manifest read without an error")
	}
}

func TestInspectUploadedBundle_SummarizesTheMetadata(t *testing.T) {
	meta, _ := json.Marshal(backup.AccountMetadata{
		PHPPools:      []backup.MetadataPHPPool{{PHPVersion: "8.3"}},
		PHPExtensions: map[string][]string{"8.3": {"intl"}},
		Databases:     []backup.MetadataDatabase{{ID: "d1", Name: "alice_shop", Engine: "postgres"}},
		DatabaseUsers: []backup.MetadataDatabaseUser{{Username: "alice_pg", Engine: "postgres", PostgresPasswordVerifier: "SCRAM-SHA-256$4096:SECRET"}},
	})
	res, err := inspectUploadedBundle([]byte(testManifest(t)), meta)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PreflightSupported || res.Summary == nil {
		t.Fatalf("result %+v, want preflight_supported and a summary", res)
	}
	want := backup.BundleSummary{PHPVersions: []string{"8.3"}, PHPExtensions: map[string][]string{"8.3": {"intl"}},
		PostgresDatabases: []string{"alice_shop"}, PostgresUsers: 1, DockerApps: []string{}}
	if !reflect.DeepEqual(*res.Summary, want) {
		t.Errorf("summary %+v, want %+v", *res.Summary, want)
	}
	if !reflect.DeepEqual(res.Components, []string{"home", "db"}) {
		t.Errorf("components %v", res.Components)
	}
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("the inspect result carries the metadata's secrets: %s", out)
	}

	noMeta, err := inspectUploadedBundle([]byte(testManifest(t)), nil)
	if err != nil || noMeta.Summary != nil || !noMeta.PreflightSupported {
		t.Errorf("no metadata: %+v err %v, want no summary and preflight_supported", noMeta, err)
	}
}

func TestUploadRestore_SkipPostgresLeavesPostgresOut(t *testing.T) {
	me := currentUsername(t)
	cmds := uploadExecRecorder(t)
	root := t.TempDir()
	stages := []backup.ManifestStage{{Name: backup.StageDB, Items: []string{me + "_pg"}}}
	results := []backupRestoreStage{{Name: backup.StageDB, Status: backup.StageStatusOK}}
	mustWrite(t, filepath.Join(root, "db", me+"_pg.pgdump"), "PGDMP")
	claims := &restoreClaims{}
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_", AllowedDBNames: []string{}, ForeignDBNames: []string{}, Claims: claims, SkipPostgres: true}

	applied, warnings := applyAccountRestore(context.Background(), root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	if len(*cmds) != 0 || len(applied) != 0 || len(claims.Databases) != 0 {
		t.Errorf("ran %v, applied %v, claimed %v; want nothing", *cmds, applied, claims.Databases)
	}
	if !hasWarning(warnings, "db "+me+"_pg (postgres): not restored: PostgreSQL is turned off on this server") {
		t.Errorf("warnings %v, want the PostgreSQL database left out", warnings)
	}
	if !(backupRestoreFromTarParams{SkipPostgres: true}).enforcement().SkipPostgres {
		t.Error("skip_postgres doesn't reach the restore's enforcement")
	}
}

func TestEnrichPHPExtensions_RecordsTheEnabledOnes(t *testing.T) {
	installTestFixtures(t, phpExtTestFixtures{
		installed: []string{"8.3"},
		confDOut: []string{
			"/etc/php/8.3/fpm/conf.d/20-intl.ini",
			"/etc/php/8.3/fpm/conf.d/20-redis.ini",
			"/etc/php/8.3/fpm/conf.d/20-calendar.ini", // built in: always there
			"/etc/php/8.3/fpm/conf.d/20-unknownext.ini",
		},
	})
	m := &backup.AccountMetadata{PHPPools: []backup.MetadataPHPPool{
		{PHPVersion: "8.3"}, {PHPVersion: "8.3"},
		{PHPVersion: "7.4"},  // not installed here
		{PHPVersion: "../x"}, // not a version
	}}
	if err := enrichPHPExtensions(m); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"8.3": {"intl", "redis"}}
	if !reflect.DeepEqual(m.PHPExtensions, want) {
		t.Errorf("recorded %v, want %v", m.PHPExtensions, want)
	}
}
