package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type phpbbDeleteReq struct {
	AppType      string `json:"app_type"`
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type phpbbDeleteResp struct {
	Status string `json:"status"`
}

// phpbbTopLevel lists every entry the upstream phpBB tarball lays down
// at the install root after `tar --strip-components=1`, plus
// config.php which the CLI installer generates.
//
// Generated from phpBB 3.3.13. Bump if a future release adds entries.
var phpbbTopLevel = []string{
	".htaccess",
	"adm",
	"assets",
	"bin",
	"cache",
	"common.php",
	"composer.json",
	"composer.lock",
	"config",
	"config.php",
	"cron.php",
	"docs",
	"download",
	"download.php",
	"ext",
	"faq.php",
	"feed.php",
	"files",
	"images",
	"includes",
	"index.php",
	"install",
	"language",
	"memberlist.php",
	"mcp.php",
	"phpbb",
	"posting.php",
	"report.php",
	"search.php",
	"store",
	"styles",
	"ucp.php",
	"vendor",
	"viewforum.php",
	"viewonline.php",
	"viewtopic.php",
	"web.config",
}

func phpbbDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req phpbbDeleteReq
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

	installPath, err := computePhpbbInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	if req.Subdirectory != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range phpbbTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
	}

	if req.Subdirectory == "" && req.Domain != "" {
		indexPath := filepath.Join(req.Docroot, "index.html")
		if _, err := os.Stat(indexPath); os.IsNotExist(err) {
			_ = writeDefaultIndex(ctx, indexPath, req.OSUser, req.Domain, req.Docroot, "")
		}
	}

	return phpbbDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("phpbb", phpbbDeleteHandler)
}
