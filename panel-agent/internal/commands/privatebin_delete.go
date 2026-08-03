package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// privatebin_delete.go — removes a PrivateBin install (GH #631). Flat-file
// like DokuWiki, but with one extra: the paste store lives OUTSIDE the
// docroot (privatebinDataDir), so the deleter must reap it too or every
// deleted install leaks an orphan privatebin-data dir of encrypted pastes.

type privatebinDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type privatebinDeleteResp struct {
	Status string `json:"status"`
}

// privatebinTopLevel lists every entry the upstream tarball lays down at
// the install root after `tar --strip-components=1`, plus the
// jabali-written cfg/conf.php (covered by cfg). For docroot installs we
// enumerate instead of `rm -rf <docroot>` so user-uploaded sibling files
// survive. Generated from PrivateBin 2.0.5; bump on release bumps.
var privatebinTopLevel = []string{
	".htaccess.disabled",
	"CHANGELOG.md",
	"CREDITS.md",
	"LICENSE.md",
	"Procfile",
	"README.md",
	"SECURITY.md",
	"bin",
	"cfg",
	"composer.json",
	"composer.lock",
	"css",
	"favicon.ico",
	"i18n",
	"img",
	"index.php",
	"js",
	"lib",
	"manifest.json",
	"robots.txt",
	"tpl",
	"vendor",
}

func privatebinDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req privatebinDeleteReq
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

	installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	if req.Subdirectory != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range privatebinTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
	}

	// The outside-docroot paste store — same derivation the installer used.
	dataDir := privatebinDataDir(req.Docroot, req.Subdirectory)
	rmData := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", dataDir)
	_ = rmData.Run()

	if req.Subdirectory == "" && req.Domain != "" {
		indexPath := filepath.Join(req.Docroot, "index.html")
		if _, err := os.Stat(indexPath); os.IsNotExist(err) {
			_ = writeDefaultIndex(ctx, indexPath, req.OSUser, req.Domain, req.Docroot, "")
		}
	}

	return privatebinDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("privatebin", privatebinDeleteHandler)
}
