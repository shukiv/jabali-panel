package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php_ioncube_install.go — install/uninstall the ionCube Loader .so for a PHP
// version. panel-api fetches + verifies the loader (ADR-0050: the agent has no
// outbound HTTPS) and passes the bytes base64-encoded plus the expected sha256;
// the agent re-verifies the bytes before writing them to the AppArmor-permitted
// /usr/lib/php/ioncube/<ver>/ path (root:root 0644). Uninstall refuses while any
// pool is still enabled on that version, so a live site can't lose its loader
// out from under it.

type ioncubeInstallParams struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	SoB64   string `json:"so_b64"`
}

type ioncubeInstallResponse struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Changed   bool   `json:"changed"`
}

func phpIoncubeInstallHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ioncubeInstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !phpVersionRegex.MatchString(p.Version) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid PHP version %q (want X.Y)", p.Version)}
	}
	if len(p.SHA256) != 64 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "sha256 must be 64 hex chars"}
	}
	data, err := base64.StdEncoding.DecodeString(p.SoB64)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("decode so_b64: %v", err)}
	}
	if len(data) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "so_b64 is empty"}
	}
	// Re-verify at the trust boundary: the bytes must match the sha the panel
	// verified against its pinned catalogue. Never write an unverified blob.
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, p.SHA256) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("loader sha256 mismatch: bytes=%s param=%s", got, p.SHA256)}
	}

	soPath := ioncubeLoaderSoPath(p.Version)
	// Idempotent: if an identical .so is already installed, no-op.
	if cur, err := os.ReadFile(soPath); err == nil {
		curSum := sha256.Sum256(cur)
		if hex.EncodeToString(curSum[:]) == strings.ToLower(p.SHA256) {
			return ioncubeInstallResponse{Version: p.Version, Installed: true, Path: soPath, Changed: false}, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(soPath), 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir loader dir: %v", err)}
	}
	if err := writeFileAtomic(soPath, data, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write loader: %v", err)}
	}
	return ioncubeInstallResponse{Version: p.Version, Installed: true, Path: soPath, Changed: true}, nil
}

type ioncubeUninstallParams struct {
	Version string `json:"version"`
}

type ioncubeUninstallResponse struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Changed   bool   `json:"changed"`
}

func phpIoncubeUninstallHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ioncubeUninstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !phpVersionRegex.MatchString(p.Version) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid PHP version %q (want X.Y)", p.Version)}
	}

	// Refuse while the loader is still enabled server-wide for this version —
	// the conf.d 00-ioncube.ini would point at a now-missing .so and every
	// master on that version would fatal on reload.
	if ioncubeVersionEnabled(p.Version) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("ionCube is still enabled for PHP %s; disable it first", p.Version),
		}
	}

	soPath := ioncubeLoaderSoPath(p.Version)
	if _, err := os.Stat(soPath); err != nil {
		return ioncubeUninstallResponse{Version: p.Version, Installed: false, Changed: false}, nil
	}
	if err := os.Remove(soPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove loader: %v", err)}
	}
	return ioncubeUninstallResponse{Version: p.Version, Installed: false, Changed: true}, nil
}

func init() {
	Default.Register("php.ioncube.install", phpIoncubeInstallHandler)
	Default.Register("php.ioncube.uninstall", phpIoncubeUninstallHandler)
}
