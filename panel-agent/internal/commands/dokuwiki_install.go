package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"encoding/json"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dokuwiki_install.go — installs DokuWiki, a flat-file wiki (no database,
// GH #210). Unlike the DB apps, there's no CLI installer: DokuWiki ships
// a web-only install.php wizard. We do it headlessly instead — download +
// extract the release, write conf/local.php + conf/users.auth.php +
// conf/acl.auth.php directly, then delete install.php so the wizard can't
// be re-run against the live wiki.
//
// Files are written as the OS user via `systemd-run … tee` (buildSystemdRunCmd
// runs with --pipe so stdin flows through) so DokuWiki's data/ tree stays
// owned by the PHP-FPM pool user.

const (
	// dokuwikiVersion is the pinned upstream release. Bump alongside the
	// SHA256 when moving to a newer "stable" release from
	// https://download.dokuwiki.org/.
	dokuwikiVersion     = "2024-02-06b"
	dokuwikiTarballURL  = "https://download.dokuwiki.org/src/dokuwiki/dokuwiki-" + dokuwikiVersion + ".tgz"
	dokuwikiTarballSHA  = "7ac919bc298c049af15764f3563ec3012cd158945ef2a22348684df701a19ba3"
)

// dokuwikiUsernamePattern — DokuWiki logins are lowercased; the API
// generates a 6-letter username. Keep it conservative.
var dokuwikiUsernamePattern = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)

type dokuwikiInstallReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"` // unused — DokuWiki auto-detects base URL
	SiteTitle    string `json:"site_title"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
	UseWWW       bool   `json:"use_www"`
}

type dokuwikiInstallResp struct {
	Version string `json:"version"`
}

func dokuwikiInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req dokuwikiInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	}
	if req.SiteTitle == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "site_title is required"}
	}
	req.AdminUser = strings.ToLower(strings.TrimSpace(req.AdminUser))
	if !dokuwikiUsernamePattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_user must be 2-32 lowercase letters, digits or underscore"}
	}
	if req.AdminPass == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	// admin_email is interpolated into the colon-separated conf/users.auth.php
	// record (user:hash:name:email:groups). A ':' or CR/LF would inject extra
	// fields or a second auth line (rogue admin), so reject them at the agent
	// boundary regardless of panel-side validation (the agent runs as root).
	if len(req.AdminEmail) > 254 || strings.ContainsAny(req.AdminEmail, ":\r\n") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email contains illegal characters"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	if req.Subdirectory != "" {
		mk := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mk, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (%s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("dokuwiki-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarball := filepath.Join(tmpDir, "dokuwiki.tgz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := dokuwikiDownload(dlCtx, tarball); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarball, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}

	// Extract as the OS user (strip the dokuwiki-<ver>/ top dir).
	extract := buildSystemdRunCmd(ctx, req.OSUser,
		"tar", "--extract", "--gzip", "--strip-components=1",
		"--file", tarball, "--directory", installPath)
	if out, err := runBoundedOutput(extract, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("tar extract: %v (%s)", err, truncateStr(string(out), 512))}
	}

	// Headless config.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.AdminPass), bcrypt.DefaultCost)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("hash admin pass: %v", err)}
	}
	// DokuWiki's bcrypt verifier (PHP crypt) accepts $2a$, but its own
	// hashes use $2y$; normalise so the format matches what DokuWiki writes.
	pwHash := strings.Replace(string(hash), "$2a$", "$2y$", 1)

	localPHP := "<?php\n" +
		"$conf['title']     = " + phpSingleQuoted(req.SiteTitle) + ";\n" +
		"$conf['lang']      = 'en';\n" +
		"$conf['license']   = 'cc-by-sa';\n" +
		"$conf['useacl']    = 1;\n" +
		"$conf['superuser'] = '@admin';\n" +
		"$conf['passcrypt'] = 'bcrypt';\n"

	// login:passwordhash:Real Name:e-mail:groups
	usersAuth := "# <?php exit()?>\n" +
		"# Managed by jabali — initial admin (GH #210).\n" +
		strings.Join([]string{
			req.AdminUser, pwHash, "Administrator", req.AdminEmail, "admin,user",
		}, ":") + "\n"

	// Public read, members edit/upload; admin via @admin superuser.
	aclAuth := "# acl.auth.php — managed by jabali\n" +
		"*\t\t@ALL\t1\n" +
		"*\t\t@user\t8\n"

	for _, f := range []struct {
		rel, content, mode string
	}{
		{"conf/local.php", localPHP, "0640"},
		{"conf/users.auth.php", usersAuth, "0640"},
		{"conf/acl.auth.php", aclAuth, "0640"},
	} {
		if err := dokuwikiWriteUserFile(ctx, req.OSUser, filepath.Join(installPath, f.rel), f.content, f.mode); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}

	// Disable the web installer.
	rm := buildSystemdRunCmd(ctx, req.OSUser, "rm", "-f", filepath.Join(installPath, "install.php"))
	if out, err := runBoundedOutput(rm, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove install.php: %v (%s)", err, truncateStr(string(out), 256))}
	}

	return dokuwikiInstallResp{Version: dokuwikiVersion}, nil
}

// dokuwikiWriteUserFile writes content to path as the OS user (via tee
// through the transient unit's --pipe stdin) and chmods it.
func dokuwikiWriteUserFile(ctx context.Context, osUser, path, content, mode string) error {
	tee := buildSystemdRunCmd(ctx, osUser, "tee", path)
	tee.Stdin = strings.NewReader(content)
	if out, err := runBoundedOutput(tee, 0); err != nil {
		return fmt.Errorf("write %s: %w (%s)", path, err, truncateStr(string(out), 256))
	}
	chmod := buildSystemdRunCmd(ctx, osUser, "chmod", mode, path)
	if out, err := runBoundedOutput(chmod, 0); err != nil {
		return fmt.Errorf("chmod %s: %w (%s)", path, err, truncateStr(string(out), 256))
	}
	return nil
}

// phpSingleQuoted renders s as a PHP single-quoted string literal,
// escaping backslashes and single quotes (the only two that matter in
// PHP single quotes).
func phpSingleQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

func dokuwikiDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dokuwikiTarballURL, nil)
	if err != nil {
		return fmt.Errorf("dokuwiki download: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("dokuwiki download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dokuwiki download: HTTP %d from %s", resp.StatusCode, dokuwikiTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("dokuwiki download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("dokuwiki download: copy: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != dokuwikiTarballSHA {
		return fmt.Errorf("dokuwiki download: SHA256 mismatch: got %s want %s", got, dokuwikiTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("dokuwiki", dokuwikiInstallHandler)
}
