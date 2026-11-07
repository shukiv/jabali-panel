package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type prestashopDeleteReq struct {
	AppType      string `json:"app_type"`
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type prestashopDeleteResp struct {
	Status string `json:"status"`
}

// prestashopTopLevel lists every entry the upstream PrestaShop inner
// zip drops into installPath after the flatten step. PrestaShop 8
// has a deeper directory tree than the smaller CMSes — these are just
// the top-level entries; rm -rf handles the nested content.
//
// Generated from PrestaShop 8.2.0.
var prestashopTopLevel = []string{
	".htaccess",
	"admin",
	"app",
	"bin",
	"cache",
	"classes",
	"composer.json",
	"composer.lock",
	"config",
	"controllers",
	"docs",
	"download",
	"img",
	"index.php",
	"INSTALL.txt",
	"install",
	"js",
	"LICENSES",
	"localization",
	"log",
	"mails",
	"modules",
	"override",
	"pdf",
	"README.md",
	"robots.txt",
	"src",
	"themes",
	"tools",
	"translations",
	"upload",
	"var",
	"vendor",
	"webservice",
}

func prestashopDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req prestashopDeleteReq
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

	installPath, err := computePrestaShopInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	if req.Subdirectory != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range prestashopTopLevel {
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

	return prestashopDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("prestashop", prestashopDeleteHandler)
}
