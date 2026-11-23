package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// wordpress.upload_path_fix — a migrated WordPress whose upload_path option is
// an absolute path from the source server (cPanel stores an addon site's as
// /home/<u>/public_html/<addon>/wp-content/uploads) keeps writing media and
// Elementor CSS into a directory that does not exist here. Runs wp-cli AS THE
// OS USER (as wordpress.search_replace does), so it only reaches the database
// the site's own credentials reach — never root SQL against a name taken from
// a tenant-editable wp-config.php.
type wordpressUploadPathFixParams struct {
	OSUser string `json:"os_user"`
	Path   string `json:"path"` // the WordPress install dir, absolute, under the user's home
}

type wordpressUploadPathFixResult struct {
	Before string `json:"before"`
	Action string `json:"action"`           // unchanged | cleared | kept
	Reason string `json:"reason,omitempty"` // why a stale-looking value was kept
}

// uploadPathWP runs one wp-cli command as osUser and returns its stdout; a var
// so tests stub it.
var uploadPathWP = func(ctx context.Context, osUser string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := buildSystemdRunCmd(cctx, osUser, append([]string{"wp"}, args...)...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return "", fmt.Errorf("%v: %s", err, lastLine(string(ee.Stderr)))
	}
	return string(out), err
}

func wordpressUploadPathFixHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p wordpressUploadPathFixParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !looksLikeUnixUsername(p.OSUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user looks unsafe"}
	}
	home := "/home/" + p.OSUser
	if !filepath.IsAbs(p.Path) || strings.Contains(p.Path, "..") || !strings.HasPrefix(filepath.Clean(p.Path), home+"/") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "path must be an absolute WordPress dir under the user's home"}
	}
	dir := filepath.Clean(p.Path)
	common := []string{"--path=" + dir, "--skip-plugins", "--skip-themes", "--skip-packages"}

	out, err := uploadPathWP(ctx, p.OSUser, append([]string{"option", "get", "upload_path", "--format=json"}, common...)...)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "wp option get upload_path: " + err.Error()}
	}
	// PHP notices from an old site can precede the value; the JSON is the last line.
	var before string
	if jerr := json.Unmarshal([]byte(lastLine(out)), &before); jerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "unreadable upload_path: " + lastLine(out)}
	}
	action, reason := uploadPathAction(before, dir, home, uploadPathIsDir, uploadPathHoldsFiles)
	if action == "cleared" {
		if _, err := uploadPathWP(ctx, p.OSUser, append([]string{"option", "update", "upload_path", ""}, common...)...); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "wp option update upload_path: " + err.Error()}
		}
	}
	return wordpressUploadPathFixResult{Before: before, Action: action, Reason: reason}, nil
}

// uploadPathAction decides what to do with a site's upload_path. Only an
// absolute ".../wp-content/uploads" left over from the source layout is
// cleared — and only when the site's own wp-content/uploads exists, which is
// where the import put the files and what an empty upload_path means. A path
// inside one of the account's live docroots is another site's directory on
// this server (shared on purpose), and a stale directory that already holds
// files would be orphaned, so both are kept for the operator to look at.
func uploadPathAction(v, dir, home string, isDir, holdsFiles func(string) bool) (action, reason string) {
	if v == "" || !filepath.IsAbs(v) {
		return "unchanged", ""
	}
	cv := filepath.Clean(v)
	if cv == dir || strings.HasPrefix(cv, dir+"/") {
		return "unchanged", ""
	}
	if filepath.Base(cv) != "uploads" || filepath.Base(filepath.Dir(cv)) != "wp-content" {
		return "kept", "not a wp-content/uploads path"
	}
	if rel, ok := strings.CutPrefix(cv, home+"/domains/"); ok {
		if parts := strings.SplitN(rel, "/", 3); len(parts) >= 2 && parts[1] == "public_html" {
			return "kept", "another site's folder on this server"
		}
	}
	if holdsFiles(cv) {
		return "kept", "the old folder exists here and holds files"
	}
	if !isDir(filepath.Join(dir, "wp-content", "uploads")) {
		return "kept", "the site has no wp-content/uploads folder"
	}
	return "cleared", ""
}

// Filesystem probes, vars so the handler test needs no /home/<user>.
var (
	uploadPathIsDir      = isDirPath
	uploadPathHoldsFiles = dirHoldsFiles
)

func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// dirHoldsFiles reports whether p is a directory with at least one regular
// file under it. Bounded, and WalkDir does not follow symlinks.
func dirHoldsFiles(p string) bool {
	seen, found := 0, false
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if seen++; seen > 5000 {
			found = true // too big to walk: treat as holding files
			return fs.SkipAll
		}
		if d.Type().IsRegular() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func init() {
	Default.Register("wordpress.upload_path_fix", wordpressUploadPathFixHandler)
}
