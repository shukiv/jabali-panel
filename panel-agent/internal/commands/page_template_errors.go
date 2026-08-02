package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// errorPagesDir is the shared docroot the per-domain vhosts point their
// error_page directives at (location = /jabali-err-NNN.html { internal;
// root /var/www/jabali-errors; }). The pages are global — the page_template
// error bodies carry no per-domain placeholders — so one set serves every
// vhost.
// Vars (not consts) so tests can point them at a temp dir.
var errorPagesDir = "/var/www/jabali-errors"

// disabledPageDir / unconfiguredPageDir hold the single shared page each
// (index.html) served for administratively-disabled domains and — when the
// admin opts in — for hosts nginx has no server block for (GH #860).
var (
	disabledPageDir     = "/var/www/jabali-disabled"
	unconfiguredPageDir = "/var/www/jabali-unconfigured"
	// catchallConfPath is included inside `location / { }` of both
	// default-vhost server blocks (install.sh writes the include). Its body
	// is either `return 444;` (shipped default — drop unknown hosts without
	// a fingerprintable response) or the unconfigured-domain page.
	catchallConfPath = "/etc/nginx/jabali-catchall.conf"
)

// syncErrorPagesParams carries the branded page bodies the reconciler
// converges from the editable page_template rows. An empty body means
// "leave the existing file untouched" (the reconciler only sends bodies
// it has). UnconfiguredEnabled is a pointer so a panel that predates the
// catch-all toggle (field absent) leaves the catch-all conf alone.
type syncErrorPagesParams struct {
	Error404            string `json:"error_404"`
	Error403            string `json:"error_403"`
	Error500            string `json:"error_500"`
	DomainDisabled      string `json:"domain_disabled"`
	DomainUnconfigured  string `json:"domain_unconfigured"`
	UnconfiguredEnabled *bool  `json:"unconfigured_enabled"`
}

type syncErrorPagesResponse struct {
	Written         []string `json:"written"`
	CatchallChanged bool     `json:"catchall_changed"`
}

const catchallOnConf = `# Managed by jabali-agent (page_templates.sync_error_pages) — do not edit.
# Unconfigured-domain page is ENABLED (Server Settings): unknown hosts get
# the branded page from the editable domain_unconfigured template.
root /var/www/jabali-unconfigured;
try_files /index.html =404;
`

const catchallOffConf = `# Managed by jabali-agent (page_templates.sync_error_pages) — do not edit.
# Unconfigured-domain page is DISABLED (default): unknown hosts are dropped
# without a response, so scanners get nothing to fingerprint.
return 444;
`

// syncErrorPagesHandler writes the shared branded error pages to
// errorPagesDir. Files are root-owned 0644 (nginx reads them as www-data;
// they are internal-only, reachable solely via error_page). Idempotent —
// the reconciler gates the call behind a content hash, but writing the
// same bytes again is harmless. Atomic per file (tmp + rename) so nginx
// never reads a half-written page.
func syncErrorPagesHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p syncErrorPagesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err),
		}
	}

	files := []struct {
		dir     string
		name    string
		content string
	}{
		{errorPagesDir, "jabali-err-404.html", p.Error404},
		{errorPagesDir, "jabali-err-403.html", p.Error403},
		{errorPagesDir, "jabali-err-500.html", p.Error500},
		{disabledPageDir, "index.html", p.DomainDisabled},
		{unconfiguredPageDir, "index.html", p.DomainUnconfigured},
	}
	var written []string
	for _, f := range files {
		if f.content == "" {
			continue
		}
		if err := os.MkdirAll(f.dir, 0o755); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("mkdir %s: %v", f.dir, err),
			}
		}
		dst := filepath.Join(f.dir, f.name)
		if err := writeFileAtomic(dst, []byte(f.content), 0o644); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("write %s: %v", dst, err),
			}
		}
		written = append(written, dst)
	}

	catchallChanged, err := convergeCatchall(ctx, p.UnconfiguredEnabled)
	if err != nil {
		return nil, err
	}
	return syncErrorPagesResponse{Written: written, CatchallChanged: catchallChanged}, nil
}

// convergeCatchall writes the default-vhost catch-all include to match the
// unconfigured-page toggle and reloads nginx only when the file actually
// changed. A nil enabled (panel predates the toggle) is a no-op.
func convergeCatchall(ctx context.Context, enabled *bool) (bool, error) {
	if enabled == nil {
		return false, nil
	}
	desired := catchallOffConf
	if *enabled {
		desired = catchallOnConf
	}

	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	if existing, err := os.ReadFile(catchallConfPath); err == nil && string(existing) == desired {
		return false, nil
	}
	if err := writeFileAtomic(catchallConfPath, []byte(desired), 0o644); err != nil {
		return false, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", catchallConfPath, err),
		}
	}
	// The include only exists inside jabali-default.conf on boxes whose
	// install.sh has run the GH #860 revision; reloading is still correct
	// (and harmless) when it hasn't — the file just sits unreferenced.
	if err := nginxTestAndReload(ctx); err != nil {
		return true, err
	}
	return true, nil
}

// writeFileAtomic writes content to dst via a temp file + rename so a
// reader never observes a partial file.
func writeFileAtomic(dst string, content []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if the rename already moved it
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func init() {
	Default.Register("page_templates.sync_error_pages", syncErrorPagesHandler)
}
