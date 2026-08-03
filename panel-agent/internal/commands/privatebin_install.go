package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// privatebin_install.go — installs PrivateBin, a zero-knowledge encrypted
// pastebin (GH #631). No database, no user accounts: the release tarball is
// self-contained (vendor/ bundled), so install = verified download + extract
// + one INI config.
//
// The one security-relevant decision: the Filesystem model's data directory
// lives OUTSIDE the docroot (sibling of public_html), so encrypted pastes,
// the traffic limiter state and the purge queue are never directly
// fetchable through nginx. PrivateBin's shipped .htaccess protections are
// Apache-only and do nothing under our nginx vhosts.
//
// Upstream pins: the GitHub source tag archive (PrivateBin bundles vendor/
// in the repo), verified by SHA-256 before extract, per the catalog's
// no-floating-latest bar (GH #631 discussion).

const (
	// privatebinVersion is the pinned upstream release. Bump alongside the
	// SHA256 (recompute from the tag archive) when moving to a newer release.
	privatebinVersion    = "2.0.5"
	privatebinTarballURL = "https://github.com/PrivateBin/PrivateBin/archive/refs/tags/" + privatebinVersion + ".tar.gz"
	privatebinTarballSHA = "19903f421eaa1bd3e29e33706dddd41ffb0f77e03f2ad7ff9da15a5e734c957b"
)

type privatebinInstallReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"` // unused — PrivateBin auto-detects
	SiteTitle    string `json:"site_title"`
	UseWWW       bool   `json:"use_www"`
}

type privatebinInstallResp struct {
	Version string `json:"version"`
	DataDir string `json:"data_dir"`
}

// privatebinDataDir places the paste store OUTSIDE the docroot: a sibling
// of the docroot's last element, suffixed with the subdirectory for
// subdir installs so two instances under one domain can't collide.
// e.g. docroot /home/u/domains/d/public_html, subdir ""      -> /home/u/domains/d/privatebin-data
//      docroot /home/u/domains/d/public_html, subdir "paste" -> /home/u/domains/d/privatebin-data-paste
func privatebinDataDir(docroot, subdirectory string) string {
	base := filepath.Join(filepath.Dir(filepath.Clean(docroot)), "privatebin-data")
	if subdirectory != "" {
		base += "-" + strings.ReplaceAll(subdirectory, "/", "-")
	}
	return base
}

func privatebinInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req privatebinInstallReq
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
		req.SiteTitle = "PrivateBin"
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	dataDir := privatebinDataDir(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		mk := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mk, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (%s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	removePlaceholderIndex(ctx, installPath)

	// Paste store outside the docroot, owned by the OS user, 0700 — only
	// the PHP-FPM pool (running as the user) needs it.
	mkData := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", "-m", "0700", dataDir)
	if out, err := runBoundedOutput(mkData, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir data dir %s: %v (%s)", dataDir, err, truncateStr(string(out), 256))}
	}

	tmpDir, err := stagingMkdirTemp("privatebin-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarball := filepath.Join(tmpDir, "privatebin.tar.gz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := privatebinDownload(dlCtx, tarball); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarball, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}

	// Extract as the OS user (strip the PrivateBin-<ver>/ top dir).
	extract := buildSystemdRunCmd(ctx, req.OSUser,
		"tar", "--extract", "--gzip", "--strip-components=1",
		"--file", tarball, "--directory", installPath)
	if out, err := runBoundedOutput(extract, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("tar extract: %v (%s)", err, truncateStr(string(out), 512))}
	}

	// cfg/conf.php — INI with the upstream sample's PHP exec guard on line
	// one (a direct browser hit gets a 403 instead of the file body).
	conf := ";<?php http_response_code(403); /*\n" +
		"; Managed by jabali (GH #631). Full option reference: cfg/conf.sample.php\n" +
		"[main]\n" +
		"name = " + privatebinINIQuoted(req.SiteTitle) + "\n" +
		"[model]\n" +
		"class = Filesystem\n" +
		"[model_options]\n" +
		"dir = " + privatebinINIQuoted(dataDir) + "\n"
	if err := dokuwikiWriteUserFile(ctx, req.OSUser, filepath.Join(installPath, "cfg", "conf.php"), conf, "0640"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return privatebinInstallResp{Version: privatebinVersion, DataDir: dataDir}, nil
}

// privatebinINIQuoted renders s as a double-quoted INI value. PHP's
// parse_ini_file has no in-string escape for '"', so strip it (titles and
// paths never legitimately carry one) along with newlines.
func privatebinINIQuoted(s string) string {
	s = strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(s)
	return "\"" + s + "\""
}

func privatebinDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, privatebinTarballURL, nil)
	if err != nil {
		return fmt.Errorf("privatebin download: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("privatebin download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("privatebin download: HTTP %d from %s", resp.StatusCode, privatebinTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("privatebin download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("privatebin download: copy: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != privatebinTarballSHA {
		return fmt.Errorf("privatebin download: SHA256 mismatch: got %s want %s", got, privatebinTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("privatebin", privatebinInstallHandler)
}
