package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
)

// Paths match install.sh defaults. The binary can't replace itself while
// running, so we build to a temp path, install, then trigger a restart.
const (
	defaultRepoDir      = "/opt/jabali-panel"
	defaultPanelBinPath = "/usr/local/bin/jabali-panel"
	defaultAgentBinPath = "/usr/local/bin/jabali-agent"
	defaultGoRoot       = "/usr/local/go"

	// lastBuiltSHAPath is where the SHA of the last fully-rebuilt
	// commit is persisted. Compared against HEAD on each update to
	// decide whether to run the build+restart chain. See runUpdate
	// for the self-heal rationale (why we don't compare pre==post).
	lastBuiltSHAPath = "/var/lib/jabali-panel/last-built-sha"
)

func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Pull latest code, rebuild, migrate, and restart services",
		Long: `Performs a self-update:
  1. git fetch + hard-reset to origin/main. Local tracked-file drift is
     discarded (the VM is a deployment target — origin is authoritative);
     untracked files like node_modules, .env, bin/, .cache/ are preserved.
     Any discarded drift is printed as a diffstat so it's visible; recover
     via git reflog if needed.
  2. If HEAD differs from /var/lib/jabali-panel/last-built-sha: npm ci,
     vite build, go build (panel-api + panel-agent), install new binaries,
     run pending migrations, restart services — then write HEAD back to
     last-built-sha. A partial-build failure leaves the file stale, so
     the next update retries automatically (self-heal).
  3. If HEAD matches last-built-sha: print "Already up to date" and exit.
     Use --force (-f) to run the rebuild + restart cycle anyway.`,
		// SilenceUsage so a runtime failure (apt 404, ENOTEMPTY race,
		// dirty migration) doesn't trigger cobra's full usage dump on
		// the operator's terminal — they want the error and a next
		// move, not the help text.
		SilenceUsage: true,
		// Args: NoArgs (QA 2026-06-22, CRITICAL): without this, cobra accepts
		// a stray positional (`jabali update status`) and RunE runs the real
		// updater anyway — a mistyped/guessed subcommand triggered a live
		// self-update during a read-only QA pass. Reject extra args outright.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runUpdate(cmd, args)
			if err != nil {
				// Surface the repair hint directly under the failing
				// step. The error itself is printed by cobra after
				// RunE returns; the hint goes to stderr first so it's
				// adjacent to the error in the operator's scrollback.
				fmt.Fprint(os.Stderr, repairHint())
			}
			return err
		},
	}
	cmd.Flags().BoolP("force", "f", false,
		"Run the full rebuild/restart cycle even when git pull found no new commits")
	cmd.Flags().Bool("from-source", false,
		"Build binaries on this host instead of downloading the release tarball from Gitea Releases. Default is to download the tarball (90s update vs 5-10min source build). Use --from-source when offline, on a private fork, or to test uncommitted changes.")
	return cmd
}

