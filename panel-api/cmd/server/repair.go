package main

import (
	"bufio"
	"errors"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

// jabali repair — self-heal subcommand for known recurring scars on a
// deployment host. Each repair encapsulates one detector + one fix; the
// detector reports whether the host is currently broken in that specific
// way, and the fix puts it back to a known-good state.
//
// New scars get a new repairStep entry. The list lives in this file so
// the truth of "what jabali knows how to fix automatically" is in one
// place reachable from `jabali repair --diagnose`.
//
// ADR-0077.

type repairStep struct {
	// id is the kebab-case selector exposed via flags (e.g. --git-ownership).
	id string

	// label is the human-readable line printed during diagnose / repair.
	label string

	// destructive=true repairs touch operator data (re-clone, rm -rf
	// node_modules, etc). They run only with --all + --yes, or with their
	// explicit --<id> flag. --auto skips them.
	destructive bool

	// detect returns (broken, detail, err). detail is a short string used
	// in the diagnose output. err means the detector itself blew up.
	detect func(ctx repairCtx) (bool, string, error)

	// fix mutates host state to clear the broken condition. Should be
	// idempotent — calling fix twice when not broken must be a no-op.
	fix func(ctx repairCtx) error
}

type repairCtx struct {
	repoDir     string
	serviceUser string
	yes         bool
}

func newRepairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Detect and fix known deployment-host issues",
		Long: `Run a series of detectors and (optionally) self-healing fixes for
recurring deployment-host issues. Useful when 'jabali update' fails or a
host is in a state that would block the next update.

Modes:
  jabali repair --diagnose       Report what is broken; change nothing.
  jabali repair --auto           Fix every safe (non-destructive) issue.
  jabali repair --all --yes      Fix everything, including destructive
                                  repairs (e.g. re-clone /opt/jabali-panel).
  jabali repair --<id> [...]     Fix one or more specific repairs by id;
                                  see --diagnose output for available ids.

Destructive repairs require either --all together with --yes, or the
specific --<id> flag together with --yes. Without --yes they prompt
interactively before touching anything irreversible.`,
		SilenceUsage: true,
		RunE:         runRepair,
	}
	cmd.Flags().Bool("diagnose", false, "Report broken conditions without fixing")
	cmd.Flags().Bool("auto", false, "Fix every non-destructive (safe) issue")
	cmd.Flags().Bool("all", false, "Fix every issue including destructive ones")
	cmd.Flags().Bool("yes", false, "Skip interactive confirmation for destructive repairs")
	for _, s := range repairSteps() {
		cmd.Flags().Bool(s.id, false, fmt.Sprintf("Fix only: %s", s.label))
	}
	return cmd
}

