package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type itflowDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type itflowDeleteResp struct {
	Status string `json:"status"`
}

// itflowTopLevel lists every entry the ITFlow git clone lays down at the
// install root, plus config.php (setup_cli writes it) and .git (the
// clone). Used for docroot deletes so we don't rm -rf a docroot that may
// hold other content. Entries that don't exist on disk are skipped.
var itflowTopLevel = []string{
	".git", ".github", ".gitignore", ".htaccess",
	"CHANGELOG.md", "CODE_OF_CONDUCT.md", "LICENSE", "README.md", "SECURITY.md",
	"admin", "agent", "api", "client", "config.php", "cron", "css", "db.sql",
	"favicon.ico", "functions.php", "guest", "includes", "index.php", "js",
	"keepalive.php", "login.php", "modals", "plugins", "post", "robots.txt",
	"scripts", "setup", "uploads",
}

func itflowDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req itflowDeleteReq
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

	installPath, err := computeITFlowInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	sub := strings.Trim(req.Subdirectory, "/")

	if sub != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range itflowTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
	}

	if sub != "" && req.Domain != "" {
		_ = removeAppRewrite(ctx, "itflow", req.Domain, sub)
	}

	return itflowDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("itflow", itflowDeleteHandler)
}
