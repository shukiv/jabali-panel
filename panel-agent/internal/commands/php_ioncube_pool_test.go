package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// wireIoncubeTestRoots points the /etc/php and /usr/lib/php/ioncube roots at
// temp dirs and skips the systemctl reload, so the handler runs without root.
func wireIoncubeTestRoots(t *testing.T) (etcRoot, libRoot string) {
	t.Helper()
	etcRoot = filepath.Join(t.TempDir(), "php")
	libRoot = filepath.Join(t.TempDir(), "ioncube")
	t.Setenv("JABALI_PHP_ETC_ROOT", etcRoot)
	t.Setenv("JABALI_IONCUBE_LIB_ROOT", libRoot)
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	return etcRoot, libRoot
}

func installFakeLoader(t *testing.T, libRoot, version string) {
	t.Helper()
	so := filepath.Join(libRoot, version, "ioncube_loader_lin_"+version+".so")
	if err := os.MkdirAll(filepath.Dir(so), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(so, []byte("\x7fELF-fake"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callPoolSet(t *testing.T, user, version string, enabled bool) (ioncubePoolSetResponse, *agentwire.AgentError) {
	t.Helper()
	raw, _ := json.Marshal(ioncubePoolSetParams{Username: user, Version: version, Enabled: enabled})
	out, err := phpIoncubePoolSetHandler(context.Background(), raw)
	if err != nil {
		var ae *agentwire.AgentError
		if errors.As(err, &ae) {
			return ioncubePoolSetResponse{}, ae
		}
		t.Fatalf("unexpected non-agent error: %v", err)
	}
	resp, ok := out.(ioncubePoolSetResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", out)
	}
	return resp, nil
}

// TestIoncubePoolSet_EnableRequiresInstalledLoader: enabling before the loader
// .so exists is a precondition failure, not a silent no-op.
func TestIoncubePoolSet_EnableRequiresInstalledLoader(t *testing.T) {
	wireIoncubeTestRoots(t)
	_, ae := callPoolSet(t, "arizote", "8.1", true)
	if ae == nil {
		t.Fatal("expected CodeFailedPrecondition when loader not installed, got success")
	}
	if ae.Code != agentwire.CodeFailedPrecondition {
		t.Errorf("code = %q, want %q", ae.Code, agentwire.CodeFailedPrecondition)
	}
	if _, err := os.Stat(ioncubeIniPath("8.1", "arizote")); !os.IsNotExist(err) {
		t.Errorf("ini should not have been written on a failed enable")
	}
}

// TestIoncubePoolSet_EnableThenDisable: the happy path — enable writes the ini
// pointing at the loader, disable removes it. Both idempotent.
func TestIoncubePoolSet_EnableThenDisable(t *testing.T) {
	_, libRoot := wireIoncubeTestRoots(t)
	installFakeLoader(t, libRoot, "8.1")

	// Enable → writes ini, changed=true.
	resp, ae := callPoolSet(t, "arizote", "8.1", true)
	if ae != nil {
		t.Fatalf("enable failed: %v", ae)
	}
	if !resp.Changed || !resp.Enabled {
		t.Errorf("enable: got changed=%v enabled=%v, want true/true", resp.Changed, resp.Enabled)
	}
	iniPath := ioncubeIniPath("8.1", "arizote")
	body, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatalf("ini not written: %v", err)
	}
	if string(body) != "zend_extension="+ioncubeLoaderSoPath("8.1")+"\n" {
		t.Errorf("ini body = %q", string(body))
	}

	// Enable again → idempotent, changed=false.
	if resp, _ := callPoolSet(t, "arizote", "8.1", true); resp.Changed {
		t.Errorf("second enable should be a no-op (changed=false), got changed=true")
	}

	// Disable → removes ini, changed=true.
	resp, ae = callPoolSet(t, "arizote", "8.1", false)
	if ae != nil {
		t.Fatalf("disable failed: %v", ae)
	}
	if !resp.Changed || resp.Enabled {
		t.Errorf("disable: got changed=%v enabled=%v, want true/false", resp.Changed, resp.Enabled)
	}
	if _, err := os.Stat(iniPath); !os.IsNotExist(err) {
		t.Errorf("ini still present after disable")
	}

	// Disable again → idempotent, changed=false.
	if resp, _ := callPoolSet(t, "arizote", "8.1", false); resp.Changed {
		t.Errorf("second disable should be a no-op (changed=false), got changed=true")
	}
}

// TestIoncubePoolSet_RejectsBadInput: username/version validated at the trust
// boundary before any filesystem path is built.
func TestIoncubePoolSet_RejectsBadInput(t *testing.T) {
	wireIoncubeTestRoots(t)
	for _, tc := range []struct{ user, version string }{
		{"../etc", "8.1"},
		{"arizote", "8.1; rm -rf"},
		{"arizote", "../../8.1"},
		{"", "8.1"},
	} {
		if _, ae := callPoolSet(t, tc.user, tc.version, true); ae == nil || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("user=%q ver=%q: want CodeInvalidArgument, got %+v", tc.user, tc.version, ae)
		}
	}
}

// TestIoncubeStatus_ReportsLiveState: status reflects installed loaders +
// enabled pools purely from the filesystem.
func TestIoncubeStatus_ReportsLiveState(t *testing.T) {
	_, libRoot := wireIoncubeTestRoots(t)
	installFakeLoader(t, libRoot, "8.1")
	installFakeLoader(t, libRoot, "8.3")
	if _, ae := callPoolSet(t, "arizote", "8.1", true); ae != nil {
		t.Fatalf("enable arizote: %v", ae)
	}

	out, err := phpIoncubeStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	st := out.(ioncubeStatusResponse)
	if len(st.InstalledVersions) != 2 || st.InstalledVersions[0] != "8.1" || st.InstalledVersions[1] != "8.3" {
		t.Errorf("installed = %v, want [8.1 8.3]", st.InstalledVersions)
	}
	if got := st.Enabled["arizote"]; len(got) != 1 || got[0] != "8.1" {
		t.Errorf("enabled[arizote] = %v, want [8.1]", got)
	}
}