func runRepair(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("jabali repair must run as root (try: sudo jabali repair ...)")
	}

	ctx := repairCtx{
		repoDir:     envOr("JABALI_REPO_DIR", defaultRepoDir),
		serviceUser: envOr("JABALI_SERVICE_USER", "jabali"),
	}
	ctx.yes, _ = cmd.Flags().GetBool("yes")

	diagnose, _ := cmd.Flags().GetBool("diagnose")
	auto, _ := cmd.Flags().GetBool("auto")
	all, _ := cmd.Flags().GetBool("all")

	steps := repairSteps()

	// Pick which steps the operator selected.
	selected := map[string]bool{}
	anySpecific := false
	for _, s := range steps {
		if v, _ := cmd.Flags().GetBool(s.id); v {
			selected[s.id] = true
			anySpecific = true
		}
	}

	if !diagnose && !auto && !all && !anySpecific {
		// No flags = same as --diagnose. Defaulting to "do nothing" is
		// the safer choice than defaulting to "fix everything" for an
		// operator who typed `jabali repair` to see what it does.
		diagnose = true
		fmt.Println("(no mode flag — defaulting to --diagnose; pass --auto, --all, or --<id> to apply fixes)")
	}

	// Run every detector first. Even in fix mode, surfacing the full
	// list before mutating state lets the operator see the plan.
	type result struct {
		step   repairStep
		broken bool
		detail string
		err    error
	}
	var results []result
	for _, s := range steps {
		broken, detail, err := s.detect(ctx)
		results = append(results, result{s, broken, detail, err})
	}

	fmt.Println("Diagnostics:")
	anyBroken := false
	for _, r := range results {
		marker := "  ✓"
		state := "OK"
		switch {
		case r.err != nil:
			marker = "  !"
			state = fmt.Sprintf("detect failed: %v", r.err)
		case r.broken:
			marker = "  ✗"
			state = "BROKEN"
			if r.detail != "" {
				state = "BROKEN — " + r.detail
			}
			anyBroken = true
		}
		safety := ""
		if r.step.destructive {
			safety = " [destructive]"
		}
		fmt.Printf("%s [%s] %s%s\n     %s\n",
			marker, r.step.id, r.step.label, safety, state)
	}

	if diagnose {
		if !anyBroken {
			fmt.Println("\nNo issues detected.")
		} else {
			fmt.Println("\nRun `jabali repair --auto` to fix safe issues, or " +
				"`jabali repair --all --yes` to also apply destructive fixes.")
		}
		return nil
	}

	// Decide which steps to actually fix.
	toFix := []repairStep{}
	for _, r := range results {
		if r.err != nil || !r.broken {
			continue
		}
		switch {
		case anySpecific:
			if selected[r.step.id] {
				toFix = append(toFix, r.step)
			}
		case all:
			toFix = append(toFix, r.step)
		case auto:
			if !r.step.destructive {
				toFix = append(toFix, r.step)
			}
		}
	}

	if len(toFix) == 0 {
		if anyBroken {
			fmt.Println("\n(no repairs selected — pass --auto, --all, or --<id>)")
		} else {
			fmt.Println("\nNothing to fix.")
		}
		return nil
	}

	for _, s := range toFix {
		if s.destructive && !ctx.yes {
			ok, err := confirm(fmt.Sprintf("Apply destructive repair %q? This may overwrite host state.", s.id))
			if err != nil {
				return err
			}
			if !ok {
				fmt.Printf("  [%s] skipped (declined)\n", s.id)
				continue
			}
		}
		fmt.Printf("\n→ [%s] %s\n", s.id, s.label)
		if s.fix == nil {
			// Detect-only step: the condition is real but clearing it needs a
			// judgement call this command must not make on the operator's
			// behalf (see dirty-migration). Report and move on rather than
			// pretending it was repaired.
			fmt.Printf("  ! %s needs a manual decision — see the detail above\n", s.id)
			continue
		}
		if err := s.fix(ctx); err != nil {
			return fmt.Errorf("repair %s: %w", s.id, err)
		}
		fmt.Printf("  ✓ %s applied\n", s.id)
	}

	fmt.Println("\n✓ Repair pass complete. Re-run `jabali update` to continue.")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func confirm(prompt string) (bool, error) {
	fmt.Printf("%s [y/N] ", prompt)
	scan := bufio.NewScanner(os.Stdin)
	if !scan.Scan() {
		if err := scan.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	ans := strings.TrimSpace(strings.ToLower(scan.Text()))
	return ans == "y" || ans == "yes", nil
}

// repairSteps lists every known repair. Order matters: detectors run
// top-to-bottom, so cheap-and-blocking checks (.git pointer corruption)
// come before expensive ones (node_modules .bin/tsc check), and ownership
// fixes come before any check that would itself fail without ownership.
func repairSteps() []repairStep {
	return []repairStep{
		{
			// Runs before the general dirty-migration detector: this is the ONE
			// dirty state we can recover automatically and safely, so fix it
			// rather than only reporting it (GH #1094 / #1103). Precisely gated —
			// see detectBrokenFtp264.
			id:     "dirty-ftp-264",
			label:  "schema half-applied migration 264 (GH #1094 FTP row-size) — panel-api cannot start",
			detect: detectBrokenFtp264,
			fix:    fixBrokenFtp264,
		},
		{
			// A dirty schema means the panel is already down, which makes every
			// other finding secondary. (The recoverable 264 case above is caught
			// first; anything reaching here is operator-driven.)
			id:     "dirty-migration",
			label:  "database schema is dirty — panel-api cannot start",
			detect: detectDirtyMigration,
			// No fix: see detectDirtyMigration. Forcing the wrong direction
			// silently skips schema changes, so this stays operator-driven.
			fix: nil,
		},
		{
			// GH #889/#953: a crash-looping per-user PHP-FPM master 500/502s
			// every site it serves. Name the cause here instead of asking each
			// reporter to paste `journalctl -u jabali-fpm@<user>`.
			id:     "fpm-masters-down",
			label:  "a per-user PHP-FPM master is crash-looping (sites 500/502 with missing fpm.sock)",
			detect: detectFPMMastersDown,
			// Detect-only: the cause is a bad extension / php.ini / AppArmor
			// deny / Snuffleupagus rule; a blind restart won't clear it and
			// would mask the real problem. The reason string points at it.
			fix: nil,
		},
		{
			id:          "git-pointer",
			label:       "/opt/jabali-panel/.git is a corrupted worktree pointer",
			destructive: true,
			detect:      detectGitPointer,
			fix:         fixGitPointer,
		},
		{
			id:     "git-ownership",
			label:  "/opt/jabali-panel/.git owned by wrong user",
			detect: detectGitOwnership,
			fix:    fixGitOwnership,
		},
		{
			id:     "git-stale-worktrees",
			label:  "/opt/jabali-panel/.git/worktrees has stale entries",
			detect: detectGitStaleWorktrees,
			fix:    fixGitStaleWorktrees,
		},
		{
			id:     "uploads-dir",
			label:  "/var/lib/jabali-uploads missing or wrong perms",
			detect: detectUploadsDir,
			fix:    fixUploadsDir,
		},
		{
			id:     "etc-jabali-perms",
			label:  "/etc/jabali not traversable by hosting users (SSH/SFTP locked out — sandbox-mode unreadable)",
			detect: detectEtcJabaliPerms,
			fix:    fixEtcJabaliPerms,
		},
		{
			id:     "ondrej-nginx-ppa",
			label:  "stale ondrej/nginx PPA in apt sources (404 on noble)",
			detect: detectOndrejPPA,
			fix:    fixOndrejPPA,
		},
		{
			id:          "node-modules",
			label:       "panel-ui/node_modules partial (missing .bin/tsc)",
			destructive: true,
			detect:      detectNodeModules,
			fix:         fixNodeModules,
		},
		{
			id:     "daemon-reload",
			label:  "systemd has unloaded unit-file changes on disk",
			detect: detectDaemonReload,
			fix:    fixDaemonReload,
		},
		{
			id:     "orphan-slices",
			label:  "jabali-user-*.slice units exist for deleted unix users",
			detect: detectOrphanSlices,
			fix:    fixOrphanSlices,
		},
		{
			id:     "crowdsec-bouncer-key",
			label:  "crowdsec-firewall-bouncer crash-loops with stale LAPI key",
			detect: detectCrowdSecBouncerKey,
			fix:    fixCrowdSecBouncerKey,
		},
		{
			id:     "apparmor-profiles-missing",
			label:  "jabali AppArmor profiles absent from /etc/apparmor.d/",
			detect: detectAppArmorProfilesMissing,
			fix:    fixAppArmorProfilesMissing,
		},
		{
			id:     "apparmor-profiles-disabled",
			label:  "jabali AppArmor profiles exist but are disabled",
			detect: detectAppArmorProfilesDisabled,
			fix:    fixAppArmorProfilesDisabled,
		},
		{
			id:          "orphan-migration-staging",
			label:       "/var/lib/jabali-migrations/* dirs for jobs already terminal in DB",
			detect:      detectOrphanMigrationStaging,
			fix:         fixOrphanMigrationStaging,
			destructive: true,
		},
		{
			id:     "nginx-config-invalid",
			label:  "jabali-default/jabali-panel.conf has `http2 on;` on nginx<1.25.1 (nginx -t fails, reloads rejected)",
			detect: detectNginxConfigInvalid,
			fix:    fixNginxConfigInvalid,
		},
		{
			id:     "nginx-missing-includes",
			label:  "panel :8443 vhost includes a missing optional snippet (phpMyAdmin/Adminer) — nginx -t fails, nothing on 8443 (GH #217)",
			detect: detectNginxMissingIncludes,
			fix:    fixNginxMissingIncludes,
		},
		{
			id:     "automation-443-include",
			label:  "panel-hostname :443 vhost missing the GH #1161 automation-API include (admin can't opt into API-on-443 for firewalled billing hosts)",
			detect: detectAutomation443Include,
			fix:    fixAutomation443Include,
		},
		{
			id:          "docroot-www-data-group",
			label:       "web docroot files not group www-data / dirs not setgid (nginx 403 on newly uploaded media)",
			destructive: true,
			detect:      detectDocrootGroup,
			fix:         fixDocrootGroup,
		},
		{
			id:     "bulwark-jwt-secret",
			label:  "Bulwark webmail-SSO secret poisoned / out of sync with bulwark.env (mail impersonation 'Invalid signature')",
			detect: detectBulwarkJWTSecret,
			fix:    fixBulwarkJWTSecret,
		},
	}
}

// ---------- git-pointer ----------
//
// Symptom: `.git` is a one-line FILE containing `gitdir: <abspath>` rather
// than a directory. Happens when an operator copies a worktree's `.git`
// pointer instead of the real repo, or when a partial rsync from a dev
// box's worktree lands those bytes on the deploy host. Result: every git
// command on the host fails with
//   fatal: not a git repository: <abspath-on-source-machine>
// and `jabali update` dies on the very first `git fetch`.

func detectGitPointer(ctx repairCtx) (bool, string, error) {
	gitPath := filepath.Join(ctx.repoDir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, fmt.Sprintf("%s does not exist", gitPath), nil
		}
		return false, "", err
	}
	if info.IsDir() {
		return false, "", nil
	}
	// Not a directory — read the contents. A worktree pointer file
	// is one short line `gitdir: <abspath>`.
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return false, "", err
	}
	content := strings.TrimSpace(string(b))
	if strings.HasPrefix(content, "gitdir:") {
		return true, "pointer file → " + strings.TrimSpace(strings.TrimPrefix(content, "gitdir:")), nil
	}
	return true, "non-directory non-pointer", nil
}

