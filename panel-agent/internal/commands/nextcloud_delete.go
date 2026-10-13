package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type nextcloudDeleteReq struct {
	AppType string `json:"app_type"` // present, ignored
	OSUser  string `json:"os_user"`
	Docroot string `json:"docroot"`
	Domain  string `json:"domain,omitempty"`
}

type nextcloudDeleteResp struct {
	Status string `json:"status"`
}

// nextcloudTopLevel lists every entry the Nextcloud 35 release lays down at
// the (flattened) docroot, from `tar -tjf nextcloud-35.0.1.tar.bz2`. A docroot
// delete removes these instead of `rm -rf`-ing a docroot that might hold other
// content. config/ holds config.php, written by the install. Bump with the
// pinned version if a release adds a top-level entry.
var nextcloudTopLevel = []string{
	// files
	".htaccess", ".user.ini", "AUTHORS", "COPYING", "REUSE.toml",
	"composer.json", "composer.lock", "console.php", "cron.php", "index.html",
	"index.php", "occ", "package-lock.json", "package.json", "public.php",
	"remote.php", "robots.txt", "status.php", "version.php",
	// directories
	"3rdparty", "LICENSES", "apps", "config", "core", "dist", "lib", "ocs",
	"ocs-provider", "resources", "themes", "updater",
}

// nextcloudDeleteHandler removes a Nextcloud install: its files, its data
// directory (every file the install's users stored — deleting the app deletes
// them, as for Moodle's moodledata), and its nginx drop-in. Every removal runs
// as the tenant, so a link the tenant planted can't redirect it.
func nextcloudDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req nextcloudDeleteReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if req.OSUser == "" || !osUserSafeRE.MatchString(req.OSUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is missing or invalid"}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid docroot: %v", err)}
	}
	if req.Domain != "" && !domainRegex.MatchString(req.Domain) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid domain"}
	}

	// Docroot-only (RootOnly): the install path is the docroot.
	installPath, err := appInstallPath(req.Docroot, "")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	dataDir, err := nextcloudDataDir(req.OSUser, installPath)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	// Stop serving it first, so nothing is served half-removed.
	if req.Domain != "" {
		_ = removeNextcloudNginx(ctx, req.Domain)
	}
	for _, name := range nextcloudTopLevel {
		_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name)).Run()
	}
	_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", dataDir).Run()

	// Restore a branded placeholder so the bare docroot doesn't 403/404.
	if req.Domain != "" {
		indexPath := filepath.Join(req.Docroot, "index.html")
		if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
			_ = writeDefaultIndex(ctx, indexPath, req.OSUser, req.Domain, req.Docroot, "")
		}
	}

	return nextcloudDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("nextcloud", nextcloudDeleteHandler)
}
