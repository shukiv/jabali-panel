package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newSecurityCmd groups security-response commands.
func newSecurityCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "security",
		Short: "Security advisories for this server",
	}
	c.AddCommand(newSecurityFeedCheckCmd())
	return c
}

func newSecurityFeedCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "feed-check",
		Short: "Act on new security advisories now (jabali-security-feed.timer runs this hourly)",
		Long: `Reads the Jabali security advisory feed and, for each advisory this server has
not handled yet, takes the actions it asks for: install pending security
updates now (when OS security auto-updates are on) and start the panel update
now, even where panel self-update is off. Both happen only where security
advisories apply automatically (the Updates page; on by default). It alerts
the admins with what was done and the command for anything that was not.
Each action runs at most once per advisory.
See docs/runbooks/security-advisory.md.`,
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			repoDir := os.Getenv("JABALI_REPO_DIR")
			if repoDir == "" {
				repoDir = defaultRepoDir
			}
			serviceUser := os.Getenv("JABALI_SERVICE_USER")
			if serviceUser == "" {
				serviceUser = "jabali"
			}
			feedURL := os.Getenv("JABALI_SECURITY_FEED_URL")
			if feedURL == "" {
				feedURL = defaultSecurityFeedURL
			}
			statePath := os.Getenv("JABALI_SECURITY_FEED_STATE")
			if statePath == "" {
				statePath = securityFeedStatePath
			}
			logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
			r := securityFeedRun{
				FeedURL:   feedURL,
				StatePath: statePath,
				PubKey:    releaseSigningPublicKey,
				Fetch:     fetchSecurityFeed,
				Host: func(ctx context.Context, signed bool) (securityFeedHost, error) {
					return liveSecurityFeedHost(ctx, repoDir, serviceUser, statePath, signed, logf), nil
				},
				Publish: func(ctx context.Context, env notifications.Envelope) error {
					if err := requireRedis(cmd, nil); err != nil {
						return err
					}
					_, err := notifications.NewQueue(sharedRedis).Publish(ctx, env)
					return err
				},
				Log: logf,
			}
			return r.run(ctx)
		},
	}
}

// fetchSecurityFeed GETs the feed or its signature, capped at
// securityFeedMaxBytes.
func fetchSecurityFeed(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, securityFeedMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > securityFeedMaxBytes {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, securityFeedMaxBytes)
	}
	return b, nil
}

