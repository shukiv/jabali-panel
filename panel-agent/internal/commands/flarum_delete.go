package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type flarumDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type flarumDeleteResp struct {
	Status string `json:"status"`
}

// flarumTopLevel lists every entry the no-public-dir archive lays down
// at the install root, plus config.php (written by `flarum install`).
// Used for docroot deletes so we don't rm -rf a docroot that may hold
// other content. Entries that don't exist on disk are skipped silently.
var flarumTopLevel = []string{
	".editorconfig",
	".env",
	".gitignore",
	".htaccess",
	".nginx.conf",
	"assets",
	"CHANGELOG.md",
	"composer.json",
	"composer.lock",
	"config.php",
	"extend.php",
	"flarum",
	"index.php",
	"LICENSE",
	"README.md",
	"site.php",
	"storage",
	"vendor",
	"web.config",
}

func flarumDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req flarumDeleteReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid docroot: %v", err)}
	}

	installPath, err := computeFlarumInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	sub := strings.Trim(req.Subdirectory, "/")

	if sub != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range flarumTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
	}

	// Remove the per-install nginx hardening snippet (docroot or subdir).
	if req.Domain != "" {
		_ = removeFlarumNginx(ctx, req.Domain, sub)
	}

	return flarumDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("flarum", flarumDeleteHandler)
}