func runUpdate(cmd *cobra.Command, args []string) (retErr error) {
	// Must run as root to install binaries and restart services.
	if os.Geteuid() != 0 {
		return fmt.Errorf("jabali update must run as root (try: sudo jabali-panel update)")
	}

	// Record this run in the panel's update history so scheduled
	// (jabali-autoupdate.timer) and manual-CLI updates appear in Recent
	// Update History, not just panel-triggered ones (GH #300 follow-up).
	// Best-effort: never affects the update. Skipped when invoked by the
	// panel (which already logged the run via the API).
	finishHistory := beginUpdateHistory()
	defer func() { finishHistory(retErr) }()

	repoDir := os.Getenv("JABALI_REPO_DIR")
	if repoDir == "" {
		repoDir = defaultRepoDir
	}

	goRoot := os.Getenv("JABALI_GO_ROOT")
	if goRoot == "" {
		goRoot = defaultGoRoot
	}
	goBin := goRoot + "/bin/go"

	// The repo is owned by the jabali service user. Run git/npm/go as
	// that user to avoid git's "dubious ownership" check and to keep
	// node_modules/go-cache owned correctly.
	serviceUser := os.Getenv("JABALI_SERVICE_USER")
	if serviceUser == "" {
		serviceUser = "jabali"
	}

	// GH #760: on a low-RAM host, prepend GOFLAGS=-p=1 + GOMEMLIMIT so the
	// from-source go build can't OOM the box (mirrors install.sh:build_backend).
	// The vars are Go-specific, so passing them to git/npm too is harmless.
	lowMemGoEnv := lowRAMGoBuildEnv()
	asUser := func(dir string, name string, args ...string) error {
		allArgs := []string{"-u", serviceUser, "-H", "env",
			"HOME=" + repoDir,
			"PATH=" + goRoot + "/bin:/usr/bin:/bin",
			"GOCACHE=" + repoDir + "/.cache/go-build",
			"GOMODCACHE=" + repoDir + "/.cache/go-mod",
		}
		allArgs = append(allArgs, lowMemGoEnv...)
		allArgs = append(allArgs, name)
		allArgs = append(allArgs, args...)
		return run(dir, "sudo", allArgs...)
	}
	// asUserOut is asUser for the one case that has to inspect what git said
	// rather than only whether it failed (JAB-210). `run` streams straight to
	// the terminal, which is right for build output but leaves the caller
	// nothing to classify.
	asUserOut := func(dir string, name string, args ...string) (string, error) {
		allArgs := []string{"-u", serviceUser, "-H", "env",
			"HOME=" + repoDir,
			"PATH=" + goRoot + "/bin:/usr/bin:/bin",
			"GOCACHE=" + repoDir + "/.cache/go-build",
			"GOMODCACHE=" + repoDir + "/.cache/go-mod",
		}
		allArgs = append(allArgs, lowMemGoEnv...)
		allArgs = append(allArgs, name)
		allArgs = append(allArgs, args...)
		c := exec.Command("sudo", allArgs...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		return string(out), err
	}

	force, _ := cmd.Flags().GetBool("force")
	fromSource, _ := cmd.Flags().GetBool("from-source")
	// Set to true by the release-tarball step when binaries were
	// downloaded + installed from Gitea Releases. Each downstream
	// build step (npm ci, vite, go build, install binaries) skips
	// itself when this is set. Source-build is the fallback when
	// fromSource=true, no release published yet, or release fetch
	// failed.
	var installedFromRelease bool
	var releaseSHA string
	_ = releaseSHA // currently unused; reserved for future logging

	// Byte copies of the binaries as they were before this update replaced
	// them, so a failed `migrate up` can put them back (JAB-210). Taken by
	// whichever install path runs — release tarball or source build — and
	// dropped once migrations succeed. Deferred cleanup covers the updates
	// that fail at some step in between and never reach the migrate step.
	var preUpdateBinaries *binarySnapshot
	defer func() { preUpdateBinaries.cleanup() }()
	snapshotBeforeSwap := func() {
		if preUpdateBinaries != nil {
			return // already captured by an earlier step in this run
		}
		snap, err := snapshotBinaries(managedBinaries)
		if err != nil {
			// Not fatal: losing the ability to roll back is worse than not
			// updating, but not worse than leaving the host on old code with
			// no explanation. Say so plainly and continue.
			fmt.Printf("  (could not snapshot the current binaries for rollback: %v — "+
				"a failed migration will NOT be able to restore them)\n", err)
			return
		}
		preUpdateBinaries = snap
	}

	// gitHead captures HEAD as a string. Runs via `sudo -u <serviceUser>`
	// because the repo is owned by the jabali user; git 2.35+ refuses to
	// operate on a repo owned by a different uid ("fatal: detected
	// dubious ownership"), which surfaces as exit 128.
	var postHEAD string
	gitHead := func() (string, error) {
		c := exec.Command("sudo", "-u", serviceUser,
			"git", "-C", repoDir, "rev-parse", "HEAD")
		out, err := c.Output()
		if err != nil {
			return "", fmt.Errorf("git rev-parse HEAD: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	// readLastBuiltSHA returns the SHA written after the last fully-
	// successful rebuild, or "" if the file doesn't exist (fresh install,
	// or a previous update failed before it could write). An IO error
	// other than "not exists" returns the error — better to bail out
	// than silently rebuild a quiescent host.
	readLastBuiltSHA := func() (string, error) {
		b, err := os.ReadFile(lastBuiltSHAPath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", fmt.Errorf("read %s: %w", lastBuiltSHAPath, err)
		}
		return strings.TrimSpace(string(b)), nil
	}

	// ---- per-artifact skip helpers --------------------------------------
	//
	// HEAD-level diff is too coarse: every README tweak re-runs npm ci,
	// vite, and both go builds. Per-artifact tracker hashes each
	// step's real input set against /var/lib/jabali-panel/last-built-
	// <name>; skip the step when inputs AND the produced artifact are
	// unchanged. --force bypasses every per-artifact skip.
	//
	// gitPathSHA returns the blob/tree SHA git already keeps for a
	// path under HEAD (free — no extra hashing). compositeSHA folds
	// many such shas into a single deterministic fingerprint.
	gitPathSHA := func(path string) (string, error) {
		out, err := exec.Command("sudo", "-u", serviceUser,
			"git", "-C", repoDir, "rev-parse", "HEAD:"+path).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	// fileContentSHA falls back to a sha256 of raw file bytes —
	// used when a path lives outside HEAD (gitignored generated
	// artifacts like panel-ui/dist/index.html). Returns "<missing>"
	// only when the file truly isn't on disk.
	fileContentSHA := func(path string) string {
		full := filepath.Join(repoDir, path)
		b, err := os.ReadFile(full)
		if err != nil {
			return "<missing>"
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	compositeSHA := func(paths ...string) string {
		lines := make([]string, 0, len(paths))
		for _, p := range paths {
			// Prefer git's blob SHA when the path is tracked (free, no
			// extra I/O). Fall back to a content hash for gitignored
			// build outputs (panel-ui/dist/index.html) — without it,
			// those paths always resolve to "<missing>" and the
			// composite never changes from rebuilds of generated files.
			// PR #248 added panel-ui/dist/index.html to apiInputs to
			// trigger a panel-api rebuild on every UI change, but the
			// gitPathSHA-only impl meant the SHA was always "<missing>"
			// — UI rebuilds never bumped apiInputs and the binary never
			// re-embedded the new dist (observed on testserver 2026-
			// 06-07: binary stuck at #250 through three PRs of UI-only
			// changes).
			sha, err := gitPathSHA(p)
			if err != nil {
				sha = fileContentSHA(p)
			}
			lines = append(lines, p+":"+sha)
		}
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		return hex.EncodeToString(sum[:])
	}
	artifactSHAPath := func(name string) string {
		return filepath.Join("/var/lib/jabali-panel", "last-built-"+name)
	}
	// artifactUnchanged returns true if --force is OFF, the prior sha
	// matches, and the produced artifact still exists. Missing target
	// always forces rebuild — covers post-uninstall + repair paths.
	artifactUnchanged := func(name, want, targetFile string) bool {
		if force {
			return false
		}
		if targetFile != "" {
			if _, err := os.Stat(targetFile); err != nil {
				return false
			}
		}
		b, err := os.ReadFile(artifactSHAPath(name))
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(b)) == want
	}
	writeArtifactSHA := func(name, sha string) {
		_ = os.MkdirAll("/var/lib/jabali-panel", 0o755)
		tmp := artifactSHAPath(name) + ".tmp"
		if err := os.WriteFile(tmp, []byte(sha+"\n"), 0o644); err == nil {
			_ = os.Rename(tmp, artifactSHAPath(name))
		}
	}

	// writeLastBuiltSHA persists the given SHA atomically (temp + rename)
	// so a crash mid-write can't leave a corrupt file. Called only after
	// the full build+restart chain succeeds.
	writeLastBuiltSHA := func(sha string) error {
		if err := os.MkdirAll("/var/lib/jabali-panel", 0o755); err != nil {
			return fmt.Errorf("mkdir /var/lib/jabali-panel: %w", err)
		}
		tmp := lastBuiltSHAPath + ".tmp"
		if err := os.WriteFile(tmp, []byte(sha+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, lastBuiltSHAPath); err != nil {
			return fmt.Errorf("rename %s → %s: %w", tmp, lastBuiltSHAPath, err)
		}
		return nil
	}

	// Prelude steps — always run. Ownership self-heal, then git pull
	// with before/after HEAD capture so we can decide whether to
	// continue past this point.
	prelude := []struct {
		name string
		fn   func() error
	}{
		{"fix repo ownership", func() error {
			// `git pull` runs as the jabali user, but a previous hand-run
			// of `git fetch`/`git pull` as root inside the repo silently
			// chowns FETCH_HEAD/ORIG_HEAD/objects/* to root — and the
			// next `jabali update` then dies with "cannot open
			// '.git/FETCH_HEAD': Permission denied". Re-chown the .git
			// dir before pulling so the update self-heals instead of
			// requiring the operator to know the magic chown command.
			//
			// Also chown panel-ui/test-results/ + playwright-report/
			// since `npx playwright test` run as root drops files
			// there which then block sudo-as-jabali `git reset --hard`
			// with "unable to unlink: Permission denied". Both dirs
			// are .gitignore'd; chown is purely to let `git reset`
			// remove any leftovers from a previous Playwright run.
			if err := run("", "chown", "-R",
				serviceUser+":"+serviceUser, repoDir+"/.git"); err != nil {
				return err
			}
			// Ignore failures on the panel-ui dirs — they may not
			// exist yet on a fresh install, and missing dirs are not
			// a deployment-blocking condition.
			_ = run("", "bash", "-c",
				"shopt -s nullglob; "+
					"chown -R "+serviceUser+":"+serviceUser+" "+
					repoDir+"/panel-ui/test-results "+
					repoDir+"/panel-ui/playwright-report 2>/dev/null; "+
					"true")
			return nil
		}},
		{"ensure mail ACME webroot (/var/www/jabali-acme)", func() error {
			// Per-domain mail certs serve /.well-known/acme-challenge/ from
			// /var/www/jabali-acme — separate from the panel cert's
			// /var/www/jabali-panel-acme. install.sh creates it only in
			// main()/bootstrap_panel_acme_webroot, which `jabali update`
			// does NOT run, so existing installs were missing it and EVERY
			// mail cert failed certbot "webroot does not exist or is not a
			// directory" (GH#132). Create it here in the PRELUDE so it lands
			// on every update, including the fast-path "already up to date"
			// no-op (buildSteps is skipped when last-built-sha == HEAD, and
			// the agent isn't always restarted on a binary-only change).
			// Idempotent: install -d is a no-op when the dir already exists.
			return run("", "bash", "-c", `set -e
WR=/var/www/jabali-acme
[ -d "$WR" ] || install -d -m 0750 -o root -g www-data "$WR"
chown root:www-data "$WR"
chmod 0750 "$WR"`)
		}},
		{"git fetch + reset to release channel", func() error {
			// Release channel (GH #445): "development" tracks origin/main
			// (historical behavior); "stable" tracks the movable `stable`
			// tag (a reviewed build promoted via `jabali release promote`).
			channel := releaseChannelOrDefault()
			// The VM is a deployment target, not a source of truth. Tracked-
			// file drift (typical cause: operator `sed`/patches a file in
			// place on the VM to test a fix, then later commits the same
			// change from a dev box and pushes) is ALWAYS disposable on
			// update — the authoritative copy is origin/main. Using
			// `git pull --ff-only` fails loudly in this case and forces the
			// operator to hand-stash or hand-reset before the update can
			// proceed; switching to fetch + `reset --hard origin/main`
			// makes the update self-healing without clobbering anything
			// the operator would actually want to keep.
			//
			// Untracked files (node_modules, bin/, .env, .cache/) are
			// untouched by `reset --hard` — it only rewrites tracked
			// content. Be LOUD about discarded drift so an operator who
			// didn't expect it sees what's gone and can recover it from
			// reflog if needed.
			// One-time self-cutover to GitHub: the forge moved retired Gitea
			// (git.jabali-panel.com / git.linux-hosting.co.il) → Codeberg →
			// GitHub (shukiv/jabali-panel; Codeberg dropped after its release
			// storage quota kept 413-ing publishes). Any origin still pointing
			// at a superseded host is repointed to GitHub here, so after this
			// one update the host fetches from GitHub directly and every
			// subsequent update comes from GitHub. Idempotent + best-effort.
			_ = asUser(repoDir, "bash", "-c",
				`u=$(git remote get-url origin 2>/dev/null); `+
					`case "$u" in `+
					`*git.jabali-panel.com*|*git.linux-hosting.co.il*|*codeberg.org*) `+
					`git remote set-url origin https://github.com/shukiv/jabali-panel.git;; `+
					`esac`)
			// Always fetch main (required).
			if err := asUser(repoDir, "git", "fetch", "origin", "main"); err != nil {
				return err
			}
			resetRef := "origin/main"
			if channel == "stable" {
				// Fetched separately from main: the `stable` tag is absent on
				// origin until the first promote, so a combined fetch would
				// hard-fail on a host that has never had one (GH #445).
				//
				// "Absent upstream" is the only failure we may ignore, though.
				// Any other one leaves a possibly-stale LOCAL refs/tags/stable
				// that rev-parse below would happily accept, resetting the
				// checkout backwards (JAB-210) — so classify, don't discard.
				fetchOut, fetchErr := asUserOut(repoDir, "git", "fetch", "--force", "origin",
					"+refs/tags/stable:refs/tags/stable")
				tagOnOrigin, fatal := classifyStableTagFetch(fetchErr, fetchOut)
				if fatal != nil {
					return fatal
				}
				if tagOnOrigin && asUser(repoDir, "git", "rev-parse", "--verify", "--quiet",
					"refs/tags/stable^{commit}") == nil {
					resetRef = "refs/tags/stable"
					fmt.Println("  release channel: stable -> tracking the promoted `stable` tag")
				} else {
					// No stable build promoted yet: stay on the current
					// build rather than jumping to main. Conservative by design.
					fmt.Println("  release channel: stable, but no `stable` release has been promoted yet — staying on the current build (use `jabali release promote` to publish one, or switch to the development channel).")
					resetRef = "HEAD"
				}
			}
			// Show diffstat of any local drift vs HEAD before we reset so
			// the operator can see what was clobbered. Silent on clean tree.
			_ = asUser(repoDir, "bash", "-c",
				`d=$(git diff --stat HEAD); `+
					`if [ -n "$d" ]; then `+
					`  echo "  (discarding local modifications on deployment target:)"; `+
					`  echo "$d" | sed "s/^/    /"; `+
					`  echo "  (recover from reflog if this was a surprise: git reflog, git reset --hard <sha>)"; `+
					`fi`)
			if err := asUser(repoDir, "git", "reset", "--hard", resetRef); err != nil {
				// A reset that dies with "unable to unlink ... Permission
				// denied" means a root-owned tracked file is in the repo (a
				// file written by a root-run command, or a manually-added
				// docker-app under install/). The reset runs as the service
				// user, so it can't replace those — and without this retry the
				// update aborts and the host silently stays on OLD code (the
				// GH #298 wp-cli self-heal, and every other fix, then never
				// land). Self-heal: chown the whole working tree back to the
				// service user and retry once.
				fmt.Println("  git reset failed (likely root-owned files in the repo) — re-chowning the tree to " + serviceUser + " and retrying")
				if cerr := run("", "chown", "-R", serviceUser+":"+serviceUser, repoDir); cerr != nil {
					return fmt.Errorf("git reset --hard %s failed and chown repair failed: %w (original reset error: %v)", resetRef, cerr, err)
				}
				if err2 := asUser(repoDir, "git", "reset", "--hard", resetRef); err2 != nil {
					return fmt.Errorf("git reset --hard %s failed even after re-chowning the repo to %s: %w", resetRef, serviceUser, err2)
				}
			}
			// GH #606: a corrupt/linked worktree (the reporting box's .git was
			// a corrupted worktree pointer, flagged by `jabali repair
			// --diagnose`) can leave HEAD correct after `git reset --hard` yet
			// some tracked files missing from the working tree — the reset
			// compares against a stale index and skips re-materializing them.
			// The very next step's `install -m <repo>/install/systemd/<unit>`
			// then dies with "install: cannot stat ... No such file or
			// directory" and the whole update aborts, stranding the host on
			// OLD code. Self-heal: if any tracked file is missing from the
			// working tree, force-restore it from the index; if it is STILL
			// missing afterwards the .git pointer itself is corrupt, so fail
			// with an actionable message instead of the opaque install-stat
			// error downstream.
			missingOut, _ := exec.Command("sudo", "-u", serviceUser,
				"git", "-C", repoDir, "ls-files", "--deleted").Output()
			if strings.TrimSpace(string(missingOut)) != "" {
				fmt.Println("  working tree is missing tracked files after reset (incomplete/corrupt checkout) — forcing a fresh checkout of the tree")
				_ = asUser(repoDir, "git", "checkout", "--force", "--", ".")
				missingOut2, _ := exec.Command("sudo", "-u", serviceUser,
					"git", "-C", repoDir, "ls-files", "--deleted").Output()
				if still := strings.TrimSpace(string(missingOut2)); still != "" {
					// A corrupt .git worktree POINTER can't be fixed by a forced
					// checkout (git operates through the broken pointer). The repair
					// that heals it re-clones the tree, which is DESTRUCTIVE, so
					// `--auto` skips it — the operator needs `--all --yes` (GH #606).
					return fmt.Errorf("working tree at %s is still missing tracked files after a forced checkout "+
						"(corrupt git worktree pointer) — run `jabali repair --all --yes` to re-clone the checkout (--auto skips this destructive fix), then re-run `jabali update`; missing:\n%s",
						repoDir, still)
				}
			}
			post, err := gitHead()
			if err != nil {
				return err
			}
			postHEAD = post
			return nil
		}},
		{"provision new software", func() error {
			// install.sh's provision_new_software() is the canonical place
			// for idempotent software additions (apt packages, cscli
			// collections, wget downloads) that must reach existing hosts
			// on every `jabali update`, not just fresh installs. Add new
			// dependencies there; they will be picked up here automatically.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil // no install.sh — dev environment, skip
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && provision_new_software"); err != nil {
				fmt.Printf("  (provision_new_software failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync panel-cert deploy-hook (clobber fix)", func() error {
			// The LE deploy-hook used to copy ANY renewed lineage to
			// /etc/jabali/tls/panel.crt (default kind=hostname), so a
			// tenant-domain renewal made the panel + Stalwart serve that
			// tenant's cert. Re-deploy the fixed hook. PRELUDE + idempotent.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_jabali_panel_cert_hook"); err != nil {
				fmt.Printf("  (install_jabali_panel_cert_hook failed: %v -- continuing)\n", err)
			}
			return nil
		}},
		{"sync nginx TLS curve hardening (CF 525 / OpenSSL 3.5 PQ)", func() error {
			// OpenSSL 3.5 made X25519MLKEM768 a default TLS 1.3 group;
			// Cloudflare's origin pull can't negotiate it -> error 525 on
			// every CF-fronted site. install_nginx_ssl_hardening drops a
			// conf.d snippet pinning classical curves. PRELUDE so it lands
			// on every update incl. the fast path. Idempotent.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_nginx_ssl_hardening"); err != nil {
				fmt.Printf("  (install_nginx_ssl_hardening failed: %v -- continuing)\n", err)
			}
			return nil
		}},
		{"self-heal panel-hostname vhost + landing (GH#135)", func() error {
			// jabali update never re-renders the server-scope nginx vhosts
			// (only fresh installs run install_nginx_default_vhost), so the
			// GH#135 fix — the panel hostname's own :443 vhost + landing
			// page — never reaches existing installs. This lives in PRELUDE
			// (not buildSteps) so it runs on EVERY update, including the
			// fast-path "already up to date" no-op; buildSteps is skipped
			// when last-built-sha == HEAD.
			//
			// Detect-gated two ways: (1) no-op when the landing vhost is
			// already present (avoids re-render + nginx reload on every
			// converged update); (2) SKIP rather than render empty vars
			// when hostname/IP can't be derived, since
			// install_nginx_default_vhost _die's on a failed nginx -t.
			// IPv4 is read back from the live `listen <ip>:80
			// default_server` line install.sh already rendered.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			script := `set -e
CONF=/etc/nginx/sites-available/jabali-default.conf
HN="$(hostname -f)"
if [ -n "$HN" ] && grep -Fq "server_name $HN;" "$CONF" 2>/dev/null; then
  exit 0
fi
IP="$(grep -oE 'listen [0-9.]+:80 default_server' "$CONF" 2>/dev/null | grep -oE '[0-9.]+' | head -1)"
if [ -z "$HN" ] || [ -z "$IP" ]; then
  echo "  (skip: could not derive hostname/IP for default-vhost re-render)"
  exit 0
fi
echo "  re-rendering default vhost for $HN ($IP) — panel-hostname landing was missing"
export JABALI_SRV_HOSTNAME="$HN" JABALI_SRV_IPV4="$IP"
source ` + installSh + ` && install_nginx_default_vhost`
			if err := run("", "bash", "-c", script); err != nil {
				fmt.Printf("  (default vhost re-render failed: %v -- continuing)\n", err)
			}
			return nil
		}},
		{"self-heal nginx http2 deprecation on >=1.25.1 (GH #292)", func() error {
			// The inverse of the fold above: on nginx >=1.25.1 the
			// `listen ... http2` parameter the server-scope vhosts were
			// rendered with is DEPRECATED and warns on every reload. update
			// re-renders the agent vhosts but not the install.sh-written
			// jabali-default/jabali-panel vhosts, so re-render them via the
			// (now version-aware) install.sh writers. Detect-gated on
			// nginx>=1.25.1 AND a deprecated form still present, so it is a
			// no-op + no reload once converged. Hostname/IP derived the same
			// proven way as the GH#135 self-heal above.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			script := `set -e
ver="$(nginx -v 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$ver" ] || exit 0
IFS=. read -r MA MI PA <<EOF
$ver
EOF
ge=0
{ [ "$MA" -gt 1 ]; } 2>/dev/null && ge=1
{ [ "$MA" -eq 1 ] && [ "$MI" -gt 25 ]; } 2>/dev/null && ge=1
{ [ "$MA" -eq 1 ] && [ "$MI" -eq 25 ] && [ "$PA" -ge 1 ]; } 2>/dev/null && ge=1
[ "$ge" = 1 ] || exit 0
CONF=/etc/nginx/sites-available/jabali-default.conf
PCONF=/etc/nginx/sites-available/jabali-panel.conf
grep -qE 'listen .* http2;' "$CONF" "$PCONF" 2>/dev/null || exit 0
HN="$(hostname -f)"
IP="$(grep -oE 'listen [0-9.]+:80 default_server' "$CONF" 2>/dev/null | grep -oE '[0-9.]+' | head -1)"
if [ -z "$HN" ] || [ -z "$IP" ]; then echo "  (skip http2 re-render: no hostname/IP)"; exit 0; fi
echo "  re-rendering server-scope nginx vhosts for http2 on; (nginx $ver, GH #292)"
export JABALI_SRV_HOSTNAME="$HN" JABALI_SRV_IPV4="$IP"
source ` + installSh + ` && install_nginx_default_vhost && install_nginx_websocket_map && install_nginx_panel_vhost`
			if err := run("", "bash", "-c", script); err != nil {
				fmt.Printf("  (http2 re-render failed: %v -- continuing)\n", err)
			}
			return nil
		}},
		{"ensure dbus (GH #296)", func() error {
			// dbus is a hard dependency (systemd-user cron, resolvectl,
			// machinectl) that minimal Debian KVM/LXC images ship without.
			// ensure_dbus installs it if absent, then activates + enables the
			// system bus; it returns early once the socket exists, so this is a
			// cheap no-op on a healthy host.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c", "source "+installSh+" && ensure_dbus"); err != nil {
				fmt.Printf("  (ensure_dbus failed: %v -- continuing)\n", err)
			}
			return nil
		}},
	}

	// Build/apply steps — run only when HEAD moved OR --force was passed.
	// Keep in lockstep with the install.sh counterparts they mirror.
	// JAB-159 phase 3: /etc/jabali/deploy-profile == "demo" selects a demo
	// build (-tags demo + VITE_DEMO=1). Absent/other = production. Read once
	// and folded into the per-artifact rebuild hashes below so flipping the
	// profile forces a rebuild of the affected artifacts.
	demoProfile := ""
	if b, err := os.ReadFile("/etc/jabali/deploy-profile"); err == nil {
		demoProfile = strings.TrimSpace(string(b))
	}
	demoBuild := demoProfile == "demo"

	buildSteps := []struct {
		name string
		fn   func() error
	}{
		{"install deps", func() error {
			// Re-run the install script's dependency functions for any
			// new packages added since the last update.
			return run("", "bash", "-c",
				"export DEBIAN_FRONTEND=noninteractive && "+
					"apt-get install -y -qq --no-install-recommends nginx zstd >/dev/null 2>&1; "+ // GH #462: zstd for backup-download tar -I zstd
					"install -d -m 0755 /etc/nginx/sites-available; "+
					"install -d -m 0755 /etc/nginx/sites-enabled; "+
					"true")
		}},
		{"sync systemd + shims", func() error {
			// install.sh copies these on first install; the update path
			// needs to re-copy them so unit file / shim changes land.
			// Keep in sync with install_jabali_slices(),
			// install_sso_reaper_timer(), and install_kratos() in install.sh.
			script := `set -e
install -d -m 0755 /usr/local/libexec/jabali
# #430: ensure setfacl is present (fpm-post-start ACLs the per-user FPM socket).
command -v setfacl >/dev/null 2>&1 || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq acl >/dev/null 2>&1 || true
install -m 0755 ` + repoDir + `/install/systemd/fpm-pre-start /usr/local/libexec/jabali/fpm-pre-start
install -m 0755 ` + repoDir + `/install/systemd/fpm-exec /usr/local/libexec/jabali/fpm-exec
# #430: fpm-post-start ExecStartPost (jabali-fpm@.service references it) — without
# this, the update ships the unit + the www-data removal but not the socket-reach
# fix, and the next FPM restart can't reach the socket. MUST land with the unit.
install -m 0755 ` + repoDir + `/install/systemd/fpm-post-start /usr/local/libexec/jabali/fpm-post-start
install -m 0755 ` + repoDir + `/install/systemd/cron-precheck /usr/local/libexec/jabali/cron-precheck
install -m 0644 ` + repoDir + `/install/systemd/jabali.slice /etc/systemd/system/jabali.slice
install -m 0644 ` + repoDir + `/install/systemd/jabali-user.slice /etc/systemd/system/jabali-user.slice
install -m 0644 ` + repoDir + `/install/systemd/jabali-fpm@.service /etc/systemd/system/jabali-fpm@.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-sso-reaper.service /etc/systemd/system/jabali-sso-reaper.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-sso-reaper.timer /etc/systemd/system/jabali-sso-reaper.timer
install -m 0644 ` + repoDir + `/install/systemd/jabali-retention-sweep.service /etc/systemd/system/jabali-retention-sweep.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-retention-sweep.timer /etc/systemd/system/jabali-retention-sweep.timer
install -m 0644 ` + repoDir + `/install/systemd/jabali-cache-doctor.service /etc/systemd/system/jabali-cache-doctor.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-cache-doctor.timer /etc/systemd/system/jabali-cache-doctor.timer
install -m 0644 ` + repoDir + `/install/systemd/jabali-notify@.service /etc/systemd/system/jabali-notify@.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-stalwart.service /etc/systemd/system/jabali-stalwart.service
# JAB-158 journald cap + JAB-153/157 disk-maintenance timer. install.sh drops
# these on first install (install_journald_cap + install_disk_maintenance_timer);
# the update path MUST re-copy them or existing hosts never get the caps.
install -d -m 0755 /etc/systemd/journald.conf.d
install -m 0644 ` + repoDir + `/install/systemd/journald-jabali.conf /etc/systemd/journald.conf.d/jabali.conf
install -m 0755 ` + repoDir + `/install/systemd/disk-maintenance /usr/local/libexec/jabali/disk-maintenance
install -m 0644 ` + repoDir + `/install/systemd/jabali-disk-maintenance.service /etc/systemd/system/jabali-disk-maintenance.service
install -m 0644 ` + repoDir + `/install/systemd/jabali-disk-maintenance.timer /etc/systemd/system/jabali-disk-maintenance.timer
# certbot panel-cert deploy-hook. install.sh drops this on first
# install; the update path MUST re-copy it or hook changes never
# reach existing hosts. This gap shipped a stale pre-ADR-0105 hook
# on mx: the kind=mail reconcile ran the old hook (no kind branch),
# which wrote the MAIL lineage into /etc/jabali/tls/panel.crt, so
# nginx :8443 served a cert valid only for mail.<hostname>
# (ERR_CERT_COMMON_NAME_INVALID). Same gap also kept the --no-block
# self-restart fix from ever reaching deployed hosts.
install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
install -m 0755 ` + repoDir + `/install/letsencrypt/jabali-panel-cert.sh /etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh
# jabali-stalwart-push-cert: install.sh refreshes this inside
# _install_stalwart_cli, which provision_new_software now re-runs on
# update (version-gated) — but provision is best-effort, so re-copy it
# here too as a belt-and-suspenders. Without it, hosts keep the pre-M6.6 hardcoded-
# path push-cert that ignores the JABALI_STALWART_CERT_* env the
# mail-domain deploy-hook exports — so per-domain mail certs (GH #132)
# got pushed under the PANEL cert name and Stalwart served the panel
# cert for every SNI on :465/:993. Idempotent file copy.
if [ -f ` + repoDir + `/install/stalwart/jabali-stalwart-push-cert.sh ]; then
  install -m 0755 ` + repoDir + `/install/stalwart/jabali-stalwart-push-cert.sh /usr/local/bin/jabali-stalwart-push-cert
fi
# jabali-kratos.service: sync with sha256 check so we restart only on change.
sha_before_k=$(sha256sum /etc/systemd/system/jabali-kratos.service 2>/dev/null | awk '{print $1}' || echo "")
install -m 0644 ` + repoDir + `/install/systemd/jabali-kratos.service /etc/systemd/system/jabali-kratos.service
systemctl daemon-reload
systemctl enable --now jabali-sso-reaper.timer
systemctl enable --now jabali-disk-maintenance.timer
systemctl enable --now jabali-retention-sweep.timer
systemctl restart systemd-journald 2>/dev/null || true
sha_after_k=$(sha256sum /etc/systemd/system/jabali-kratos.service 2>/dev/null | awk '{print $1}' || echo "")
if [ "$sha_before_k" != "$sha_after_k" ]; then
  echo "  (jabali-kratos.service changed — restarting)"
  systemctl restart jabali-kratos.service || true
fi
`
			return run("", "bash", "-c", script)
		}},
		{"sync docker-app catalog", func() error {
			// M48: rsync install/docker-apps/ -> /usr/local/share/
			// jabali/docker-apps/, the path panel-api reads at
			// startup (cmd/server/serve.go ~line 191).
			// install.sh's build_backend() drops the same block, but
			// `jabali update` doesn't go through build_backend — so
			// without this step, existing VMs upgrade to a new
			// catalog version and still see "0 in catalog" until
			// they re-run install.sh by hand. Idempotent.
			src := repoDir + "/install/docker-apps"
			if _, err := os.Stat(src); err != nil {
				return nil
			}
			return run("", "bash", "-c",
				"install -d -m 0755 /usr/local/share/jabali/docker-apps && "+
					"rsync -a --delete --exclude=.git "+src+"/ /usr/local/share/jabali/docker-apps/")
		}},
		{"reconcile crowdsec appsec config", func() error {
			// install_crowdsec_appsec is the canonical writer of
			// /etc/crowdsec/appsec-configs/jabali-appsec.yaml. Its
			// on_match reconcile migrates a stale allowlist prefix to
			// the current /api/v1/ (ADR-0107) — but it only runs on
			// fresh install unless invoked here. Without this step a
			// host that ran `jabali update` kept the old
			// /api/v1/admin/ allowlist, so every panel PATCH/PUT/DELETE
			// 403'd at the CrowdSec WAF (the production symptom on the
			// second server). Idempotent: seeds only if absent,
			// reconciles inband + on_match in place, rewrites acquis
			// only on diff. Reload crowdsec only if the config changed.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			cfg := "/etc/crowdsec/appsec-configs/jabali-appsec.yaml"
			before, _ := os.ReadFile(cfg)
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_crowdsec_appsec"); err != nil {
				fmt.Printf("  (install_crowdsec_appsec failed: %v — continuing)\n", err)
				return nil
			}
			after, _ := os.ReadFile(cfg)
			if string(before) != string(after) {
				fmt.Println("  (jabali-appsec.yaml changed — reloading crowdsec)")
				if err := run("", "bash", "-c", "systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec || true"); err != nil {
					fmt.Printf("  (crowdsec reload failed: %v — continuing)\n", err)
				}
			}
			return nil
		}},
		{"sync nginx fastcgi-cache keyzone", func() error {
			// ADR-0108: the shared FastCGI keyzone (http context) must
			// exist before any per-domain vhost references
			// `fastcgi_cache jabali_fcgi;`, or `nginx -t` fails and the
			// reload is refused. install.sh ships it on fresh install;
			// the update path MUST re-copy it or hosts that enable
			// caching get a broken nginx config (the recurring
			// "jabali update doesn't refresh host config" scar —
			// PR#45/#49). Static file copy + dir; reload only when the
			// conf changed AND `nginx -t` passes (fail-safe: never
			// reload a broken config).
			src := repoDir + "/install/nginx/jabali-fastcgi-cache.conf"
			if _, err := os.Stat(src); err != nil {
				return nil // dev tree without the asset — skip
			}
			dst := "/etc/nginx/conf.d/jabali-fastcgi-cache.conf"
			before, _ := os.ReadFile(dst)
			want, _ := os.ReadFile(src)
			script := "install -d -m 0700 -o www-data -g www-data /var/cache/nginx/jabali; " +
				"install -m 0644 " + src + " " + dst
			if err := run("", "bash", "-c", script); err != nil {
				fmt.Printf("  (fastcgi-cache keyzone install failed: %v — continuing)\n", err)
				return nil
			}
			if string(before) != string(want) {
				if err := run("", "bash", "-c", "nginx -t"); err != nil {
					fmt.Printf("  (nginx -t failed after fastcgi-cache keyzone: %v — NOT reloading)\n", err)
					return nil
				}
				fmt.Println("  (jabali-fastcgi-cache.conf changed — reloading nginx)")
				_ = run("", "bash", "-c", "systemctl reload nginx || systemctl restart nginx || true")
			}
			return nil
		}},
		{"clean stale TLS env from panel.env (M25 socket lockdown)", func() error {
			// Hosts that ran jabali update before M25 still carry
			// TLS_CERT + TLS_KEY in /etc/jabali/panel.env from when
			// panel-api listened on https://0.0.0.0:443. Post-M25 the
			// addr is unix:/run/jabali-panel/api.sock and nginx
			// terminates TLS upstream — the env vars are no-ops and
			// trigger a per-restart WARN log. Comment them out so a
			// re-enable is a one-line uncomment for the operator.
			const panelEnv = "/etc/jabali/panel.env"
			b, err := os.ReadFile(panelEnv)
			if err != nil {
				return nil // file absent on fresh installs; not an error
			}
			src := string(b)
			if !strings.Contains(src, "PANEL_ADDR=unix:") {
				return nil // addr is TCP; keep TLS active
			}
			lines := strings.Split(src, "\n")
			changed := false
			for i, ln := range lines {
				trim := strings.TrimSpace(ln)
				if strings.HasPrefix(trim, "TLS_CERT=") || strings.HasPrefix(trim, "TLS_KEY=") {
					lines[i] = "# " + ln + "   # auto-disabled by jabali update: addr is unix socket"
					changed = true
				}
			}
			if !changed {
				return nil
			}
			if err := os.WriteFile(panelEnv, []byte(strings.Join(lines, "\n")), 0o640); err != nil {
				fmt.Printf("  (clean stale TLS env: write failed: %v)\n", err)
				return nil
			}
			fmt.Println("  (commented stale TLS_CERT/TLS_KEY in panel.env; restart panel to drop warn)")
			return nil
		}},
		{"sync OnFailure + restart drop-ins", func() error {
			// Re-render /etc/systemd/system/<unit>.service.d/10-jabali-restart.conf
			// for every critical service (nginx/mariadb/pdns/redis/crowdsec/
			// systemd-resolved + jabali daemons). Idempotent — the helper
			// hashes before/after and only daemon-reloads on diff.
			//
			// Without this on the update path, hosts installed before the
			// units list grew (e.g. before the jabali-* additions) never
			// pick up the OnFailure=jabali-notify@%n hook for those units,
			// so a service.down event only fires from the polling
			// service_down event source — which is up to a 60s window
			// behind the actual crash.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_restart_drop_ins"); err != nil {
				fmt.Printf("  (install_restart_drop_ins failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync service-user dirs (ensure_user_and_dirs)", func() error {
			// Mirror install.sh's ensure_user_and_dirs on every
			// jabali update. Catches any dirs added to the function
			// after the initial install — e.g. /var/lib/jabali-
			// migrations (M35) shipped in 30041b57 didn't land on
			// existing hosts because update.go didn't re-source
			// the function. Round 6 QA flagged the missing dir +
			// hand-created it; this step closes the gap.
			//
			// Idempotent: every sub-step is `install -d` or
			// `usermod -aG` which is no-op when state already
			// matches.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && ensure_user_and_dirs"); err != nil {
				fmt.Printf("  (ensure_user_and_dirs failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync apparmor profiles", func() error {
			// Re-render every jabali AppArmor profile from
			// install/apparmor/ + reload distro system profiles
			// (mariadb/redis/pdns). Without this, profile edits in the
			// repo never reach existing hosts — operator has to ssh in
			// and `apparmor_parser -r` by hand. Detected first-time
			// installs preserve complain mode; existing hosts keep
			// whatever mode the operator chose per profile.
			//
			// Idempotent. Failure is logged but doesn't block the
			// update; running daemons keep their currently-loaded
			// profile (or none if AA is disabled on this host).
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			// first_install=0 → preserve existing per-profile mode.
			if err := run("", "bash", "-c",
				// GH #687: route through install_apparmor so the update honors the
				// kernel-feature gate (skips loading daemon profiles on kernels
				// missing /sys/.../apparmor/features/unix) instead of applying them
				// unconditionally.
				"source "+installSh+" && install_apparmor"); err != nil {
				fmt.Printf("  (apparmor profile sync failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync jabali-agent + jabali-panel units", func() error {
			// Re-render the jabali-agent.service and jabali-panel.service
			// unit files from install.sh's writers so hardening/env
			// changes (PrivateTmp drops, SupplementaryGroups additions,
			// RestartSec tweaks) reach existing hosts. Without this,
			// `jabali update` only copies binaries — the running unit
			// keeps the install-time directives until the operator
			// manually re-runs install.sh.
			//
			// The cascade is delicate: jabali-panel.service Requires=
			// jabali-agent.service, so restarting the agent restarts
			// panel-api (us). To avoid suicide-during-update, we
			// daemon-reload here and schedule the restart via a transient
			// one-shot 5s in the future — by then the parent shell has
			// returned and the cascade is harmless.
			//
			// Detection of "did anything change?" is by sha256 over the
			// before/after file content; we only schedule the restart
			// when content actually drifted.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			script := `set -e
sha_before_a=$(sha256sum /etc/systemd/system/jabali-agent.service 2>/dev/null | awk '{print $1}' || echo "")
sha_before_p=$(sha256sum /etc/systemd/system/jabali-panel.service 2>/dev/null | awk '{print $1}' || echo "")
source ` + installSh + `
write_agent_systemd_unit
write_systemd_unit
systemctl daemon-reload
sha_after_a=$(sha256sum /etc/systemd/system/jabali-agent.service 2>/dev/null | awk '{print $1}' || echo "")
sha_after_p=$(sha256sum /etc/systemd/system/jabali-panel.service 2>/dev/null | awk '{print $1}' || echo "")
if [ "$sha_before_a" != "$sha_after_a" ] || [ "$sha_before_p" != "$sha_after_p" ]; then
  # Schedule a deferred restart so the cascade (panel-api Requires= agent)
  # doesn't kill us mid-update. 5s gives the parent shell ample time to
  # return.
  systemd-run --on-active=5s --unit=jabali-agent-deferred-restart /bin/systemctl restart jabali-agent.service >/dev/null 2>&1 || true
  echo "  (agent/panel unit content changed — restart scheduled in 5s)"
fi
`
			if err := run("", "bash", "-c", script); err != nil {
				fmt.Printf("  (unit sync failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync static assets", func() error {
			// Mirror the file-writing half of install_php_pool_template(),
			// ensure_user_and_dirs(), and install_phpmyadmin() from
			// install.sh so template, group-membership, and phpMyAdmin
			// handler changes land on update. apt package installs and
			// systemd service creation stay in install.sh — update is for
			// hosts that already booted once.
			return run("", "bash", "-c",
				"set -e; "+
					"install -d -m 0755 -o root -g root /etc/jabali-panel/fpm; "+
					"install -d -m 0755 -o root -g root /etc/jabali-panel/user-phpver; "+
					// pool template — changes here affect every future
					// pool-apply for every user.
					"install -m 0644 "+repoDir+"/install/php/jabali-php-pool.conf.tmpl /etc/jabali-panel/php-pool.conf.tmpl; "+
					// phpMyAdmin SSO handler — install.sh extracts the
					// tarball to /opt/phpmyadmin/current/, but updates to
					// sso.php/config.inc.php shipped in the repo never
					// reach the host unless install.sh is re-run. Copy
					// them now so update.go is the single-source refresh.
					"if [ -d /opt/phpmyadmin/current ]; then "+
					"  install -m 0640 -o root -g www-data "+repoDir+"/install/phpmyadmin/sso.php /opt/phpmyadmin/current/sso.php; "+
					// Strip controluser/controlpass/controlhost/controlport
					// from an existing config.inc.php. Earlier installs
					// seeded controluser='root', which makes phpMyAdmin
					// open a second connection as root@localhost on every
					// page load and surface two "Access denied" banners
					// even though SSO succeeded. pmadb is already false,
					// so no control connection is needed — stripping the
					// keys makes PMA skip it. Idempotent: sed -i only
					// rewrites if lines match, so re-running is a no-op
					// once they're gone.
					"  if [ -f /opt/phpmyadmin/current/config.inc.php ]; then "+
					"    sed -i \"/\\$cfg\\['Servers'\\]\\[1\\]\\['control\\(user\\|pass\\|host\\|port\\)'\\]/d\" /opt/phpmyadmin/current/config.inc.php; "+
					"  fi; "+
					"fi; "+
					// M11 filebrowser decommission (ADR-0030): stop/disable the
					// legacy service + strip its nginx include so updates don't
					// resurrect dead bits. Idempotent: || true swallows
					// "not-found" on hosts that never had it.
					"systemctl stop jabali-filebrowser.service 2>/dev/null || true; "+
					"systemctl disable jabali-filebrowser.service 2>/dev/null || true; "+
					"rm -f /etc/systemd/system/jabali-filebrowser.service /etc/nginx/conf.d/jabali-files.conf /etc/nginx/sites-available/includes/jabali-files.conf; "+
					"sed -i '/includes\\/jabali-files.conf/d' /etc/nginx/sites-available/jabali-default.conf 2>/dev/null || true; "+
					"systemctl daemon-reload; "+
					"nginx -t && systemctl reload nginx; "+
					// SFTP jabali-sftp group — required by the sshd Match block and by
					// the reconciler's join_sftp_group agent call. Idempotent.
					"getent group jabali-sftp >/dev/null || groupadd --system jabali-sftp; "+
					// SFTP sshd drop-in — update ships new versions of the Match block.
					// Idempotent: install -m 0644 is a no-op if target matches source.
					"install -m 0644 -o root -g root "+repoDir+"/install/ssh/jabali-sftp.conf /etc/ssh/sshd_config.d/jabali-sftp.conf; "+
					// Validate before reload so a broken config doesn't take down SSH.
					// If sshd -t fails here, the step returns non-zero and the update
					// halts before the reload. Operator sees the error and fixes the
					// source file.
					"sshd -t; "+
					// GH #133: do NOT `systemctl reload ssh` here. On hosts where
					// ssh.service + ssh.socket are both enabled (Debian 13 /
					// Ubuntu 24.04 preset) they conflict on :22; a reload SIGHUPs a
					// socket-activated sshd that then cannot rebind the port
					// (fatal: Cannot bind any address) -> ssh.service fails ->
					// operator lockout. Instead run the shared normalizer, which
					// converges the host onto a single classic ssh.service listener
					// (masking ssh.socket) in a lockout-safe sequence and applies
					// the freshly-written drop-in via a clean restart.
					"bash "+repoDir+"/install/ssh/normalize-ssh-classic.sh || echo 'ssh normalize reported a problem' >&2; "+
					// jabali service user in www-data group — needed for
					// the reconciler's per-user FPM socket stat-check.
					// usermod is idempotent; 'groups | grep -w' avoids an
					// unnecessary write when already a member.
					"groups "+serviceUser+" | grep -qw www-data || usermod -aG www-data "+serviceUser+"; "+
					// M13 SSH sandbox: refresh wrapper + nspawn-enter +
					// sudoers + mode files on every update. Idempotent.
					"getent group jabali-ssh-sandbox >/dev/null || groupadd --system jabali-ssh-sandbox; "+
					"install -d -m 0755 -o root -g root /etc/jabali /etc/jabali/users /var/lib/jabali-nspawn /var/lib/jabali-nspawn/images; "+
					"install -m 0755 -o root -g root "+repoDir+"/install/ssh/jabali-nspawn-enter /usr/local/bin/jabali-nspawn-enter; "+
					"visudo -cf "+repoDir+"/install/ssh/jabali-nspawn-sudoers >/dev/null && install -m 0440 -o root -g root "+repoDir+"/install/ssh/jabali-nspawn-sudoers /etc/sudoers.d/jabali-nspawn; "+
					"[ -f /etc/jabali/ssh-sandbox-mode ] || { echo bubblewrap > /etc/jabali/ssh-sandbox-mode; chmod 0644 /etc/jabali/ssh-sandbox-mode; }; "+
					"[ -f /etc/jabali/default-nspawn-image ] || { echo debian-12-v1 > /etc/jabali/default-nspawn-image; chmod 0644 /etc/jabali/default-nspawn-image; }")
		}},
		{"install from release tarball (or fall back to source)", func() error {
			if fromSource {
				fmt.Println("  (skipped: --from-source flag)")
				return nil
			}
			// JAB-210: refuse a release whose migrations stop short of the
			// schema this database is already at. Checked HERE, before the
			// swap — verifying afterwards would report the very state we are
			// trying to prevent, which is what happened on the reported box:
			// the update aborted at `migrate up`, but only after an older
			// binary had already replaced the newer one on disk.
			if cfg := sharedCfg; cfg.Database.URL != "" && cfg.Database.URL != "placeholder-until-phase-3" {
				candidateMax, mErr := maxMigrationVersionInDir(repoDir)
				if mErr != nil {
					return fmt.Errorf("schema downgrade check: %w", mErr)
				}
				st, sErr := db.State(cfg.Database.URL)
				if sErr != nil {
					return fmt.Errorf("schema downgrade check: read live schema version: %w", sErr)
				}
				if gErr := guardSchemaDowngrade(candidateMax, st.Version); gErr != nil {
					return gErr
				}
			}
			ctx := cmd.Context()
			snapshotBeforeSwap()
			installed, sha, err := installFromRelease(ctx, repoDir, func(format string, args ...any) {
				fmt.Printf("  "+format+"\n", args...)
			})
			if err != nil {
				// Hard failure (sha256 mismatch, bad MANIFEST, disk
				// write error). Do NOT fall back to source build —
				// these indicate corruption or a systemic problem
				// the operator needs to see.
				return fmt.Errorf("release install failed (use --from-source to fall back to git+build): %w", err)
			}
			installedFromRelease = installed
			releaseSHA = sha
			if installed {
				fmt.Println("  binaries installed from release tarball — skipping rebuild")
			} else {
				fmt.Println("  no release tarball available — falling back to source build")
			}
			return nil
		}},
		{"npm ci", func() error {
			if installedFromRelease {
				fmt.Println("  (skipped: binaries from release tarball)")
				return nil
			}
			// Skip when package-lock.json + package.json unchanged AND
			// node_modules/.bin/tsc still present.
			want := compositeSHA("panel-ui/package-lock.json", "panel-ui/package.json")
			if artifactUnchanged("npm", want, repoDir+"/panel-ui/node_modules/.bin/tsc") {
				fmt.Println("  (npm ci skipped: lockfile unchanged + node_modules intact)")
				return nil
			}
			// Wipe node_modules before npm ci. npm ci's docs promise it
			// does this itself, but in practice it dies with
			//   ENOTEMPTY: directory not empty, rmdir '.../node_modules/vite'
			// whenever a prior partial install or filesystem quirk leaves
			// a half-removed package tree behind.
			//
			// The dance below makes that resilient:
			//   1. mv node_modules → node_modules.stale.<pid> so the
			//      target dir is gone in one atomic syscall (rm -rf can
			//      take seconds on a heavy tree, leaving a window where
			//      npm ci sees a half-deleted target and races).
			//   2. background-rm the stale tree so the install isn't
			//      blocked on it.
			//   3. run npm ci. If it fails (the ENOTEMPTY rotate race
			//      inside npm itself, or the partial-install case where
			//      "added N packages" prints but .bin/ is empty), wipe
			//      and retry once — empirically the second attempt
			//      lands clean. Two failures in a row points at a real
			//      package-lock issue and we surface that.
			err := asUser(repoDir+"/panel-ui", "bash", "-c", `set -e
trash="node_modules.stale.$$"
if [ -d node_modules ]; then
  mv node_modules "$trash"
  ( rm -rf "$trash" 2>/dev/null & )
fi
attempt() { npm ci --no-audit --no-fund; }
if ! attempt; then
  echo "[jabali] npm ci failed, wiping node_modules and retrying once..." >&2
  rm -rf node_modules
  sleep 2
  attempt
fi
test -x node_modules/.bin/tsc || {
  echo "[jabali] npm ci reported success but node_modules/.bin/tsc is missing — partial install" >&2
  exit 1
}
`)
			if err != nil {
				return err
			}
			writeArtifactSHA("npm", want)
			return nil
		}},
		{"build frontend", func() error {
			if installedFromRelease {
				fmt.Println("  (skipped: binaries from release tarball)")
				return nil
			}
			// Skip when every input to vite (src + public + index.html
			// + vite.config + tsconfigs + lockfile) is unchanged AND
			// dist/index.html still present.
			want := compositeSHA(
				"panel-ui/src",
				"panel-ui/public",
				"panel-ui/index.html",
				"panel-ui/vite.config.ts",
				"panel-ui/tsconfig.json",
				"panel-ui/tsconfig.app.json",
				"panel-ui/tsconfig.node.json",
				"panel-ui/package-lock.json",
			)
			want += "|profile:" + demoProfile
			if artifactUnchanged("vite", want, repoDir+"/panel-ui/dist/index.html") {
				fmt.Println("  (vite build skipped: src + config + lockfile unchanged + dist intact)")
				return nil
			}
			// vite's emptyDir unlinks every file under dist/ before
			// writing the new bundle. If any prior build left root-owned
			// artifacts there (e.g. a legacy update ran as root, or an
			// operator ran `npm run build` from a root shell), the
			// jabali-owned build here hits EACCES on unlink and aborts.
			// chown the tree to the service user each run — cheap,
			// idempotent, and immune to however dist got into that state.
			distDir := repoDir + "/panel-ui/dist"
			if _, err := os.Stat(distDir); err == nil {
				if err := run("", "chown", "-R",
					serviceUser+":"+serviceUser, distDir); err != nil {
					return err
				}
			}
			// viteBuild wraps `npm run build` with NODE_OPTIONS so
			// node's V8 heap cap matches the host. Without a cap, V8
			// trusts /proc/meminfo and tries to grow up to ~1.5GB on
			// a 1GB-RAM VM — kernel OOM-killer SIGKILLs the process
			// (exit 137) mid-bundle. Capping at 50% of MemTotal keeps
			// node within budget; transient garbage pressure rises but
			// the build actually completes.
			// heapPct is the share of MemTotal V8 may use. 50 is the
			// steady-state cap; the retry raises it — see below.
			viteBuild := func(heapPct int) error {
				viteEnv := ""
				if demoBuild {
					viteEnv = "VITE_DEMO=1 "
				}
				bashCmd := fmt.Sprintf(
					"%sNODE_OPTIONS=\"--max-old-space-size=$(awk '/MemTotal/{print int($2*%.2f/1024)}' /proc/meminfo)\" npm run build",
					viteEnv, float64(heapPct)/100)
				return asUser(repoDir+"/panel-ui", "bash", "-c", bashCmd)
			}
			buildErr := viteBuild(viteHeapPctDefault)
			if buildErr == nil {
				writeArtifactSHA("vite", want)
				return nil
			}
			// Two different out-of-memory failures, and they need
			// opposite remedies.
			//
			//   exit 137 — the KERNEL killed node. There was not enough
			//              real memory; the answer is more swap.
			//   exit 134 — V8 aborted ITSELF on reaching
			//              --max-old-space-size ("JavaScript heap out of
			//              memory"). Memory was available; the ceiling we
			//              imposed was too low. More swap alone changes
			//              nothing — the answer is a higher ceiling.
			//
			// Only 137 used to be handled, which made this worse over
			// time: capping the heap to avoid the OOM-killer is exactly
			// what converts a 137 into a 134, so the original fix turned
			// the failure into a shape its own retry did not match. Seen
			// on a 2 GB host where the cap lands at ~986 MB: the frontend
			// bundle has outgrown that, and `jabali update` aborted with
			// no retry at all.
			if isHeapAbort(buildErr) {
				// Ensure swap first so the larger heap has somewhere to
				// spill; on a host that already has swap this is a clean
				// no-op. Failure is not fatal — the retry may still fit.
				if swapErr := ensureBuildSwap(); swapErr != nil {
					fmt.Printf("  (vite build hit its heap ceiling; swap helper failed, retrying anyway: %v)\n", swapErr)
				}
				fmt.Printf("  (vite build hit its %d%% heap ceiling; retrying once at %d%% of RAM)\n",
					viteHeapPctDefault, viteHeapPctRetry)
				if err2 := viteBuild(viteHeapPctRetry); err2 != nil {
					return err2
				}
				writeArtifactSHA("vite", want)
				return nil
			}
			// Retry once after creating swap when the first attempt
			// died with exit 137 (SIGKILL — almost always OOM-killer
			// on small VMs). Idempotent: skips if any swap already
			// active OR the swap file already exists.
			if isOOMKilled(buildErr) {
				if swapErr := ensureBuildSwap(); swapErr != nil {
					fmt.Printf("  (vite build OOM-killed; swap helper failed: %v)\n", swapErr)
					return buildErr
				}
				fmt.Println("  (vite build OOM-killed; added 2G swap, retrying once)")
				if err2 := viteBuild(viteHeapPctDefault); err2 != nil {
					return err2
				}
				writeArtifactSHA("vite", want)
				return nil
			}
			return buildErr
		}},
		{"prune npm cache", func() error {
			// JAB-156: npm ci leaves every downloaded tarball in the
			// service user's ~/.npm/_cacache (/opt/jabali-panel/.npm),
			// which grows unbounded across updates as deps churn. The
			// build is already done, so purge it — the next update's
			// npm ci re-populates only what it needs. Best-effort: a
			// clean failure must never fail the update.
			if installedFromRelease {
				return nil
			}
			if err := asUser(repoDir+"/panel-ui", "bash", "-c", "npm cache clean --force >/dev/null 2>&1 || true"); err != nil {
				fmt.Printf("  (npm cache clean skipped: %v)\n", err)
			}
			return nil
		}},
		{"build panel-api + panel-agent (parallel)", func() error {
			if installedFromRelease {
				fmt.Println("  (skipped: binaries from release tarball)")
				return nil
			}
			// Both Go binaries are independent: same go module + cache,
			// no shared output. Run concurrently — on a 2-vCPU VPS this
			// halves wall-clock from ~60s → ~30s for a cold rebuild.
			// Per-binary skip checks identical to the npm/vite ones.
			// panel-ui/dist/index.html is the artifact vite emits — its
			// content includes the hashed script + style tag names. Hashing
			// it makes panel-api rebuild whenever the SPA bundle changes,
			// so the //go:embed all:dist in panel-ui/embed.go picks up the
			// fresh assets. Without this, a UI-only change rebuilds dist
			// but leaves the binary embedding the previous bundle hashes,
			// nginx serves the cached HTML pointing at a stale /assets/
			// index-XXX.js that the embedded FS still has, and the browser
			// keeps rendering the old UI even after `jabali update`.
			// "internal" = the repo-root /internal/ tree of SHARED Go
			// packages (appseccfg, cronvalidate, kratosclient, backup,
			// limits, …) that BOTH binaries import. It is NOT under the
			// "panel-api"/"panel-agent" subtrees, so without listing it
			// explicitly a change to any shared package (e.g. moving
			// appseccfg.WebmailHostsPath) doesn't bump these composites
			// and the binary is skipped — shipping a stale binary against
			// a new unit/config. Same failure mode #255 fixed for dist.
			apiInputs := compositeSHA("panel-api", "agentwire", "internal", "go.mod", "go.sum", "panel-ui/dist/index.html")
			apiInputs += "|profile:" + demoProfile
			agentInputs := compositeSHA("panel-agent", "agentwire", "internal", "go.mod", "go.sum")
			apiSkip := artifactUnchanged("panel-api-bin", apiInputs, defaultPanelBinPath)
			agentSkip := artifactUnchanged("panel-agent-bin", agentInputs, defaultAgentBinPath)
			if apiSkip {
				fmt.Println("  (panel-api skipped: sources unchanged + binary intact)")
			}
			if agentSkip {
				fmt.Println("  (panel-agent skipped: sources unchanged + binary intact)")
			}
			if apiSkip && agentSkip {
				return nil
			}
			// Build-info ldflags: surfaced by `jabali version` and the
			// /health endpoint. Mirrors install.sh's build flags so
			// the version string survives both the initial install AND
			// every `jabali update` cycle (without these, panel-api's
			// api.Version drops back to "dev" after the first update).
			shortSHA, _ := gitRevParseAsUser(repoDir, serviceUser, "--short", "HEAD")
			fullSHA, _ := gitRevParseAsUser(repoDir, serviceUser, "HEAD")
			buildTime := time.Now().UTC().Format(time.RFC3339)
			ldflagsAPI := "-s -w" +
				" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Version=" + shortSHA +
				" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Commit=" + fullSHA +
				" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.BuildTime=" + buildTime
			ldflagsAgent := "-s -w -X main.version=" + shortSHA

			// Low-RAM hosts build one binary at a time: three concurrent Go
			// builds at GOMEMLIMIT=900MiB each overshoot a 2 GB box and the
			// update dies mid-build (see serializeGoBuildsForMB).
			serialBuilds := serializeGoBuilds()
			var wg sync.WaitGroup
			spawn := func(fn func()) {
				if serialBuilds {
					fn()
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					fn()
				}()
			}
			if serialBuilds {
				fmt.Println("  (low RAM: building binaries one at a time)")
			}
			var apiErr, agentErr error
			if !apiSkip {
				spawn(func() {
					apiArgs := []string{"build", "-trimpath"}
					if demoBuild {
						apiArgs = append(apiArgs, "-tags", "demo")
					}
					apiArgs = append(apiArgs, "-ldflags", ldflagsAPI, "-o", repoDir+"/bin/jabali-panel.new", "./panel-api/cmd/server")
					apiErr = asUser(repoDir, goBin, apiArgs...)
				})
			}
			if !agentSkip {
				spawn(func() {
					agentErr = asUser(repoDir, goBin, "build", "-trimpath", "-ldflags", ldflagsAgent,
						"-o", repoDir+"/bin/jabali-agent.new", "./panel-agent/cmd/jabali-agent")
				})
			}
			// jabali-ssh-shell is a tiny binary built from the same
			// panel-agent module; always rebuild it when buildSteps runs
			// (this whole step is skipped on the fast-path no-op, so the
			// cost is only paid on a real update). It is the single SSH
			// login-shell wrapper across all install/update paths — the
			// old install/ssh/jabali-ssh-shell bash script was retired in
			// favour of this binary (filtered passwd + -c forwarding).
			var sshShellErr error
			if !agentSkip {
				spawn(func() {
					sshShellErr = asUser(repoDir, goBin, "build", "-trimpath", "-ldflags", ldflagsAgent,
						"-o", repoDir+"/bin/jabali-ssh-shell.new", "./panel-agent/cmd/jabali-ssh-shell")
				})
			}
			// jabali-mailhook: the loopback MTA-hook service (disclaimers, GH #233).
			// Same panel-agent module; rebuild alongside the agent/ssh-shell.
			var mailhookErr error
			if !agentSkip {
				wg.Add(1)
				go func() {
					defer wg.Done()
					mailhookErr = asUser(repoDir, goBin, "build", "-trimpath", "-ldflags", ldflagsAgent,
						"-o", repoDir+"/bin/jabali-mailhook.new", "./panel-agent/cmd/jabali-mailhook")
				}()
			}
			wg.Wait()
			if apiErr != nil {
				return fmt.Errorf("panel-api: %w", apiErr)
			}
			if agentErr != nil {
				return fmt.Errorf("panel-agent: %w", agentErr)
			}
			if sshShellErr != nil {
				return fmt.Errorf("jabali-ssh-shell: %w", sshShellErr)
			}
			if mailhookErr != nil {
				return fmt.Errorf("jabali-mailhook: %w", mailhookErr)
			}
			// Persist the sha only on the side we just rebuilt; skipped
			// side keeps its existing sha file untouched.
			if !apiSkip {
				writeArtifactSHA("panel-api-bin", apiInputs)
			}
			if !agentSkip {
				writeArtifactSHA("panel-agent-bin", agentInputs)
			}
			return nil
		}},
		{"install binaries", func() error {
			if installedFromRelease {
				fmt.Println("  (skipped: binaries already installed from release tarball)")
				return nil
			}
			snapshotBeforeSwap()
			// Skip per-binary when its .new file is absent — the parallel
			// build step short-circuited that side because its inputs
			// hadn't changed. The currently-installed binary stays.
			apiNew := repoDir + "/bin/jabali-panel.new"
			agentNew := repoDir + "/bin/jabali-agent.new"
			if _, err := os.Stat(apiNew); err == nil {
				if err := run("", "install", "-m", "0755", apiNew, defaultPanelBinPath); err != nil {
					return err
				}
				_ = os.Remove(apiNew)
			}
			if _, err := os.Stat(agentNew); err == nil {
				if err := run("", "install", "-m", "0755", agentNew, defaultAgentBinPath); err != nil {
					return err
				}
				_ = os.Remove(agentNew)
			}
			sshShellNew := repoDir + "/bin/jabali-ssh-shell.new"
			if _, err := os.Stat(sshShellNew); err == nil {
				if err := run("", "install", "-m", "0755", "-o", "root", "-g", "root", sshShellNew, "/usr/local/bin/jabali-ssh-shell"); err != nil {
					return err
				}
				_ = os.Remove(sshShellNew)
			}
			mailhookNew := repoDir + "/bin/jabali-mailhook.new"
			if _, err := os.Stat(mailhookNew); err == nil {
				if err := run("", "install", "-m", "0755", "-o", "root", "-g", "root", mailhookNew, "/usr/local/bin/jabali-mailhook"); err != nil {
					return err
				}
				_ = os.Remove(mailhookNew)
			}
			// Idempotent ergonomic alias: `jabali` → `jabali-panel`.
			// install.sh creates this on fresh installs; update.go refreshes it
			// on every upgrade in case it got clobbered.
			_ = run("", "ln", "-sf", defaultPanelBinPath, "/usr/local/bin/jabali")
			return nil
		}},
		{"re-render appsec config (post-build)", func() error {
			// The earlier "reconcile crowdsec appsec config" buildStep runs
			// BEFORE the binary is rebuilt + installed (just above), so an
			// AppSec change shipped in THIS build (a CRS exclusion in
			// internal/appseccfg — GH #594) wouldn't render until the NEXT
			// update. Re-run render-config NOW with the freshly-installed
			// binary, and reload crowdsec if the YAML *or* the CRS before-
			// plugin changed (render-config writes both; the earlier step only
			// watched the YAML, so a before.conf-only change never reloaded).
			if _, err := os.Stat("/etc/crowdsec"); err != nil {
				return nil // crowdsec not installed
			}
			yamlPath := "/etc/crowdsec/appsec-configs/jabali-appsec.yaml"
			beforePath := "/var/lib/crowdsec/data/crs-plugins/jabali/jabali-before.conf"
			yBefore, _ := os.ReadFile(yamlPath)
			cBefore, _ := os.ReadFile(beforePath)
			if err := run("", defaultPanelBinPath, "appsec", "render-config", "--reconcile"); err != nil {
				fmt.Printf("  (post-build appsec render-config failed: %v — continuing)\n", err)
				return nil
			}
			yAfter, _ := os.ReadFile(yamlPath)
			cAfter, _ := os.ReadFile(beforePath)
			if string(yBefore) != string(yAfter) || string(cBefore) != string(cAfter) {
				fmt.Println("  (appsec config changed post-build — reloading crowdsec)")
				_ = run("", "bash", "-c", "systemctl reload crowdsec 2>/dev/null || systemctl restart crowdsec || true")
			}
			return nil
		}},
		{"setup jabali-mailhook service", func() error {
			// The MTA-hook disclaimer service (GH #233) is set up only in
			// install.sh main() on fresh installs; run its idempotent
			// installer here too so existing hosts get the token + unit +
			// service on update. Must run AFTER the binary is installed
			// (above) — provision_new_software runs before binaries, so it
			// can't do this. Best-effort: a failure shouldn't abort the
			// update (the reconciler registers the Stalwart hook once the
			// service is up).
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil // dev environment, no install.sh
			}
			if !mailModuleInstalled() {
				fmt.Println("  (mail module not installed on this host — skipping)")
				return nil
			}
			if err := run("", "bash", "-c", "source "+installSh+" && install_jabali_mailhook"); err != nil {
				fmt.Printf("  (install_jabali_mailhook failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"run migrations", func() error {
			if err := run("", defaultPanelBinPath, "migrate", "up"); err != nil {
				// The binaries were swapped several steps ago; the schema is
				// still where it started. Undo the half that we can undo
				// rather than leaving new code on disk in front of an old
				// schema, where the next restart runs the mismatch (JAB-210).
				return rollbackAfterFailedMigrate(preUpdateBinaries, err, func(format string, args ...any) {
					fmt.Printf("  "+format+"\n", args...)
				})
			}
			// Past this point the binary and the schema agree, so the
			// snapshot is only taking up disk.
			preUpdateBinaries.cleanup()
			preUpdateBinaries = nil
			return nil
		}},
		{"restart services", func() error {
			if err := run("", "systemctl", "restart", "jabali-agent"); err != nil {
				return err
			}
			return run("", "systemctl", "restart", "jabali-panel")
		}},
		{"verify NTP time sync", func() error {
			// TOTP enrolment quietly breaks when the wall clock drifts
			// > 30s from real time (every code the authenticator app
			// generates falls outside the validation window). Re-run
			// install_time_sync on every update so a host whose NTP
			// daemon got disabled or a fresh-image clone whose clock
			// skewed re-converges automatically. Non-fatal — install
			// just warns if not synchronised yet.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_time_sync"); err != nil {
				fmt.Printf("  (install_time_sync failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync nginx panel vhost + websocket map", func() error {
			// Re-render /etc/nginx/sites-available/jabali-panel.conf
			// from the template, and (re-)install the WebSocket upgrade
			// map snippet at /etc/nginx/conf.d/jabali-websocket-map.conf.
			// Required for log streaming WS endpoints to work — without
			// the map, $connection_upgrade is empty and nginx -t fails;
			// without $http_host on Host header, backend builds WS URLs
			// without the :8443 port.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_nginx_websocket_map && install_nginx_panel_vhost"); err != nil {
				fmt.Printf("  (install_nginx_panel_vhost failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync bulwark systemd + env", func() error {
			// Re-install the jabali-webmail.service unit file, the
			// server-unix.js wrapper, the nginx upstream snippet, and
			// re-render /etc/jabali-panel/bulwark.env from repo so
			// changes (e.g. Restart=always, NODE_TLS_REJECT_UNAUTHORIZED)
			// reach existing hosts without a full install.sh run.
			// _install_bulwark_systemd is idempotent: install -m no-ops
			// when content matches; daemon-reload runs unconditionally;
			// _install_bulwark_env (called at the tail) restarts webmail
			// only when the env content actually changed. Tarball stays
			// untouched. Failure is non-fatal — old unit keeps working.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if !mailModuleInstalled() {
				fmt.Println("  (mail module not installed on this host — skipping)")
				return nil
			}
			// Version-bump aware: install_bulwark writes the pinned version to
			// /opt/jabali-webmail/VERSION. If the repo pins a newer Bulwark than
			// what's installed, re-run the FULL install_bulwark (download + verify
			// + swap) so a bump (GH #325) actually reaches existing hosts — the
			// systemd/env sync alone never touches the tarball. Otherwise just
			// re-sync the unit/env (idempotent).
			if err := run("", "bash", "-c",
				"set -e; source "+installSh+"; "+
					"want=$(grep -m1 'local bulwark_version=' "+installSh+" | sed -E 's/.*\"([^\"]+)\".*/\\1/'); "+
					"have=$(cat /opt/jabali-webmail/VERSION 2>/dev/null || true); "+
					"if [ -n \"$want\" ] && [ \"$want\" != \"$have\" ]; then "+
					"echo \"  bulwark: installed=[$have] pinned=[$want] — reinstalling tarball\"; install_bulwark; "+
					"else _install_bulwark_systemd; fi"); err != nil {
				fmt.Printf("  (bulwark sync/upgrade failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync stalwart binary", func() error {
			// install.sh installs the Stalwart SERVER binary only on a fresh
			// install; the update path re-copies the unit + push-cert (above)
			// but never re-downloaded the binary, so a version bump (GH #525)
			// never reached existing hosts — they stayed on the originally-
			// installed version. upgrade_stalwart_binary is idempotent: a no-op
			// when already at the pinned STALWART_VERSION, else it downloads +
			// checksum-verifies + atomically swaps the binary and restarts
			// jabali-stalwart. Non-fatal — a host that can't upgrade keeps
			// running the old binary rather than failing the whole update.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if !mailModuleInstalled() {
				fmt.Println("  (mail module not installed on this host — skipping)")
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && upgrade_stalwart_binary"); err != nil {
				fmt.Printf("  (stalwart binary upgrade failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync stalwart spam-filter rules", func() error {
			// Vendor the pinned spam-filter rules bundle + (re-)install
			// the weekly auto-refresh timer + script. Existing hosts
			// without these (installed before feat/stalwart-spam-filter-pin)
			// still hit github.com/stalwartlabs/spam-filter at every
			// Stalwart cold start; this convergence step pins them to
			// /opt/stalwart/share/spam-filter-rules.json.gz once and
			// arms the timer to keep it refreshed.
			//
			// Apply-plan re-render + stalwart-cli re-apply is intentionally
			// skipped here — apply is create-only for some objects and the
			// SpamSettings update lives in the install-time plan path.
			// Operators on a stale apply-plan can re-run install.sh to
			// converge that. Non-fatal here either way.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if !mailModuleInstalled() {
				fmt.Println("  (mail module not installed on this host — skipping)")
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && _install_spam_rules"); err != nil {
				fmt.Printf("  (_install_spam_rules failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync kratos config", func() error {
			// install_kratos is idempotent: if the binary is already at
			// the target version it skips the download but still
			// re-renders /etc/jabali-panel/kratos.yml from the template,
			// detects content drift via cmp, and restarts
			// jabali-kratos.service when the rendered config changed.
			//
			// Without this step, kratos.yml.tmpl edits (e.g. the
			// selfservice.methods.code.enabled flip in 8c93811 that the
			// v4 debug report flagged) reach the on-disk config but
			// never the running process — operator has to ssh in and
			// `systemctl restart jabali-kratos` by hand.
			//
			// Failure is logged but doesn't block the update; the
			// running Kratos keeps serving the old config.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_kratos"); err != nil {
				fmt.Printf("  (install_kratos failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync malware stack", func() error {
			// Source install.sh and re-run install_malware_stack so existing
			// hosts converge on amendments to the M33 stack (ADR-0072) without
			// manual SSH. Runs unconditionally — the function is fully
			// idempotent (LMD install gated by version marker; clamav apt
			// install gated by `command -v clamscan`; PMF tarball gated by
			// presence of php.yar; systemd unit writes are install -m no-ops
			// when content matches).
			//
			// install.sh has a BASH_SOURCE guard at its tail so sourcing it
			// does NOT trigger main()'s full install — only the named
			// function runs.
			//
			// Failure here does not block the update — malware stack is a
			// post-deploy convergence step, not a service the panel depends
			// on. Log and move on.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_malware_stack"); err != nil {
				fmt.Printf("  (install_malware_stack failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync PHP Defense (Snuffleupagus)", func() error {
			// M41 (ADR-0088). Re-runs install_snuffleupagus so the per-PHP
			// minor sp.so + conf.d wiring + rule bundle mirror converge on
			// `jabali update`. Idempotent — build.sh skips minors already
			// at the pinned tag (.jabali-version stamp); apt install of
			// phpX.Y-dev / build-essential / libpcre2-dev short-circuits
			// when already present.
			//
			// Without this step, fresh installs that were missing phpX.Y-dev
			// at install_snuffleupagus time stayed permanently broken until
			// manual intervention — rules + DB migrations landed but sp.so
			// never built. install.sh's main() runs install_snuffleupagus
			// once at install time only; update flow needs its own hook.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_snuffleupagus"); err != nil {
				fmt.Printf("  (install_snuffleupagus failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"sync adminer", func() error {
			// M37 Phase 4 — Adminer SSO bridge. install_adminer is
			// idempotent: re-runs are no-ops (file existence check
			// + install -m), so it's safe to call on every update.
			// Failure is logged but doesn't block the update.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c",
				"source "+installSh+" && install_adminer"); err != nil {
				fmt.Printf("  (install_adminer failed: %v — continuing)\n", err)
			}
			return nil
		}},
		{"self-heal nginx http2-on (PR#16/ADR-0066)", func() error {
			// jabali update mirrors install.sh halves but does NOT
			// re-render the nginx server-scope vhosts, so a host that
			// rendered the brief `http2 on;` template (an unknown
			// directive on nginx<1.25.1 -> `nginx -t` fails) stays
			// broken across every update. Run the idempotent
			// nginx-config-invalid repair (foldHTTP2: `http2 on;` ->
			// `listen ... ssl http2`) so update self-heals it.
			// Detect-gated: no-op + no reload when already valid.
			if broken, detail, derr := detectNginxConfigInvalid(repairCtx{}); derr == nil && broken {
				fmt.Printf("  nginx-config-invalid: %s -- folding http2\n", detail)
				return fixNginxConfigInvalid(repairCtx{})
			}
			return nil
		}},
		{"self-heal crowdsec BOUNCING_ON_TYPE (GH #212 log spam)", func() error {
			// The nginx Lua bouncer rejects the firewall-bouncer comma-list
			// `ban,captcha` and falls back to `ban`, spamming the nginx error
			// log every reload. Idempotent: only rewrites + reloads when the
			// stale value is present.
			return run("", "bash", "-c",
				`c=/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf; `+
					`if [ -f "$c" ] && grep -q '^BOUNCING_ON_TYPE=ban,captcha' "$c"; then `+
					`sed -i 's/^BOUNCING_ON_TYPE=ban,captcha$/BOUNCING_ON_TYPE=ban/' "$c"; `+
					`nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true; `+
					`echo "  fixed BOUNCING_ON_TYPE (was ban,captcha -> ban)"; `+
					`fi`)
		}},
		{"remove panel user from docker group (#487: root-equivalent)", func() error {
			// install_docker_engine is opt-in and doesn't run on every update,
			// so the docker-group strip there never reaches existing hosts.
			// Do it here on every update: derive the actual service user from
			// the unit, drop it from the docker group (idempotent), and
			// restart so the running process loses the supplementary group.
			return run("", "bash", "-c",
				`u="$(systemctl show -p User --value jabali-panel.service 2>/dev/null)"; [ -z "$u" ] && u=jabali; `+
					`if id -nG "$u" 2>/dev/null | tr ' ' '\n' | grep -qx docker; then `+
					`gpasswd -d "$u" docker >/dev/null 2>&1 || true; `+
					`systemctl try-restart jabali-panel.service >/dev/null 2>&1 || true; `+
					`echo "  removed $u from docker group (#487)"; `+
					`fi`)
		}},
		{"harden /proc hidepid (Gitea #499)", func() error {
			// jabali update doesn't run install.sh main(), so the hidepid mount
			// (which hides other users' /proc cmdline) only reaches fresh
			// installs without this heal. Idempotent; no-op when already set or
			// when the container denies the remount.
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c", "source "+installSh+" && harden_proc_hidepid"); err != nil {
				fmt.Printf("  (harden_proc_hidepid failed: %v -- continuing)\n", err)
			}
			return nil
		}},
		{"reapply PHP pools from template (GH #401)", func() error {
			// The pool template was re-synced above, but ReconcilePHPPools
			// skips ACTIVE pools, so template hardening (e.g. the GH #401
			// disable_functions default) never reaches existing tenants on
			// its own. Flip every active pool to pending so the next
			// reconciler tick re-renders it from the fresh template. Runs
			// the just-installed binary; non-fatal (pools also re-render on
			// any later pool change). Idempotent.
			if err := run("", defaultPanelBinPath, "php", "pool", "reapply-all"); err != nil {
				fmt.Printf("  (pool reapply failed: %v -- pools will pick up the template on next change)\n", err)
			}
			return nil
		}},
		{"migrate CrowdSec LAPI DB SQLite → MariaDB (CPU fix)", func() error {
			// SQLite pegged crowdsec at high CPU under the CAPI community
			// blocklist (~15k decisions serialized on SQLite's global lock,
			// profiled to cgo sqlite3_step). configure_crowdsec_mariadb moves
			// the LAPI DB to the panel MariaDB (InnoDB row-locking, off-process
			// queries). Idempotent — no-op once already on mysql. Sources the
			// just-pulled install.sh; best-effort (crowdsec keeps SQLite on
			// failure).
			installSh := repoDir + "/install.sh"
			if _, err := os.Stat(installSh); err != nil {
				return nil
			}
			if err := run("", "bash", "-c", "source "+installSh+" && configure_crowdsec_mariadb"); err != nil {
				fmt.Printf("  (crowdsec MariaDB migration failed: %v -- staying on SQLite)\n", err)
			}
			return nil
		}},
		{"refresh jabali-cache plugin on cache-enabled sites (GH #613)", func() error {
			// WordPress.org is the canonical plugin source (#613); existing
			// cache-enabled sites only pick up a newly-published version on a
			// cache re-toggle. Sweep them here so `jabali update` bumps every
			// site to the latest WordPress.org release. Runs the just-installed
			// binary + restarted agent (so the wordpress.cache_plugin_refresh
			// verb exists); best-effort + idempotent (a current site is a no-op).
			if err := run("", defaultPanelBinPath, "app", "refresh-cache-plugin"); err != nil {
				fmt.Printf("  (cache plugin refresh failed: %v -- sites keep their current plugin version)\n", err)
			}
			return nil
		}},
	}

	for _, s := range prelude {
		fmt.Printf("→ %s\n", s.name)
		if err := s.fn(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}

	// Fast path: we already fully rebuilt this SHA. The build + restart
	// cycle would do ~30-60 s of CPU work + bounce services, all for a
	// no-op. Skip unless the operator asked for a forced rebuild.
	//
	// Self-heal: if a PREVIOUS update advanced HEAD but failed before
	// last-built-sha was written, lastBuilt stays at the old SHA (or ""
	// on a fresh host) and we re-run the build chain — which is what
	// the operator wanted. The earlier implementation compared
	// preHEAD==postHEAD, which would have skipped the rebuild in that
	// stuck state, requiring --force to recover.
	lastBuilt, err := readLastBuiltSHA()
	if err != nil {
		return err
	}
	// Self-verify before honoring the marker. The marker says "HEAD
	// was successfully built into the installed binary"; if the binary
	// is OLDER than the HEAD commit (hand-replaced, half-applied prior
	// build, marker hand-edited, …) the marker is lying and we must
	// rebuild even when SHAs match. Catches the f08a97eb-class
	// regression where source-on-disk and installed binary fell out of
	// sync but the marker stayed pinned to HEAD, making `--force` the
	// only escape — and any operator running an older CLI without the
	// `&& !force` clause was permanently stuck.
	binStale := false
	if binInfo, statErr := os.Stat(defaultPanelBinPath); statErr == nil {
		commitTimeRaw, _ := exec.Command("sudo", "-u", serviceUser,
			"git", "-C", repoDir, "show", "-s", "--format=%cI", postHEAD).Output()
		if ct := strings.TrimSpace(string(commitTimeRaw)); ct != "" {
			if t, perr := time.Parse(time.RFC3339, ct); perr == nil && binInfo.ModTime().Before(t) {
				binStale = true
			}
		}
	} else {
		// Binary missing entirely — rebuild.
		binStale = true
	}
	if lastBuilt == postHEAD && !force && !binStale {
		shortSHA := postHEAD
		if len(shortSHA) >= 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Printf("\n✓ Already up to date (HEAD=%s). Skipped rebuild.\n", shortSHA)
		fmt.Println("  Run `jabali update --force` to rebuild and restart anyway.")
		return nil
	}
	if binStale && !force && lastBuilt == postHEAD {
		fmt.Println("  (last-built-sha matches HEAD but installed binary is older than the HEAD commit — forcing rebuild)")
	}

	for _, s := range buildSteps {
		fmt.Printf("→ %s\n", s.name)
		if err := s.fn(); err != nil {
			// The prelude already advanced the checkout to postHEAD, but this
			// build/install step failed — so the RUNNING binary is unchanged
			// (still lastBuilt) while the code on disk is at postHEAD. Make
			// that half-state explicit and flag the most common cause (the
			// go/vite build getting OOM-killed on a low-RAM box), so the
			// operator knows the binary did NOT update and simply re-runs
			// after freeing memory — instead of a bare "signal: killed" that
			// reads as noise (GH #760 follow-up).
			oomHint := ""
			if isLikelyOOM(err) {
				oomHint = "\n  Looks like the build was killed for memory (OOM). Free RAM or add swap, then re-run `jabali update`."
			}
			fmt.Fprintf(os.Stderr,
				"\n✗ Update FAILED at %q.\n  The running binary was NOT changed (still %s); the code is now at %s.\n  Re-run `jabali update` after fixing the cause.%s\n",
				s.name, shortSHA7(lastBuilt), shortSHA7(postHEAD), oomHint)
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}

	// Record the SHA we just fully built + restarted against. Must be
	// the LAST thing we do — if any step above fails, we DON'T write,
	// and the next update retries the build chain automatically.
	if err := writeLastBuiltSHA(postHEAD); err != nil {
		// Don't fail the whole update for a cosmetic bookkeeping miss;
		// binaries + migrations + services are already updated. Next
		// run will simply rebuild once more (harmless).
		fmt.Printf("  (warning: could not persist last-built-sha: %v)\n", err)
	}

	fmt.Println("\n✓ Update complete.")
	return nil
}

// run executes a command with inherited stdout/stderr so the user sees
// build output and errors in real time.
// groupExists reports whether a system group of the given name resolves.
// Split out from mailModuleInstalled so the gate logic is unit-testable
// without provisioning a real group.
func groupExists(name string) bool {
	_, err := user.LookupGroup(name)
	return err == nil
}

// mailModuleInstalled reports whether the Stalwart/mail module was provisioned
// on this host. install_stalwart creates the jabali-mail group up front (its
// service user's primary group) and every mail file is owned by it. On a
// Custom / no-mail install the group never exists, so re-running the mail
// convergence buildSteps (stalwart binary, spam-filter rules, mailhook,
// bulwark) on `jabali update` dies at `install -g jabali-mail` (GH #727).
// Update converges what is installed; enabling mail later is an explicit
// `--install-module mail` via Server Settings, which creates the group itself.
func mailModuleInstalled() bool {
	return groupExists("jabali-mail")
}

func run(dir string, name string, args ...string) error {
	c := exec.Command(name, args...)
	if dir != "" {
		c.Dir = dir
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	// Inherit PATH + GOPATH etc but ensure our Go is first.
	c.Env = appendGoPath(os.Environ())
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func appendGoPath(env []string) []string {
	goRoot := os.Getenv("JABALI_GO_ROOT")
	if goRoot == "" {
		goRoot = defaultGoRoot
	}
	// Prepend Go bin to PATH so the right `go` is found.
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + goRoot + "/bin:" + e[5:]
			return env
		}
	}
	return append(env, "PATH="+goRoot+"/bin:/usr/bin:/bin")
}

// gitRevParseAsUser runs `git -C repoDir rev-parse <args...>` as
// serviceUser (matches the rest of the update flow which avoids "dubious
// ownership" by always shelling git through sudo). Returns ("unknown",
// err) on failure so callers can ship a best-effort build-info value
// rather than aborting the whole build.
func gitRevParseAsUser(repoDir, serviceUser string, args ...string) (string, error) {
	cmdArgs := append([]string{"-u", serviceUser, "git", "-C", repoDir, "rev-parse"}, args...)
	out, err := exec.Command("sudo", cmdArgs...).Output()
	if err != nil {
		return "unknown", err
	}
	return strings.TrimSpace(string(out)), nil
}
