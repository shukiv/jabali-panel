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
	// app root (moved up) — full top-level set of the pinned 2.4.2 zip:
	// artisan composer.json composer.lock .env.example LICENSE readme.md
	// SECURITY.md server.php version.md + the app dirs.
	"app", "bootstrap", "config", "database", "lang", "resources", "routes",
	"storage", "vendor", "artisan", "composer.json", "composer.lock", ".env",
	"readme.md", "version.md", ".env.example", "phpunit.xml",
	"LICENSE", "SECURITY.md", "server.php",
	// flattened public/ assets — must cover EVERY top-level entry of the
	// pinned zip's public/ (2.4.2: build, favicons, .htaccess, index.php,
	// robots.txt, web.config). A miss here leaves the dir behind and the
	// next install dies at the flatten step: `mv` refuses to overwrite a
	// non-empty leftover dir (GH #1042 — retry after a failed install
	// 404'd on `favicons`). "public" itself covers an install that died
	// MID-flatten (moved-up entries + a still-populated public/); a
	// completed install has no public/ left, so listing it is free.
	"index.php", ".htaccess", "favicon.ico", "robots.txt", "build", "web.config",
	"favicons", "public",
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
