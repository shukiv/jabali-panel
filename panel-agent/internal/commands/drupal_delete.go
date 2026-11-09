package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type drupalDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type drupalDeleteResp struct {
	Status string `json:"status"`
}

// drupalTopLevel lists every entry the upstream Drupal tarball lays
// down at the install root after `tar --strip-components=1`, plus
// settings.php (drush site:install writes it inside sites/default,
// which is already covered by the "sites" entry below) and the
// vendor/ + composer.lock that `composer require drush/drush` adds.
//
// Generated from Drupal 10.3.6; bump if a future release adds a top-
// level entry. Entries that don't exist on disk are skipped silently.
var drupalTopLevel = []string{
	".composer", // composer home created by installDrushViaComposer
	".csslintrc",
	".editorconfig",
	".eslintignore",
	".eslintrc.json",
	".gitattributes",
	".ht.router.php",
	".htaccess",
	".prettierignore",
	".prettierrc.json",
	"autoload.php",
	"composer.json",
	"composer.lock",
	"core",
	"example.gitignore",
	"index.php",
	"INSTALL.txt",
	"LICENSE.txt",
	"modules",
	"profiles",
	"README.md",
	"robots.txt",
	"sites",
	"themes",
	"update.php",
	"vendor",
	"web.config",
}

func drupalDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req drupalDeleteReq
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

	installPath, perr := computeDrupalInstallPath(req.Docroot, req.Subdirectory)
	if perr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: perr.Error()}
	}

	if req.Subdirectory != "" {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
	} else {
		for _, name := range drupalTopLevel {
			cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
			_ = cmd.Run()
		}
	}

	// Remove the per-install nginx rewrite snippet if one was written
	// at install time. No-op for docroot installs.
	if sub := strings.Trim(req.Subdirectory, "/"); sub != "" && req.Domain != "" {
		_ = removeAppRewrite(ctx, "drupal", req.Domain, sub)
	}

	return drupalDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("drupal", drupalDeleteHandler)
}
