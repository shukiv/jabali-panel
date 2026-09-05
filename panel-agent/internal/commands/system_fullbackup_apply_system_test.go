package commands

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// The container also holds every users/*.tar.zst; the extractor must pull ONLY
// the top-level system.tar.zst (a full extract would write hundreds of GB to
// reach it on a real DR box).
func TestExtractSystemInnerFromContainer(t *testing.T) {
	dir := t.TempDir()
	container := filepath.Join(dir, "container.tar")
	f, err := os.Create(container)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	write := func(name, body string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{"run_id":"x"}`)
	write("users/alice.tar.zst", strings.Repeat("A", 4096)) // must be skipped
	write("system.tar.zst", "SYSTEM-LEG")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dest := filepath.Join(dir, "out", "system.tar.zst")
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := extractSystemInnerFromContainer(container, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "SYSTEM-LEG" {
		t.Errorf("extracted content = %q, want the system leg", got)
	}
	// Only the one dest file exists in the out dir — no users/ leaked through.
	ents, _ := os.ReadDir(filepath.Dir(dest))
	if len(ents) != 1 || ents[0].Name() != "system.tar.zst" {
		t.Errorf("out dir should hold only system.tar.zst, got %v", ents)
	}
}

func TestExtractSystemInnerFromContainer_MissingLeg(t *testing.T) {
	dir := t.TempDir()
	container := filepath.Join(dir, "c.tar")
	f, _ := os.Create(container)
	tw := tar.NewWriter(f)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("{}"))
	_ = tw.Close()
	f.Close()
	if err := extractSystemInnerFromContainer(container, filepath.Join(dir, "out.zst")); err == nil {
		t.Error("a container with no system.tar.zst must be rejected")
	}
}

// GH #1408 slice 3: the manifest is reconstructed from the extracted job tree.
// panel_db yields one stage PER <db>.sql (applyPanelDBStage loads Items[0]);
// every other recognized stage dir yields one stage; unknown dirs are ignored.
func TestReconstructSystemStages(t *testing.T) {
	job := t.TempDir()
	mk := func(parts ...string) {
		if err := os.MkdirAll(filepath.Join(append([]string{job}, parts...)...), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(job, dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk(backup.StagePanelDB)
	touch(backup.StagePanelDB, "jabali_panel.sql")
	touch(backup.StagePanelDB, "jabali_kratos.sql")
	touch(backup.StagePanelDB, "jabali_pdns.sql")
	touch(backup.StagePanelDB, "notes.txt") // non-.sql ignored
	mk(backup.StagePanelConfig)
	mk(backup.StageTLS)
	mk(backup.StageOSUsers)
	mk("junk") // unknown dir ignored

	stages, err := reconstructSystemStages(job)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	var panelDBs []string
	others := map[string]int{}
	for _, s := range stages {
		if s.Name == backup.StagePanelDB {
			if len(s.Items) != 1 {
				t.Fatalf("panel_db stage must carry exactly one db, got %v", s.Items)
			}
			panelDBs = append(panelDBs, s.Items[0])
		} else {
			others[s.Name]++
		}
	}
	if got := strings.Join(panelDBs, ","); got != "jabali_kratos,jabali_panel,jabali_pdns" {
		t.Errorf("panel_db stages (sorted) = %q, want the three system DBs", got)
	}
	for _, want := range []string{backup.StagePanelConfig, backup.StageTLS, backup.StageOSUsers} {
		if others[want] != 1 {
			t.Errorf("stage %s: got %d, want 1", want, others[want])
		}
	}
	if others["junk"] != 0 {
		t.Error("unknown 'junk' dir must not become a stage")
	}
}

// A panel_db dir with no <db>.sql is a corrupt container — fail, don't return an
// empty (silently no-op) db load.
func TestReconstructSystemStages_EmptyPanelDBFails(t *testing.T) {
	job := t.TempDir()
	if err := os.MkdirAll(filepath.Join(job, backup.StagePanelDB), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := reconstructSystemStages(job); err == nil {
		t.Error("empty panel_db stage must be rejected")
	}
}

func TestSoleJobDir(t *testing.T) {
	// exactly one job dir → returned
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "01JOBULID"), 0o750); err != nil {
		t.Fatal(err)
	}
	dir, id, err := soleJobDir(root)
	if err != nil || id != "01JOBULID" || dir != filepath.Join(root, "01JOBULID") {
		t.Fatalf("soleJobDir single: dir=%q id=%q err=%v", dir, id, err)
	}

	// two job dirs → refuse (ambiguous; never guess which system to apply)
	two := t.TempDir()
	_ = os.MkdirAll(filepath.Join(two, "a"), 0o750)
	_ = os.MkdirAll(filepath.Join(two, "b"), 0o750)
	if _, _, err := soleJobDir(two); err == nil {
		t.Error("two job dirs must be rejected")
	}
}
