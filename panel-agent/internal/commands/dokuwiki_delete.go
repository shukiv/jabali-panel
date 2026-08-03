package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dokuwiki_delete.go — removes a DokuWiki install (GH #227: delete failed
// with `unknown app_type "dokuwiki" for app.delete` because dokuwiki
// registered an installer but never a deleter). DokuWiki is flat-file —
// no database to drop and no per-install nginx snippet to remove — so the
// deleter only has to clear the files the install laid down.

type dokuwikiDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type dokuwikiDeleteResp struct {
	Status string `json:"status"`
}

// dokuwikiTopLevel lists every entry the upstream DokuWiki tarball lays
// down at the install root after `tar --strip-components=1`. Same
// rationale as mediawikiTopLevel: for docroot installs we enumerate
// instead of `rm -rf <docroot>` so user-uploaded sibling files survive.
// `conf/` covers the jabali-written local.php / users.auth.php /
// acl.auth.php; `data/` covers the runtime wiki pages + media.
//
// Generated from DokuWiki 2024-02-06b; bump if a future release adds a
// top-level entry. Entries that don't exist on disk are skipped silently.
var dokuwikiTopLevel = []string{
	".htaccess.dist",
	"bin",
	"conf",
	"COPYING",
	"data",
	"doku.php",
	"feed.php",
	"inc",
	"index.php",
	"install.php",
	"lib",
	"README",
	"SECURITY.md",
	"vendor",
	"VERSION",
}

func dokuwikiDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req dokuwikiDeleteReq
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
		for _, name := range dokuwikiTopLevel {
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

	return dokuwikiDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("dokuwiki", dokuwikiDeleteHandler)
}
