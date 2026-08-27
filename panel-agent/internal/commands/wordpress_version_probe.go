// wordpress.version_probe — read <dir>/wp-includes/version.php at a WordPress
// root and report whether it exists plus the parsed $wp_version.
//
// Used by the reconciler's ready-install probe (GH #1237): it both detects drift
// (version.php gone → the install is broken) and refreshes the stored version
// after a WordPress core update / auto-update, which the panel otherwise never
// learned about (the version was captured once at install and never re-read).
//
// Privileged path-only, like fs.stat: the caller passes a system-derived docroot
// path, never user input, and version.php is a world-readable WP core file.
package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type wpVersionProbeParams struct {
	// Dir is the WordPress root (docroot [+ subdir]); version.php is read from
	// <Dir>/wp-includes/version.php.
	Dir string `json:"dir"`
}

type wpVersionProbeResponse struct {
	Exists  bool   `json:"exists"`
	Version string `json:"version,omitempty"`
}

func wpVersionProbeHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wpVersionProbeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if p.Dir == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "dir required"}
	}
	vp := filepath.Join(p.Dir, "wp-includes", "version.php")
	if _, err := os.Lstat(vp); err != nil {
		if os.IsNotExist(err) {
			return wpVersionProbeResponse{Exists: false}, nil
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// readWPVersion parses $wp_version from <dir>/wp-includes/version.php; it
	// returns "" if the file is unreadable/unparseable, which the caller treats
	// as "leave the stored version as-is".
	return wpVersionProbeResponse{Exists: true, Version: readWPVersion(p.Dir)}, nil
}

func init() {
	Default.Register("wordpress.version_probe", wpVersionProbeHandler)
}
