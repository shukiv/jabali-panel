package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func callInstall(t *testing.T, version, sha, b64 string) (ioncubeInstallResponse, *agentwire.AgentError) {
	t.Helper()
	raw, _ := json.Marshal(ioncubeInstallParams{Version: version, SHA256: sha, SoB64: b64})
	out, err := phpIoncubeInstallHandler(context.Background(), raw)
	if err != nil {
		var ae *agentwire.AgentError
		if errors.As(err, &ae) {
			return ioncubeInstallResponse{}, ae
		}
		t.Fatalf("non-agent error: %v", err)
	}
	return out.(ioncubeInstallResponse), nil
}

func callUninstall(t *testing.T, version string) (ioncubeUninstallResponse, *agentwire.AgentError) {
	t.Helper()
	raw, _ := json.Marshal(ioncubeUninstallParams{Version: version})
	out, err := phpIoncubeUninstallHandler(context.Background(), raw)
	if err != nil {
		var ae *agentwire.AgentError
		if errors.As(err, &ae) {
			return ioncubeUninstallResponse{}, ae
		}
		t.Fatalf("non-agent error: %v", err)
	}
	return out.(ioncubeUninstallResponse), nil
}

// TestIoncubeInstall_VerifyWriteIdempotent: install writes the .so after a
// sha match; a second identical install is a no-op; a sha mismatch is rejected.
func TestIoncubeInstall_VerifyWriteIdempotent(t *testing.T) {
	wireIoncubeTestRoots(t)
	blob := []byte("\x7fELF fake loader")
	sum := sha256.Sum256(blob)
	shaHex := hex.EncodeToString(sum[:])
	b64 := base64.StdEncoding.EncodeToString(blob)

	// Install → changed=true, .so present with correct bytes.
	resp, ae := callInstall(t, "8.4", shaHex, b64)
	if ae != nil {
		t.Fatalf("install: %v", ae)
	}
	if !resp.Installed || !resp.Changed {
		t.Errorf("install: installed=%v changed=%v want true/true", resp.Installed, resp.Changed)
	}
	got, err := os.ReadFile(ioncubeLoaderSoPath("8.4"))
	if err != nil || string(got) != string(blob) {
		t.Fatalf("loader not written correctly: %v", err)
	}

	// Re-install identical → idempotent no-op.
	if resp, _ := callInstall(t, "8.4", shaHex, b64); resp.Changed {
		t.Errorf("second identical install should be changed=false")
	}

	// sha mismatch → rejected, invalid argument.
	if _, ae := callInstall(t, "8.4", hex.EncodeToString(sha256.New().Sum(nil)), b64); ae == nil || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("sha mismatch: want CodeInvalidArgument, got %+v", ae)
	}
}

// TestIoncubeUninstall_BlockedWhileEnabled: uninstall must refuse while the
// loader is enabled server-wide for that version, then succeed once disabled.
func TestIoncubeUninstall_BlockedWhileEnabled(t *testing.T) {
	wireIoncubeTestRoots(t)
	blob := []byte("loader")
	sum := sha256.Sum256(blob)
	callInstall(t, "8.4", hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(blob))

	// Enable server-wide on 8.4.
	if _, ae := callServerSet(t, "8.4", true); ae != nil {
		t.Fatalf("enable: %v", ae)
	}

	// Uninstall blocked.
	if _, ae := callUninstall(t, "8.4"); ae == nil || ae.Code != agentwire.CodeFailedPrecondition {
		t.Errorf("uninstall while enabled: want CodeFailedPrecondition, got %+v", ae)
	}

	// Disable, then uninstall succeeds.
	if _, ae := callServerSet(t, "8.4", false); ae != nil {
		t.Fatalf("disable: %v", ae)
	}
	resp, ae := callUninstall(t, "8.4")
	if ae != nil {
		t.Fatalf("uninstall after disable: %v", ae)
	}
	if resp.Installed || !resp.Changed {
		t.Errorf("uninstall: installed=%v changed=%v want false/true", resp.Installed, resp.Changed)
	}
	if _, err := os.Stat(ioncubeLoaderSoPath("8.4")); !os.IsNotExist(err) {
		t.Errorf("loader still present after uninstall")
	}
}

func TestIoncubeInstall_RejectsBadInput(t *testing.T) {
	wireIoncubeTestRoots(t)
	// bad version
	if _, ae := callInstall(t, "8", "aa", "eA=="); ae == nil || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("bad version: want CodeInvalidArgument, got %+v", ae)
	}
	// bad sha length
	if _, ae := callInstall(t, "8.4", "short", "eA=="); ae == nil || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("bad sha: want CodeInvalidArgument, got %+v", ae)
	}
}