func fixGitPointer(ctx repairCtx) error {
	// Re-clone /opt/jabali-panel from origin while preserving operator
	// state that is intentionally NOT in git: node_modules, .cache,
	// .env, bin/. This mirrors the recovery snippet from the runbook.
	repo := ctx.repoDir
	backup := repo + ".broken"

	originURL, err := readOriginURL(repo)
	if err != nil {
		return fmt.Errorf("could not determine remote origin URL: %w", err)
	}

	// Move broken tree out of the way.
	if _, err := os.Stat(backup); err == nil {
		if err := run("", "rm", "-rf", backup); err != nil {
			return fmt.Errorf("clean stale %s: %w", backup, err)
		}
	}
	if err := run("", "mv", repo, backup); err != nil {
		return err
	}

	// Fresh clone.
	if err := run("", "git", "clone", originURL, repo); err != nil {
		return err
	}
	if err := run("", "chown", "-R",
		ctx.serviceUser+":"+ctx.serviceUser, repo); err != nil {
		return err
	}

	// Restore preserved untracked state. Each cp is best-effort so a
	// missing source from the broken tree doesn't abort the recovery.
	preserves := []string{
		".env",
		"panel-ui/node_modules",
		"panel-ui/dist",
		".cache",
		"bin",
	}
	for _, p := range preserves {
		src := filepath.Join(backup, p)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(repo, p)
		_ = run("", "cp", "-a", src, dst)
	}

	fmt.Printf("  (preserved tree kept at %s — delete after you confirm the new clone works)\n", backup)
	return nil
}

func readOriginURL(repo string) (string, error) {
	c := exec.Command("git", "-C", repo, "remote", "get-url", "origin")
	out, err := c.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	// Fallback: read .git/config directly. Useful when the gitdir pointer
	// is broken but .git/config still exists somewhere reachable.
	cfg, cerr := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if cerr != nil {
		// Last resort: try the broken-clone backup if we already moved it.
		cfg, cerr = os.ReadFile(filepath.Join(repo+".broken", ".git", "config"))
	}
	if cerr != nil {
		return "", err
	}
	for _, line := range strings.Split(string(cfg), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "url = ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "url = ")), nil
		}
	}
	return "", fmt.Errorf("origin URL not found in any git config")
}

// ---------- git-ownership ----------
//
// Symptom: `.git/objects/*` or `.git/FETCH_HEAD` is owned by root after
// a hand-run `git fetch` as root, so the next `jabali update` (which
// runs git as the jabali user) hits "permission denied" or
// "fatal: detected dubious ownership".

func detectGitOwnership(ctx repairCtx) (bool, string, error) {
	gitDir := filepath.Join(ctx.repoDir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil // git-pointer detector handles missing dir
		}
		return false, "", err
	}
	if !info.IsDir() {
		return false, "", nil // git-pointer detector owns this case
	}
	out, err := exec.Command("stat", "-c", "%U", gitDir).Output()
	if err != nil {
		return false, "", err
	}
	owner := strings.TrimSpace(string(out))
	if owner != ctx.serviceUser {
		return true, fmt.Sprintf("owner=%s expected=%s", owner, ctx.serviceUser), nil
	}
	return false, "", nil
}

func fixGitOwnership(ctx repairCtx) error {
	return run("", "chown", "-R",
		ctx.serviceUser+":"+ctx.serviceUser,
		filepath.Join(ctx.repoDir, ".git"))
}

// ---------- git-stale-worktrees ----------
//
// Symptom: `.git/worktrees/<name>/gitdir` files reference paths from a
// dev machine that don't exist on the deploy host. Git itself ignores
// missing worktrees on most operations, but `git worktree prune --expire
// now` keeps the dir clean and removes any stale config that could
// confuse downstream tooling.

func detectGitStaleWorktrees(ctx repairCtx) (bool, string, error) {
	wtRoot := filepath.Join(ctx.repoDir, ".git", "worktrees")
	entries, err := os.ReadDir(wtRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", err
	}
	if len(entries) == 0 {
		return false, "", nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return true, strings.Join(names, ","), nil
}

func fixGitStaleWorktrees(ctx repairCtx) error {
	// `git worktree prune --expire now` is the supported way to drop
	// every worktree subdir whose checkout path is missing — exactly
	// the case a deploy host hits.
	return run(ctx.repoDir, "git", "worktree", "prune", "--expire", "now")
}

// ---------- uploads-dir ----------
//
// Symptom: /var/lib/jabali-uploads missing → file uploads fail with
// "no such file or directory". The dir is created in install.sh on
// fresh installs but partial state can leave it absent.

func detectUploadsDir(ctx repairCtx) (bool, string, error) {
	const dir = "/var/lib/jabali-uploads"
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, "missing", nil
		}
		return false, "", err
	}
	if !info.IsDir() {
		return true, "exists but not a directory", nil
	}
	return false, "", nil
}

func fixUploadsDir(_ repairCtx) error {
	const dir = "/var/lib/jabali-uploads"
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Owner: root:jabali (panel-api writes via systemd ReadWritePaths,
	// jabali-agent reads to ingest). Ownership matches install.sh.
	return run("", "chown", "root:jabali", dir)
}

// ---------- ondrej-nginx-ppa ----------
//
// Symptom: apt update/install fails on Debian noble because the legacy
// ondrej/nginx PPA returns 404. Same scar as install.sh's strip step.

func detectOndrejPPA(_ repairCtx) (bool, string, error) {
	candidates := []string{
		"/etc/apt/sources.list.d/ondrej-ubuntu-nginx-noble.sources",
		"/etc/apt/sources.list.d/ondrej-ubuntu-nginx-noble.list",
		"/etc/apt/sources.list.d/ondrej-nginx.list",
	}
	var found []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = append(found, filepath.Base(p))
		}
	}
	if len(found) == 0 {
		return false, "", nil
	}
	return true, strings.Join(found, ","), nil
}