// liveSecurityFeedHost reads this server's settings and wires the actions.
// It refreshes main and the stable tag first, so fixed_in commits and the
// stable release are known locally; a failed fetch leaves what is there.
func liveSecurityFeedHost(ctx context.Context, repoDir, serviceUser, statePath string, signed bool, logf func(string, ...any)) securityFeedHost {
	git := func(args ...string) (string, error) {
		full := append([]string{"-u", serviceUser, "-H", "env", "HOME=" + repoDir, "PATH=/usr/bin:/bin", "GIT_NO_REPLACE_OBJECTS=1",
			"git", "-C", repoDir}, args...)
		out, err := exec.CommandContext(ctx, "sudo", full...).CombinedOutput()
		return string(out), err
	}
	if out, err := git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main"); err != nil {
		logf("security feed: git fetch main failed (%v): %s", err, strings.TrimSpace(out))
	}
	if out, err := git("fetch", "-q", "--force", "origin", "+refs/tags/stable:refs/tags/stable"); err != nil {
		logf("security feed: git fetch stable failed (%v): %s", err, strings.TrimSpace(out))
	}

	h := securityFeedHost{
		Now:    time.Now,
		Signed: signed,
		Git:    git,
		// The Updates page defaults: OS security updates on (JAB-353) and
		// security advisories applied automatically (000332).
		AptEnabled:      true,
		SecurityEnabled: true,
		StartApt: func() error {
			// Not apt-daily-upgrade.service: its daily stamp makes a second
			// start on the same day a no-op, and it does not refresh the
			// package lists. apt-refresh reports the result to the Updates
			// page, as the timer's run does.
			return runQuiet(ctx, "systemd-run", "--unit=jabali-security-feed-apt", "--collect", "--no-block",
				"--property=TimeoutStartSec=1h", "/bin/sh", "-c",
				"apt-get update -qq -o DPkg::Lock::Timeout=300; unattended-upgrade -v; /usr/local/bin/jabali update apt-refresh")
		},
		StartUpdate: func() error {
			// The agent writes jabali-autoupdate.service whatever the
			// self-update setting (only its timer follows it). Starting the
			// same unit keeps an advisory's update and the nightly one from
			// running at once. Where the agent has not written it yet, run
			// the same command as a transient unit of that name.
			if _, err := os.Stat("/etc/systemd/system/jabali-autoupdate.service"); err == nil {
				return runQuiet(ctx, "systemctl", "start", "--no-block", "jabali-autoupdate.service")
			}
			return runQuiet(ctx, "systemd-run", "--unit=jabali-autoupdate", "--collect", "--no-block",
				"--property=TimeoutStartSec=2h", "/usr/local/bin/jabali", "update", "-f")
		},
		StartConverger: func(adv securityAdvisory) error {
			return startSecurityConverger(ctx, repoDir, serviceUser, filepath.Dir(statePath), adv)
		},
	}
	cfg, err := repository.NewUpdateAutoupdateConfigRepository(sharedDB).Get(ctx)
	switch {
	case err == nil:
		h.AptEnabled, h.SecurityEnabled = cfg.AptEnabled, cfg.SecurityEnabled
	case errors.Is(err, repository.ErrNotFound):
	default:
		h.SecurityEnabled = false
		logf("security feed: read the auto-update settings failed (%v); only alerting", err)
	}
	if ch, err := releaseChannel(); err == nil {
		h.OnStable = ch == "stable"
	}
	if h.OnStable {
		// The schema version `jabali update` checks the stable release against.
		if st, err := db.State(sharedCfg.Database.URL); err == nil {
			h.LiveSchema = st.Version
		} else {
			logf("security feed: read the database schema version failed (%v)", err)
		}
	}
	return h
}

// securityConvergerScript runs one install.sh function from the extracted
// tree next to it. install.sh resets REPO_DIR when sourced, so it is set after.
const securityConvergerScript = `#!/bin/bash
# Written by jabali security feed-check: applies one install.sh converger from
# a newer commit, for a security advisory, on a server ahead of stable.
set -u
cd "$(dirname "$0")/tree" || exit 1
fn=$1
# shellcheck disable=SC1091
source ./install.sh >/dev/null 2>&1
REPO_DIR=$PWD
declare -F "$fn" >/dev/null || { echo "install.sh has no function $fn" >&2; exit 3; }
"$fn"
`

// startSecurityConverger extracts the converger commit's tree and runs the
// converger in a transient unit, so a long run is not tied to this command.
func startSecurityConverger(ctx context.Context, repoDir, serviceUser, stateDir string, adv securityAdvisory) error {
	name := strings.ToLower(adv.ID)
	dir := filepath.Join(stateDir, name)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		return err
	}
	// git writes the archive to stdout as the service user; this process
	// (root) keeps it, so the tree it runs from is root's.
	tarPath := filepath.Join(dir, "tree.tar")
	f, err := os.OpenFile(tarPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var stderr strings.Builder
	archive := exec.CommandContext(ctx, "sudo", "-u", serviceUser, "-H", "env", "HOME="+repoDir, "PATH=/usr/bin:/bin", "GIT_NO_REPLACE_OBJECTS=1",
		"git", "-C", repoDir, "archive", "--format=tar", adv.Jabali.ConvergerCommit)
	archive.Stdout, archive.Stderr = f, &stderr
	err = archive.Run()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("git archive %s: %v: %s", adv.Jabali.ConvergerCommit[:9], err, strings.TrimSpace(stderr.String()))
	}
	if err := runQuiet(ctx, "tar", "-x", "-f", tarPath, "-C", tree); err != nil {
		return err
	}
	_ = os.Remove(tarPath)
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte(securityConvergerScript), 0o700); err != nil {
		return err
	}
	return runQuiet(ctx, "systemd-run", "--unit=jabali-security-converger-"+name, "--collect", "--no-block",
		"--property=TimeoutStartSec=1h", "/bin/bash", script, adv.Jabali.Converger)
}

// runQuiet runs a command and folds its output into the error.
func runQuiet(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
