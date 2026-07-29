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
// temp dirs and skips the systemctl reload, so handlers run without root.
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

func callServerSet(t *testing.T, version string, enabled bool) (ioncubeServerSetResponse, *agentwire.AgentError) {
	t.Helper()
	raw, _ := json.Marshal(ioncubeServerSetParams{Version: version, Enabled: enabled})
	out, err := phpIoncubeServerSetHandler(context.Background(), raw)
	if err != nil {
		var ae *agentwire.AgentError
		if errors.As(err, &ae) {
			return ioncubeServerSetResponse{}, ae
		}
		t.Fatalf("non-agent error: %v", err)
	}
	return out.(ioncubeServerSetResponse), nil
}

// TestIoncubeServerSet_EnableRequiresInstalledLoader: enabling before the loader
// .so exists is a precondition failure, not a silent no-op.
func TestIoncubeServerSet_EnableRequiresInstalledLoader(t *testing.T) {
	wireIoncubeTestRoots(t)
	_, ae := callServerSet(t, "8.1", true)
	if ae == nil || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("want CodeFailedPrecondition when loader absent, got %+v", ae)
	}
	if _, err := os.Stat(ioncubeConfdIniPath("8.1")); !os.IsNotExist(err) {
		t.Errorf("conf.d ini should not exist after a failed enable")
	}
}

// TestIoncubeServerSet_EnableThenDisable: enable drops the conf.d ini pointing
// at the loader; disable removes it. Both idempotent.
func TestIoncubeServerSet_EnableThenDisable(t *testing.T) {
	_, libRoot := wireIoncubeTestRoots(t)
	installFakeLoader(t, libRoot, "8.4")

	resp, ae := callServerSet(t, "8.4", true)
	if ae != nil {
		t.Fatalf("enable: %v", ae)
	}
	if !resp.Changed || !resp.Enabled {
		t.Errorf("enable: changed=%v enabled=%v want true/true", resp.Changed, resp.Enabled)
	}
	body, err := os.ReadFile(ioncubeConfdIniPath("8.4"))
	if err != nil || string(body) != "zend_extension="+ioncubeLoaderSoPath("8.4")+"\n" {
		t.Fatalf("conf.d ini wrong: %q err=%v", string(body), err)
	}

	if resp, _ := callServerSet(t, "8.4", true); resp.Changed {
		t.Errorf("second enable should be a no-op")
	}

	resp, ae = callServerSet(t, "8.4", false)
	if ae != nil || !resp.Changed || resp.Enabled {
		t.Fatalf("disable: resp=%+v ae=%v", resp, ae)
	}
	if _, err := os.Stat(ioncubeConfdIniPath("8.4")); !os.IsNotExist(err) {
		t.Errorf("conf.d ini still present after disable")
	}

	if resp, _ := callServerSet(t, "8.4", false); resp.Changed {
		t.Errorf("second disable should be a no-op")
	}
}

func TestIoncubeServerSet_RejectsBadVersion(t *testing.T) {
	wireIoncubeTestRoots(t)
	for _, v := range []string{"8", "8.4.1", "../8.4", ""} {
		if _, ae := callServerSet(t, v, true); ae == nil || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("version %q: want CodeInvalidArgument, got %+v", v, ae)
		}
	}
}

// TestIoncubeStatus_ReportsLiveState: status reflects installed .so + enabled
// conf.d ini from the filesystem.
func TestIoncubeStatus_ReportsLiveState(t *testing.T) {
	_, libRoot := wireIoncubeTestRoots(t)
	installFakeLoader(t, libRoot, "8.1")
	installFakeLoader(t, libRoot, "8.4")
	if _, ae := callServerSet(t, "8.4", true); ae != nil {
		t.Fatalf("enable 8.4: %v", ae)
	}

	out, err := phpIoncubeStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	st := out.(ioncubeStatusResponse)
	if len(st.InstalledVersions) != 2 || st.InstalledVersions[0] != "8.1" || st.InstalledVersions[1] != "8.4" {
		t.Errorf("installed = %v, want [8.1 8.4]", st.InstalledVersions)
	}
	if len(st.EnabledVersions) != 1 || st.EnabledVersions[0] != "8.4" {
		t.Errorf("enabled = %v, want [8.4]", st.EnabledVersions)
	}
}