func fixOndrejPPA(_ repairCtx) error {
	return run("", "bash", "-c",
		"rm -f /etc/apt/sources.list.d/ondrej-ubuntu-nginx-noble.sources "+
			"/etc/apt/sources.list.d/ondrej-ubuntu-nginx-noble.list "+
			"/etc/apt/sources.list.d/ondrej-nginx.list")
}

// ---------- node-modules ----------
//
// Symptom: `panel-ui/node_modules/.bin/tsc` is missing — npm ci reported
// success but produced a partial install (or got interrupted). The repair
// reinstalls node_modules (wipe → npm ci → verify .bin/tsc) so the issue is
// actually cleared, not just wiped (GH #1173).

func detectNodeModules(ctx repairCtx) (bool, string, error) {
	tsc := filepath.Join(ctx.repoDir, "panel-ui", "node_modules", ".bin", "tsc")
	if _, err := os.Stat(tsc); err == nil {
		return false, "", nil
	}
	// Only flag broken if the lockfile is present — a fresh checkout
	// without npm ci yet is not a "repair" case.
	lock := filepath.Join(ctx.repoDir, "panel-ui", "package-lock.json")
	if _, err := os.Stat(lock); err != nil {
		return false, "", nil
	}
	return true, "node_modules/.bin/tsc missing despite package-lock.json", nil
}

func fixNodeModules(ctx repairCtx) error {
	// GH #1173: a bare `rm -rf node_modules` left .bin/tsc missing, so the
	// detector re-reported BROKEN the moment repair finished. Actually reinstall
	// via the shared resilient npm ci (wipe → npm ci → retry once → verify
	// .bin/tsc), the same path `jabali update` uses, run as the repo owner.
	return npmCIResilient(ctx.repoDir)
}

// ---------- daemon-reload ----------
//
// Symptom: systemd is running with an old version of one of jabali's
// unit files because someone installed a new version on disk but never
// ran `systemctl daemon-reload`. systemctl itself surfaces this via the
// per-unit `NeedDaemonReload` property when set to "yes".

func detectDaemonReload(_ repairCtx) (bool, string, error) {
	out, err := exec.Command("bash", "-c",
		"systemctl list-units --all --no-legend 'jabali-*.service' 'jabali-*.timer' 'jabali-*.slice' "+
			"| awk '{print $1}'").Output()
	if err != nil {
		return false, "", err
	}
	var stale []string
	for _, line := range strings.Split(string(out), "\n") {
		unit := strings.TrimSpace(line)
		if unit == "" {
			continue
		}
		propOut, err := exec.Command("systemctl", "show", unit, "-p", "NeedDaemonReload").Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(propOut), "NeedDaemonReload=yes") {
			stale = append(stale, unit)
		}
	}
	if len(stale) == 0 {
		return false, "", nil
	}
	return true, strings.Join(stale, ","), nil
}

func fixDaemonReload(_ repairCtx) error {
	return run("", "systemctl", "daemon-reload")
}

// ---------- orphan-slices ----------
//
// Symptom: jabali-user-<username>.slice units linger after the unix user was
// deleted (e.g. deleted before slice teardown was wired into userDeleteHandler).
// Orphan slices consume cgroup resources and clutter `jabali server-status`.

func orphanSliceUsernames() ([]string, error) {
	out, err := exec.Command("systemctl", "list-units", "--all", "--no-legend", "jabali-user-*.slice").Output()
	if err != nil {
		return nil, err
	}
	var orphans []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if !strings.HasPrefix(unit, "jabali-user-") || !strings.HasSuffix(unit, ".slice") {
			continue
		}
		username := strings.TrimSuffix(strings.TrimPrefix(unit, "jabali-user-"), ".slice")
		if _, err := user.Lookup(username); err != nil {
			orphans = append(orphans, username)
		}
	}
	return orphans, nil
}

func detectOrphanSlices(_ repairCtx) (bool, string, error) {
	orphans, err := orphanSliceUsernames()
	if err != nil {
		return false, "", err
	}
	if len(orphans) == 0 {
		return false, "", nil
	}
	return true, strings.Join(orphans, ","), nil
}

func fixOrphanSlices(_ repairCtx) error {
	orphans, err := orphanSliceUsernames()
	if err != nil {
		return err
	}
	for _, username := range orphans {
		fmt.Printf("  removing orphan slice: %s\n", username)
		_ = exec.Command("systemctl", "stop", "jabali-fpm@"+username+".service").Run()
		_ = exec.Command("systemctl", "disable", "jabali-fpm@"+username+".service").Run()
		_ = exec.Command("systemctl", "stop", "jabali-user-"+username+".slice").Run()
		sliceUnit := "/etc/systemd/system/jabali-user-" + username + ".slice"
		fpmDropinDir := "/etc/systemd/system/jabali-fpm@" + username + ".service.d"
		fpmDropin := fpmDropinDir + "/slice.conf"
		_ = os.Remove(sliceUnit)
		_ = os.Remove(fpmDropin)
		_ = os.Remove(fpmDropinDir) // rmdir; no-ops if non-empty or absent
	}
	if len(orphans) > 0 {
		return run("", "systemctl", "daemon-reload")
	}
	return nil
}

// ---------- crowdsec-bouncer-key ----------
//
// Symptom: crowdsec-firewall-bouncer service loops in failed/activating state.
// Journal shows "bouncer stream halted" or "Unauthorized". Root cause: the LAPI
// database was reset or the installer re-seeded a new key, but the bouncer YAML
// still carries the old key. The fix mirrors install.sh: delete the stale LAPI
// registration, add a fresh one, patch the YAML, and restart the service.

func crowdsecFirewallBouncerService() string {
	for _, pkg := range []string{
		"crowdsec-firewall-bouncer-nftables",
		"crowdsec-firewall-bouncer-iptables",
	} {
		out, err := exec.Command("systemctl", "cat", pkg+".service").Output()
		if err == nil && len(out) > 0 {
			return pkg + ".service"
		}
	}
	return ""
}

func detectCrowdSecBouncerKey(_ repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("cscli"); err != nil {
		return false, "", nil // crowdsec not installed
	}
	svc := crowdsecFirewallBouncerService()
	if svc == "" {
		return false, "", nil // bouncer package not installed
	}
	out, _ := exec.Command("systemctl", "is-active", svc).Output()
	if strings.TrimSpace(string(out)) == "active" {
		return false, "", nil // running fine
	}
	// Only flag if the service is failed/activating (crash-loop). A service
	// that was never started intentionally (inactive/disabled) is not ours
	// to repair here.
	subOut, _ := exec.Command("systemctl", "show", svc, "-p", "SubState").Output()
	sub := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(subOut)), "SubState="))
	if sub != "failed" && sub != "auto-restart" {
		return false, "", nil
	}
	// Look for stale-key evidence in recent journal lines.
	journal, _ := exec.Command("journalctl", "-u", svc, "-n", "80", "--no-pager").Output()
	j := string(journal)
	if strings.Contains(j, "stream halted") || strings.Contains(j, "Unauthorized") {
		return true, fmt.Sprintf("%s crash-loop: stale LAPI key detected", svc), nil
	}
	return true, fmt.Sprintf("%s SubState=%s", svc, sub), nil
}

