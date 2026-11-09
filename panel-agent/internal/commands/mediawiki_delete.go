package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type mediawikiDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type mediawikiDeleteResp struct {
	Status string `json:"status"`
}

// mediawikiTopLevel lists every entry the upstream MediaWiki tarball
// lays down at the install root after `tar --strip-components=1`,
// plus LocalSettings.php which the CLI installer generates. Same
// rationale as dokuwikiTopLevel — for docroot installs we enumerate
// instead of `rm -rf` so user-uploaded sibling files survive.
//
// Generated from MediaWiki 1.41.2; bump if a future release adds a
// top-level entry. Entries that don't exist on disk are skipped
// silently.
var mediawikiTopLevel = []string{
	"api.php",
	"autoload.php",
	"bin",
	"cache",
	"COPYING",
	"composer.json",
	"composer.local.json-sample",
	"docs",
	"extensions",
	"FAQ",
	"HISTORY",
	"images",
	"img_auth.php",
	"includes",
	"index.php",
	"INSTALL",
	"jsduck.json",
	"languages",
	"LICENSES",
	"load.php",
	"LocalSettings.php",
	"maintenance",
	"mw-config",
	"opensearch_desc.php",
	"phpcs.xml",
	"profileinfo.php",
	"README.md",
	"resources",
	"rest.php",
	"skins",
	"tests",
	"thumb.php",
	"thumb_handler.php",
	"UPGRADE",
	"vendor",
}

func mediawikiDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req mediawikiDeleteReq
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

	installPath, err := computeMediaWikiInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	if req.Subdirectory != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range mediawikiTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
		// MediaWiki's RELEASE-NOTES file is versioned (e.g. RELEASE-NOTES-1.41).
		// The exact suffix changes per release so enumerating it in
		// mediawikiTopLevel would mean bumping the deleter for every
		// upstream version. Glob it instead so a delete after an upgrade
		// still cleans up.
		if matches, err := filepath.Glob(filepath.Join(installPath, "RELEASE-NOTES-*")); err == nil {
			for _, m := range matches {
				if filepath.Dir(m) == installPath {
					cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", m)
					_ = cmd.Run()
				}
			}
		}
	}

	return mediawikiDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("mediawiki", mediawikiDeleteHandler)
}
