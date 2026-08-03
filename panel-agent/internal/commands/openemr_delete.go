package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// openemr_delete.go — removes an OpenEMR install (GH #631). The MariaDB
// database drops via the generic RequiresDB teardown; this clears files +
// the nginx snippet. OpenEMR extracts a large fixed tree at the install
// root; for a docroot install we rm -rf the known top-level entries so
// sibling files survive, and for a subdir install we rm -rf the subdir.

type openemrDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type openemrDeleteResp struct {
	Status string `json:"status"`
}

// openemrTopLevel lists the top-level entries the OpenEMR 8.2.0 tarball
// lays down (generated from the release). Entries absent on disk are
// skipped silently. sites/ holds sqlconf.php + PHI documents, so it goes.
var openemrTopLevel = []string{
	"acl_setup.php", "admin.php", "ccr", "contrib", "controller.php",
	"cores", "custom", "Documentation", "gacl", "index.php", "interface",
	"library", "modules", "myportal", "package.json", "patients",
	"phpfhirapi", "portal", "public", "sites", "sql", "src", "templates",
	"tests", "vendor", "version.php", "composer.json", "composer.lock",
	".htaccess", "setup.php", "sql_upgrade.php", "sql_patch.php",
	"ippf_upgrade.php", "acl_upgrade.php",
}

func openemrDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req openemrDeleteReq
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
	sub := strings.Trim(req.Subdirectory, "/")

	if sub != "" {
		_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath).Run()
	} else {
		for _, name := range openemrTopLevel {
			_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name)).Run()
		}
	}

	if sub == "" && req.Domain != "" {
		indexPath := filepath.Join(req.Docroot, "index.html")
		if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
			_ = writeDefaultIndex(ctx, indexPath, req.OSUser, req.Domain, req.Docroot, "")
		}
	}

	if req.Domain != "" {
		_ = removeOpenEMRNginx(ctx, req.Domain, sub)
	}

	return openemrDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("openemr", openemrDeleteHandler)
}
