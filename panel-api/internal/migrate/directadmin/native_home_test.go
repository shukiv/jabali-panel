package directadmin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
)

type recordingAgent struct {
	calls []map[string]any
	fail  string // a src_dir whose copy fails
}

func (a *recordingAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	p := params.(map[string]any)
	if command != "migration.import_home" {
		return nil, errors.New("unexpected command " + command)
	}
	a.calls = append(a.calls, p)
	if a.fail != "" && p["src_dir"] == a.fail {
		return nil, errors.New("rsync failed")
	}
	return json.RawMessage(`{"bytes_copied":10,"files":2}`), nil
}

func nativeHomeFixture(t *testing.T) *cpanel.ParsedTarball {
	home := filepath.Join(t.TempDir(), "home")
	for _, d := range []string{"domains/a.example/public_html", "domains/shop.a.example/public_html", "domains/a.example/public_ftp"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return &cpanel.ParsedTarball{
		HomeDir: home,
		DocRoots: map[string]string{
			"shop.a.example": "/home/u/domains/shop.a.example/public_html",
			"a.example":      "/home/u/domains/a.example/public_html",
			"gone.example":   "/home/u/domains/gone.example/public_html",
		},
	}
}

// Each docroot is copied on its own, into its own subpath, so the agent copies
// it without --delete-after; then the rest of the home, with every public_html
// left out of that whole-home copy.
func TestImportNativeHome(t *testing.T) {
	parsed := nativeHomeFixture(t)
	a := &recordingAgent{}
	res, err := ImportNativeHome(context.Background(), a, parsed, "JOB", "u")
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"job_id": "JOB", "src_dir": filepath.Join(parsed.HomeDir, "domains/a.example/public_html"), "dest_user": "u", "dest_subpath": "domains/a.example/public_html"},
		{"job_id": "JOB", "src_dir": filepath.Join(parsed.HomeDir, "domains/shop.a.example/public_html"), "dest_user": "u", "dest_subpath": "domains/shop.a.example/public_html"},
		{"job_id": "JOB", "src_dir": parsed.HomeDir, "dest_user": "u", "extra_excludes": []string{"public_html", "public_html/", "public_html/**"}},
	}
	if !reflect.DeepEqual(a.calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", a.calls, want)
	}
	if res.DomainsCopied != 2 || res.BytesCopied != 30 || res.Files != 6 {
		t.Errorf("result %+v", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "home_skip:gone.example: no public_html in the backup" {
		t.Errorf("skipped %v", res.Skipped)
	}
}

// A docroot that fails to copy fails the home step: the site would be missing.
func TestImportNativeHome_CopyFails(t *testing.T) {
	parsed := nativeHomeFixture(t)
	a := &recordingAgent{fail: filepath.Join(parsed.HomeDir, "domains/a.example/public_html")}
	if _, err := ImportNativeHome(context.Background(), a, parsed, "JOB", "u"); err == nil {
		t.Fatal("a failed docroot copy passed")
	}
	if len(a.calls) != 1 {
		t.Errorf("went on after the failure: %d calls", len(a.calls))
	}
}