func fixCrowdSecBouncerKey(_ repairCtx) error {
	svc := crowdsecFirewallBouncerService()
	if svc == "" {
		return fmt.Errorf("crowdsec-firewall-bouncer service not found")
	}
	pkg := strings.TrimSuffix(svc, ".service")
	conf := "/etc/crowdsec/bouncers/" + pkg + ".yaml"
	if _, err := os.Stat(conf); err != nil {
		return fmt.Errorf("bouncer conf %s: %w", conf, err)
	}
	const bouncerName = "jabali-firewall"
	// Prune stale LAPI registration (ignore error if it never existed).
	_ = exec.Command("cscli", "bouncers", "delete", bouncerName).Run()
	// Mint a fresh key.
	keyOut, err := exec.Command("cscli", "bouncers", "add", bouncerName, "-o", "raw").Output()
	if err != nil {
		return fmt.Errorf("cscli bouncers add: %w", err)
	}
	apiKey := strings.TrimSpace(string(keyOut))
	if apiKey == "" {
		return fmt.Errorf("cscli bouncers add returned empty key")
	}
	// Patch api_key in the bouncer YAML.
	if err := run("", "yq", "-y", "-i",
		fmt.Sprintf(`.api_key = "%s"`, apiKey), conf); err != nil {
		return fmt.Errorf("yq patch %s: %w", conf, err)
	}
	// Restart with fresh credentials.
	return run("", "systemctl", "restart", svc)
}

// ---------- apparmor-profiles-missing ----------
//
// Symptom: the five jabali AppArmor profiles are absent from
// /etc/apparmor.d/.  This happens when install_apparmor ran before
// clone_or_update_repo (ordering bug fixed 2026-05-10) or when the
// profiles were accidentally removed.  Fix: copy profiles from the
// repo and load them in complain mode.

// jabaliAAProfiles returns the AppArmor profile names jabali actually ships,
// derived from install/apparmor/ at runtime. The old static 5-name list
// claimed usr.local.bin.jabali-agent + usr.local.bin.jabali-kratos, which
// have NO source file under install/apparmor/ — so the missing-profiles
// detector flagged them broken forever and fixAppArmorProfilesMissing could
// never install them (it skips profiles with no source). Deriving the set
// from the shipped files means the detector only checks what can actually be
// installed, and auto-syncs if profiles are added/removed later.
func jabaliAAProfiles(repoDir string) []string {
	entries, err := os.ReadDir(filepath.Join(repoDir, "install", "apparmor"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "usr.local.bin.") {
			names = append(names, e.Name())
		}
	}
	return names
}

// apparmorEnforceable reports whether AppArmor can actually load/enforce
// profiles on this host. In a nested/unprivileged LXC the module is often
// "loaded" but the apparmor securityfs interface is absent (the host owns
// AppArmor), so jabali's profiles can be neither parsed nor enforced — reporting
// them "missing" or "disabled" there is noise, not a fixable condition.
func apparmorEnforceable() bool {
	_, err := os.Stat("/sys/kernel/security/apparmor/profiles")
	return err == nil
}

func detectAppArmorProfilesMissing(ctx repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("aa-status"); err != nil {
		return false, "", nil // AppArmor not installed
	}
	if !apparmorEnforceable() {
		return false, "", nil // e.g. nested LXC — AppArmor can't run here
	}
	missing := []string{}
	for _, p := range jabaliAAProfiles(ctx.repoDir) {
		if _, err := os.Stat("/etc/apparmor.d/" + p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%d jabali AppArmor profile(s) missing: %s",
		len(missing), strings.Join(missing, ", ")), nil
}

func fixAppArmorProfilesMissing(ctx repairCtx) error {
	srcDir := filepath.Join(ctx.repoDir, "install", "apparmor")
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("AppArmor profile source dir %s not found: %w", srcDir, err)
	}
	for _, p := range jabaliAAProfiles(ctx.repoDir) {
		src := filepath.Join(srcDir, p)
		dst := "/etc/apparmor.d/" + p
		if _, err := os.Stat(src); err != nil {
			continue // profile not in this repo version — skip
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		// Load in complain mode (non-blocking for the running process).
		_ = exec.Command("apparmor_parser", "-r", dst).Run()
		_ = exec.Command("aa-complain", dst).Run()
	}
	return nil
}

// ---------- apparmor-profiles-disabled ----------
//
// Symptom: profiles exist under /etc/apparmor.d/ but aa-disable has
// created symlinks in /etc/apparmor.d/disable/ — the profiles are on
// disk but not enforced.  Fix: remove the disable symlinks and reload
// in complain mode.

func detectAppArmorProfilesDisabled(ctx repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("aa-status"); err != nil {
		return false, "", nil
	}
	if !apparmorEnforceable() {
		return false, "", nil // e.g. nested LXC — AppArmor can't run here
	}
	disabled := []string{}
	for _, p := range jabaliAAProfiles(ctx.repoDir) {
		disablePath := "/etc/apparmor.d/disable/" + p
		if _, err := os.Lstat(disablePath); err == nil {
			disabled = append(disabled, p)
		}
	}
	if len(disabled) == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%d jabali AppArmor profile(s) disabled: %s",
		len(disabled), strings.Join(disabled, ", ")), nil
}

func fixAppArmorProfilesDisabled(ctx repairCtx) error {
	for _, p := range jabaliAAProfiles(ctx.repoDir) {
		disablePath := "/etc/apparmor.d/disable/" + p
		profilePath := "/etc/apparmor.d/" + p
		if _, err := os.Lstat(disablePath); err != nil {
			continue
		}
		if err := os.Remove(disablePath); err != nil {
			return fmt.Errorf("remove disable symlink %s: %w", disablePath, err)
		}
		if _, err := os.Stat(profilePath); err == nil {
			_ = exec.Command("apparmor_parser", "-r", profilePath).Run()
			_ = exec.Command("aa-complain", profilePath).Run()
		}
	}
	return nil
}

// repairHint is appended to error messages from runUpdate so an operator
// who hits a wall has a clear next move: a single command that may
// self-heal whatever broke the update.
//
// Wired into update.go's error-path returns. Cheap to produce — no IO.
func repairHint() string {
	return "\n  → If this looks like a deployment-host issue, try:\n" +
		"      jabali repair --diagnose\n" +
		"      jabali repair --auto\n"
}

