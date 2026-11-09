package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// wordpressDeleteReq is the input shape for wordpress.delete.
type wordpressDeleteReq struct {
	AppType string `json:"app_type"` // present, ignored
	OSUser  string `json:"os_user"`  // domain owner (e.g. "shuki")
	Docroot string `json:"docroot"`  // /home/shuki/domains/example.com/public_html
	// Subdirectory pins the install root under the docroot for subdir
	// installs (example.com/blog). Empty = the install owns the docroot.
	// Without this the deleter rm'd docroot/wp-admin etc., never
	// docroot/<subdir>/wp-admin — so a subdir WP "delete" removed the
	// panel row but left every file on disk (the site stayed live).
	Subdirectory string `json:"subdirectory,omitempty"`
	// Domain is used to render the placeholder index.html after the
	// WP files are removed (matches what domain.create writes on a
	// fresh domain). Optional — if empty, the placeholder restore is
	// skipped and the docroot is left empty (nginx will 403).
	Domain string `json:"domain,omitempty"`
}

// wordpressDeleteResp is the output shape for wordpress.delete.
type wordpressDeleteResp struct {
	Status string `json:"status"` // "deleted"
}

func wordpressDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req wordpressDeleteReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate required fields
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "os_user is required",
		}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "docroot is required",
		}
	}

	// Validate docroot is within /home/<osuser>/domains/
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid docroot: %v", err),
		}
	}

	// Subdir install (example.com/blog): the subdirectory is dedicated to
	// this WordPress, so remove it wholesale — mirrors every other app
	// deleter (drupal/joomla/itflow/…). The selective wp-* removal below
	// only exists to spare sibling files in a SHARED docroot; a subdir has
	// none, and rm -rf catches wp-config.php + uploads + everything. The
	// per-domain xmlrpc snippet and index.html restore are docroot-only
	// (a sibling root install on the same domain may still rely on them).
	if sub := strings.Trim(req.Subdirectory, "/"); sub != "" {
		// Defense-in-depth: the subdir is validated at create, but never
		// trust it. appInstallPath keeps it inside the docroot and refuses
		// one that names the docroot itself.
		installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
		}
		cmd := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", installPath)
		_ = cmd.Run()
		return wordpressDeleteResp{Status: "deleted"}, nil
	}

	// Targets to remove from the docroot. wp-*.php is glob-expanded
	// Go-side because systemd-run invokes rm with argv (no shell) — a
	// literal "wp-*.php" silently no-ops, leaving wp-config.php (with
	// the DB password) on disk after a "delete". Standard wp directories
	// + non-wp WordPress files are listed explicitly.
	wpDirs := []string{
		"wp-admin",
		"wp-content",
		"wp-includes",
		"readme.html",
		"license.txt",
		"index.php",
		// xmlrpc.php ships with WP but doesn't start with "wp-" so the
		// wp-*.php glob below skips it. Listed explicitly.
		"xmlrpc.php",
	}

	targets := make([]string, 0, len(wpDirs)+16)
	for _, name := range wpDirs {
		targets = append(targets, filepath.Join(req.Docroot, name))
	}
	if matches, err := filepath.Glob(filepath.Join(req.Docroot, "wp-*.php")); err == nil {
		// Defense-in-depth: every match must be inside the validated
		// docroot. Glob doesn't follow symlinks but a malicious entry
		// could still be a path outside the tree on a buggy FS.
		for _, m := range matches {
			if filepath.Dir(m) == req.Docroot {
				targets = append(targets, m)
			}
		}
	}

	// Build and run the delete command under systemd-run as the OS user.
	// Best-effort: a missing file is not an error (rm -f), and one failed
	// delete shouldn't block the rest.
	for _, fullPath := range targets {
		cmd := buildSystemdRunCmd(ctx,
			req.OSUser,
			"rm", "-rf", fullPath,
		)
		_ = cmd.Run()
	}

	// Remove the xmlrpc.php nginx deny snippet written at install time.
	// Best-effort — a stale snippet causes no harm (it only denies xmlrpc).
	_ = removeWordPressXmlrpcBlock(ctx, req.Domain)

	// Restore the domain.create placeholder index.html so the docroot
	// doesn't 403 after delete. Only if no index.html exists — don't
	// clobber a user-uploaded one. Domain is optional in the request;
	// without it the template can't be rendered, so we skip.

	return wordpressDeleteResp{
		Status: "deleted",
	}, nil
}

func init() {
	Default.Register("wordpress.delete", wordpressDeleteHandler)
	RegisterAppDeleter("wordpress", wordpressDeleteHandler)
}
