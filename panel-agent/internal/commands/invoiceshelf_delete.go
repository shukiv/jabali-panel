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

// invoiceshelf_delete.go — removes an InvoiceShelf install (GH #631). The
// MariaDB database is dropped by the panel's generic DB teardown (the app
// is RequiresDB=true); this only clears files + the nginx hardening
// snippet. Same enumerate-for-docroot / rm-rf-for-subdir shape as Flarum.

type invoiceshelfDeleteReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type invoiceshelfDeleteResp struct {
	Status string `json:"status"`
}

// invoiceshelfTopLevel lists what a flattened InvoiceShelf install lays
// down at the webroot: the moved-up app dirs, the flattened public/ assets,
// and the jabali-written .env. Docroot installs enumerate so sibling files
// survive; subdir installs rm -rf the whole subdir.
var invoiceshelfTopLevel = []string{
	// app root (moved up)
	"app", "bootstrap", "config", "database", "lang", "resources", "routes",
	"storage", "vendor", "artisan", "composer.json", "composer.lock", ".env",
	"readme.md", "version.md", ".env.example", "phpunit.xml",
	// flattened public/ assets
	"index.php", ".htaccess", "favicon.ico", "robots.txt", "build", "web.config",
}

func invoiceshelfDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req invoiceshelfDeleteReq
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
		for _, name := range invoiceshelfTopLevel {
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
		_ = removeInvoiceShelfNginx(ctx, req.Domain, sub)
	}

	return invoiceshelfDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("invoiceshelf", invoiceshelfDeleteHandler)
}