// ---------- orphan-migration-staging ----------
//
// Symptom: /var/lib/jabali-migrations/<job-id>/ holds 3-5 GB of
// extracted cpmove / DA / Hestia trees for jobs that are done /
// failed / cancelled. Real cause: pre-e0572bcc imports kept the
// staging dir forever; auto-cleanup landed but didn't sweep the
// historical backlog. Detector reads the DB for terminal job-ids
// + flags any staging dir whose job is terminal.

func migrationStagingRoot() string {
	return "/var/lib/jabali-migrations"
}

func detectOrphanMigrationStaging(ctx repairCtx) (bool, string, error) {
	orphans, err := orphanMigrationStagingDirs(ctx)
	if err != nil {
		return false, "", err
	}
	if len(orphans) == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%d orphan dirs (%s, ...)", len(orphans), orphans[0]), nil
}

func fixOrphanMigrationStaging(ctx repairCtx) error {
	orphans, err := orphanMigrationStagingDirs(ctx)
	if err != nil {
		return err
	}
	for _, p := range orphans {
		fmt.Printf("  removing orphan staging dir: %s\n", p)
		if rmErr := os.RemoveAll(p); rmErr != nil {
			fmt.Printf("  WARN: rm %s: %v\n", p, rmErr)
		}
	}
	return nil
}

// orphanMigrationStagingDirs lists /var/lib/jabali-migrations/<job-id>
// dirs whose mtime is older than the cutoff. DB lookups are out of
// scope for `jabali repair` (no DB handle on repairCtx); time-based
// sweep covers the common case — auto-cleanup landed in e0572bcc so
// any dir whose import completed is gone within seconds. Anything
// older than 7 days is almost certainly an orphan from a pre-
// cleanup-era run or a pull-source that died mid-extract.
func orphanMigrationStagingDirs(_ repairCtx) ([]string, error) {
	root := migrationStagingRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	var orphans []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			orphans = append(orphans, filepath.Join(root, e.Name()))
		}
	}
	return orphans, nil
}

// ---------- docroot-www-data-group ----------
//
// Symptom: a freshly uploaded WordPress image (or any file the app's
// PHP-FPM pool writes) returns 403, with nginx logging
//   open() "/home/<u>/domains/<d>/public_html/wp-content/uploads/.../x.jpg"
//   failed (13: Permission denied)
//
// Cause: jabali serves tenant files as <user>:www-data 0640 and nginx
// (www-data) reads them via the group. But the per-user PHP-FPM pool
// runs with group=<user>, and if the docroot dirs are not setgid the
// files PHP creates land as <user>:<user> 0640 → www-data is "other" →
// no read → 403. domain_create now lays docroots down 2750 (setgid) so
// new files inherit the www-data group; this repair retrofits sites
// provisioned before that, or whose group ownership was clobbered by an
// out-of-band restore / chown -R (GH reviews-il.co.il, 2026-06-16).
//
// Destructive: it rewrites group ownership + the setgid bit across every
// docroot, so it runs only with --all --yes or the explicit flag.

// jabaliDocrootRe matches `root /home/...;` directives in the per-domain
// nginx configs. We read the authoritative docroot list from nginx
// rather than globbing /home so we only ever touch real served roots.
var jabaliDocrootRe = regexp.MustCompile(`(?m)^\s*root\s+(/home/[^;\n]+);`)

func jabaliDocroots() []string {
	seen := map[string]bool{}
	var out []string
	add := func(dr string) {
		dr = strings.TrimSpace(dr)
		if dr == "" || seen[dr] || !strings.HasPrefix(dr, "/home/") {
			return
		}
		// Only ever a real (non-symlink) directory — the detector/fix
		// must never be pointed at a symlinked "docroot".
		if fi, lerr := os.Lstat(dr); lerr == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			seen[dr] = true
			out = append(out, dr)
		}
	}

	// Source 1: `root /home/...;` directives in the per-domain nginx
	// configs. jabali has shipped these under a few layouts across
	// versions (and migrated-from-DA hosts keep sites-available), so we
	// scan all the standard locations rather than one.
	confGlobs := []string{
		"/etc/nginx/jabali/*/*.conf",
		"/etc/nginx/jabali/*.conf",
		"/etc/nginx/sites-enabled/*.conf",
		"/etc/nginx/sites-available/*.conf",
		"/etc/nginx/conf.d/*.conf",
	}
	for _, g := range confGlobs {
		matches, _ := filepath.Glob(g)
		for _, f := range matches {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, m := range jabaliDocrootRe.FindAllStringSubmatch(string(b), -1) {
				add(m[1])
			}
		}
	}

	// Source 2: on-disk docroots. The authoritative jabali layout is
	// /home/<user>/domains/<domain>/public_html; some hosts also use
	// /home/<user>/public_html. This is the safety net when the nginx
	// config layout doesn't match any glob above.
	for _, g := range []string{
		"/home/*/domains/*/public_html",
		"/home/*/public_html",
	} {
		matches, _ := filepath.Glob(g)
		for _, dr := range matches {
			add(dr)
		}
	}
	return out
}

// homeChainDirs returns the docroot's ancestor directories up to (but
// not including) /home/<user> — the dirs nginx (www-data) must traverse
// to reach the served root. domain_create chowns these <user>:www-data
// 2750; if a chown -R clobbers them, www-data loses +x and every file
// 403s regardless of the file's own group. For
// /home/u/domains/d/public_html → [/home/u/domains/d, /home/u/domains].
func homeChainDirs(dr string) []string {
	clean := filepath.Clean(dr)
	parts := strings.Split(clean, "/")
	if len(parts) < 4 || parts[1] != "home" {
		return nil
	}
	userRoot := "/home/" + parts[2]
	var out []string
	for cur := filepath.Dir(clean); cur != userRoot && strings.HasPrefix(cur, userRoot+"/"); cur = filepath.Dir(cur) {
		out = append(out, cur)
	}
	return out
}

// dirTraversableByGroup reports whether path is a real dir owned by gid
// with group-execute set (so www-data can traverse it). A symlink or
// missing dir returns false-but-not-drift (handled by the caller).
func dirTraversableByGroup(path string, gid int) (ok bool, isReal bool) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false, false
	}
	st, sok := fi.Sys().(*syscall.Stat_t)
	if !sok {
		return false, false
	}
	return int(st.Gid) == gid && fi.Mode().Perm()&0o010 != 0, true
}

func wwwDataGid() (int, error) {
	g, err := user.LookupGroup("www-data")
	if err != nil {
		return -1, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1, fmt.Errorf("www-data gid %q: %w", g.Gid, err)
	}
	return gid, nil
}

// docrootDrifted reports whether any real (non-symlink) entry under dr is
// not group www-data, or any real directory lacks the setgid bit. Walks
// with Lstat (filepath.Walk) so symlinked dirs are never descended and
// symlink entries are ignored — never follows a tenant symlink out of
// the tree. Stops at the first offending entry.
var errDocrootDrift = errors.New("drift")

