package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type osticketDeleteReq struct {
	AppType string `json:"app_type"` // present, ignored
	OSUser  string `json:"os_user"`
	Docroot string `json:"docroot"`
	Domain  string `json:"domain,omitempty"`
}

type osticketDeleteResp struct {
	Status string `json:"status"`
}

// osticketTopLevel lists every entry osTicket lays down at the (flattened)
// webroot for v1.18.x. Used so a docroot delete removes osTicket's files
// without rm -rf-ing a docroot that might hold other content. setup/ is
// normally removed at install time; it's listed here in case a failed install
// left it behind. Entries that don't exist on disk are skipped.
var osticketTopLevel = []string{
	// files
	"account.php", "ajax.php", "avatar.php", "bootstrap.php", "captcha.php",
	"client.inc.php", "file.php", "index.php", "login.php", "logo.php",
	"logout.php", "main.inc.php", "manage.php", "offline.php", "open.php",
	"profile.php", "pwreset.php", "secure.inc.php", "tickets.php", "view.php",
	"web.config",
	// directories
	"api", "apps", "assets", "css", "images", "include", "js", "kb", "pages",
	"scp", "setup",
	// the install shim, if a failed install left it outside setup/
	"install-cli.php",
}

func osticketDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req osticketDeleteReq
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

	// Docroot-only (RootOnly): the install path IS the docroot.
	installPath, err := appInstallPath(req.Docroot, "")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	for _, name := range osticketTopLevel {
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", filepath.Join(installPath, name))
		_ = cmd.Run()
	}

	// Restore a branded placeholder so the bare docroot doesn't 403/404.
	if req.Domain != "" {
		indexPath := filepath.Join(req.Docroot, "index.html")
		if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
			_ = writeDefaultIndex(ctx, indexPath, req.OSUser, req.Domain, req.Docroot, "")
		}
		_ = removeOsTicketNginx(ctx, req.Domain)
	}

	return osticketDeleteResp{Status: "deleted"}, nil
}

func init() {
	RegisterAppDeleter("osticket", osticketDeleteHandler)
}