func docrootDrifted(dr string, gid int) bool {
	drift := false
	_ = filepath.Walk(dr, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, keep scanning
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // ignore symlinks (Walk won't descend symlinked dirs)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if int(st.Gid) != gid {
			drift = true
			return errDocrootDrift
		}
		if info.IsDir() && info.Mode()&os.ModeSetgid == 0 {
			drift = true
			return errDocrootDrift
		}
		return nil
	})
	return drift
}

func detectDocrootGroup(_ repairCtx) (bool, string, error) {
	gid, err := wwwDataGid()
	if err != nil {
		return false, "", nil // no www-data group (dev box) → not applicable
	}
	docroots := jabaliDocroots()
	var bad []string
	for _, dr := range docroots {
		drift := docrootDrifted(dr, gid)
		if !drift {
			for _, anc := range homeChainDirs(dr) {
				if ok, real := dirTraversableByGroup(anc, gid); real && !ok {
					drift = true
					break
				}
			}
		}
		if drift {
			bad = append(bad, dr)
		}
	}
	if len(bad) == 0 {
		return false, "", nil
	}
	detail := fmt.Sprintf("%d/%d docroot(s) with files unreadable by nginx: %s",
		len(bad), len(docroots), strings.Join(shortPaths(bad, 3), ", "))
	return true, detail, nil
}

// shortPaths renders up to n basenames for diagnose output.
func shortPaths(paths []string, n int) []string {
	out := make([]string, 0, n+1)
	for i, p := range paths {
		if i >= n {
			out = append(out, fmt.Sprintf("(+%d more)", len(paths)-n))
			break
		}
		// /home/<user>/domains/<domain>/public_html → <domain>
		parts := strings.Split(p, "/")
		if len(parts) >= 5 {
			out = append(out, parts[4])
		} else {
			out = append(out, p)
		}
	}
	return out
}

func fixDocrootGroup(_ repairCtx) error {
	gid, err := wwwDataGid()
	if err != nil {
		return err
	}
	for _, dr := range jabaliDocroots() {
		home := userHomeOf(dr)
		if home == "" {
			continue
		}
		// fsperm descends symlink-safely from /home/<user> (openat
		// O_NOFOLLOW per component), repairing the traversal chain
		// (domains/<domain>, domains) so www-data keeps +x to reach the
		// root, then the docroot subtree. Never passes a tenant path to
		// a privileged chmod/chown (TOCTOU-safe).
		if err := fsperm.RepairDocrootGroup(home, dr, gid); err != nil {
			return fmt.Errorf("%s: %w", dr, err)
		}
	}
	return nil
}

// userHomeOf returns /home/<user> for a /home/<user>/... docroot, or "".
func userHomeOf(dr string) string {
	parts := strings.Split(filepath.Clean(dr), "/")
	if len(parts) < 3 || parts[1] != "home" || parts[2] == "" {
		return ""
	}
	return "/home/" + parts[2]
}

// ---------- bulwark-jwt-secret ----------
//
// Symptom: clicking "Open webmail" / mail-user impersonation fails with
// Bulwark's "Invalid signature" (GH #193). Root cause was openssl's
// base64 line-wrap newline baked into the secret, which split
// BULWARK_JWT_AUTH_SECRET across two lines in bulwark.env so Bulwark and
// panel-api signed/verified with different keys. This also covers any
// later divergence (file regenerated, env not resynced) — `jabali update`
// rebuilds the panel binary but never re-runs the secret provisioning, so
// a mismatched env would otherwise persist.
//
// Fix: sanitize the secret file to a clean single token, rewrite
// BULWARK_JWT_AUTH_SECRET in bulwark.env to match (dropping any orphan
// fragment line), and restart jabali-webmail + jabali-panel so both pick
// up the same key.

const (
	bulwarkSecretFile = "/etc/jabali-panel/bulwark-jwt-auth.secret"
	bulwarkEnvFile    = "/etc/jabali-panel/bulwark.env"
)

var alnumRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// bulwarkCleanSecret returns the sanitized secret (alnum only), the raw
// file contents, and ok=false if the file is absent (no Bulwark here).
func bulwarkCleanSecret() (clean, raw string, ok bool) {
	b, err := os.ReadFile(bulwarkSecretFile)
	if err != nil {
		return "", "", false
	}
	raw = string(b)
	return alnumRe.ReplaceAllString(raw, ""), raw, true
}

// bulwarkEnvSecret extracts the BULWARK_JWT_AUTH_SECRET value from
// bulwark.env (the value Bulwark actually uses), or "" if absent.
func bulwarkEnvSecret() string {
	b, err := os.ReadFile(bulwarkEnvFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, found := strings.CutPrefix(line, "BULWARK_JWT_AUTH_SECRET="); found {
			return strings.TrimRight(v, "\r")
		}
	}
	return ""
}

func detectBulwarkJWTSecret(_ repairCtx) (bool, string, error) {
	clean, raw, ok := bulwarkCleanSecret()
	if !ok {
		return false, "", nil // no Bulwark secret on this host
	}
	env := bulwarkEnvSecret()
	if env == "" {
		return false, "", nil // bulwark.env not provisioned
	}
	if strings.TrimSpace(raw) != clean {
		return true, "secret file has a stray newline/whitespace (splits the env value)", nil
	}
	if env != clean {
		return true, "bulwark.env BULWARK_JWT_AUTH_SECRET != secret file", nil
	}
	return false, "", nil
}

func fixBulwarkJWTSecret(_ repairCtx) error {
	clean, raw, ok := bulwarkCleanSecret()
	if !ok {
		return nil
	}
	// 1. Rewrite the secret file clean if it carried any junk.
	if strings.TrimSpace(raw) != clean {
		if err := writeBulwarkSecretFile(clean); err != nil {
			return err
		}
	}
	// 2. Rebuild bulwark.env: drop the old secret line + any orphan
	//    fragment (a line with no '=' that isn't a comment/blank), then
	//    set BULWARK_JWT_AUTH_SECRET=clean.
	if err := resyncBulwarkEnv(clean); err != nil {
		return err
	}
	// 3. Restart both so they reload the matching key.
	for _, unit := range []string{"jabali-webmail", "jabali-panel"} {
		_ = exec.Command("systemctl", "restart", unit).Run()
	}
	return nil
}

func writeBulwarkSecretFile(clean string) error {
	if err := os.WriteFile(bulwarkSecretFile, []byte(clean), 0o640); err != nil {
		return fmt.Errorf("write secret file: %w", err)
	}
	if g, err := user.LookupGroup("jabali-webmail"); err == nil {
		if gid, aerr := strconv.Atoi(g.Gid); aerr == nil {
			_ = os.Chown(bulwarkSecretFile, 0, gid)
		}
	}
	return nil
}

func resyncBulwarkEnv(clean string) error {
	b, err := os.ReadFile(bulwarkEnvFile)
	if err != nil {
		return fmt.Errorf("read bulwark.env: %w", err)
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(line, "BULWARK_JWT_AUTH_SECRET=") {
			continue // drop old secret line
		}
		// Drop orphan fragments: non-empty, not a comment, and no '='.
		if t != "" && !strings.HasPrefix(t, "#") && !strings.Contains(line, "=") {
			continue
		}
		keep = append(keep, line)
	}
	out := strings.Join(keep, "\n")
	out = strings.TrimRight(out, "\n") + "\nBULWARK_JWT_AUTH_SECRET=" + clean + "\n"
	tmp := bulwarkEnvFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o640); err != nil {
		return fmt.Errorf("write bulwark.env tmp: %w", err)
	}
	if g, err := user.LookupGroup("jabali-webmail"); err == nil {
		if gid, aerr := strconv.Atoi(g.Gid); aerr == nil {
			_ = os.Chown(tmp, gid, gid)
		}
	}
	if err := os.Rename(tmp, bulwarkEnvFile); err != nil {
		return fmt.Errorf("rename bulwark.env: %w", err)
	}
	return nil
}

// ---------- etc-jabali-perms ----------
//
// Symptom: /etc/jabali is not world-traversable (e.g. an operator chmod'd
// it 0700 to "protect" db-password), or ssh-sandbox-mode isn't world-
// readable. The login wrapper reads /etc/jabali/ssh-sandbox-mode AS the
// hosting user; if it can't, it falls back to nologin — locking EVERY
// tenant out of SSH/SFTP (GH #211: a user was one chmod from reinstalling
// Debian). The secrets in /etc/jabali (db-password, config.toml) are
// protected at the FILE level (0640), so the directory is meant to stay
// 0755. Fix: restore 0755 on the dir + 0644 on the mode file. Non-
// destructive — only loosens to the install default.

func detectEtcJabaliPerms(_ repairCtx) (bool, string, error) {
	fi, err := os.Stat("/etc/jabali")
	if err != nil {
		return false, "", nil // dir absent — unrelated
	}
	issues := []string{}
	if fi.Mode().Perm()&0o001 == 0 {
		issues = append(issues, fmt.Sprintf("/etc/jabali is %#o (needs o+x so tenants can traverse)", fi.Mode().Perm()))
	}
	if mfi, merr := os.Stat("/etc/jabali/ssh-sandbox-mode"); merr == nil {
		if mfi.Mode().Perm()&0o004 == 0 {
			issues = append(issues, fmt.Sprintf("/etc/jabali/ssh-sandbox-mode is %#o (needs o+r)", mfi.Mode().Perm()))
		}
	}
	if len(issues) == 0 {
		return false, "", nil
	}
	return true, "hosting users can't read the SSH sandbox mode (login wrapper falls back to nologin — every tenant locked out of SSH/SFTP): " + strings.Join(issues, "; "), nil
}

func fixEtcJabaliPerms(_ repairCtx) error {
	if err := os.Chmod("/etc/jabali", 0o755); err != nil {
		return fmt.Errorf("chmod 0755 /etc/jabali: %w", err)
	}
	if _, err := os.Stat("/etc/jabali/ssh-sandbox-mode"); err == nil {
		if err := os.Chmod("/etc/jabali/ssh-sandbox-mode", 0o644); err != nil {
			return fmt.Errorf("chmod 0644 /etc/jabali/ssh-sandbox-mode: %w", err)
		}
	}
	return nil
}

// detectDirtyMigration reports a schema golang-migrate considers mid-flight.
//
// This is the highest-impact thing repair can find: panel-api runs migrations
// at startup and refuses to boot while the schema is dirty, so the panel is
// DOWN and systemd is restart-looping it. Before this detector existed,
// `jabali repair --diagnose` — the tool whose whole purpose is "a host in a
// state that would block the next update" — reported all-green on exactly that
// host, because nothing looked at the migration table.
//
// Deliberately has no fix. Clearing the flag means asserting whether the
// migration completed, and forcing the wrong way silently leaves the schema
// missing changes. The detector points at `jabali migrate status`, which
// explains how to tell the two cases apart.
func detectDirtyMigration(_ repairCtx) (bool, string, error) {
	if err := initConfig(); err != nil {
		return false, "", nil // config unreadable — other detectors report that
	}
	cfg := sharedCfg
	if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
		return false, "", nil
	}
	st, err := db.State(cfg.Database.URL)
	if err != nil {
		// A DB we cannot reach is its own problem, and one the operator will
		// already be seeing; do not report it as a dirty migration.
		return false, "", nil
	}
	if !st.Dirty {
		return false, "", nil
	}
	return true, fmt.Sprintf(
		"schema dirty at version %d — panel-api cannot start; run `jabali migrate status` for how to clear it",
		st.Version), nil
}

// detectBrokenFtp264 reports the one dirty state repair can recover on its own:
// the half-applied original migration 000264 (GH #1094). The `ftp_pasv_address
// VARCHAR` ALTER hit InnoDB's row-size ceiling and rolled back, but the earlier
// `CREATE TABLE ftp_accounts` had committed, leaving the schema dirty at 264
// with a fix (#1103) that a dirty host can't apply. See db.IsBrokenFtp264 for
// the precise fingerprint — anything else falls through to detectDirtyMigration.
func detectBrokenFtp264(_ repairCtx) (bool, string, error) {
	if err := initConfig(); err != nil {
		return false, "", nil
	}
	cfg := sharedCfg
	if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
		return false, "", nil
	}
	broken, err := db.IsBrokenFtp264(cfg.Database.URL)
	if err != nil {
		// Unreachable DB is its own finding; don't misreport it here.
		return false, "", nil
	}
	if !broken {
		return false, "", nil
	}
	return true, "migration 264 half-applied (ftp_accounts created, server_settings ALTER rolled back at the row-size ceiling); recoverable", nil
}

// fixBrokenFtp264 clears the half-applied 000264 and re-applies the corrected
// migration (drop the orphan empty ftp_accounts table, force back to 263,
// migrate up). Gated on the fingerprint via db.RecoverBrokenFtp264, so it is a
// no-op unless detectBrokenFtp264 matched — never a blind force.
func fixBrokenFtp264(_ repairCtx) error {
	if err := initConfig(); err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := sharedCfg
	if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
		return fmt.Errorf("database URL not configured")
	}
	recovered, err := db.RecoverBrokenFtp264(cfg.Database.URL)
	if err != nil {
		return err
	}
	if !recovered {
		// Fingerprint no longer matches (e.g. already recovered) — nothing to do.
		return nil
	}
	return nil
}
