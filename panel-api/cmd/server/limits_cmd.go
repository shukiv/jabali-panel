package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// quotaMountOrEmpty resolves the mount path /home lives on. Returns
// "" only when QuotaMountFor itself errors — /home==/ is fine (install.sh
// enables ext4 hidden quota inodes on /, matching cPanel/DA), and the
// runtime decision to honor quotas is gated separately by
// server_settings.disk_quota_enabled.
func quotaMountOrEmpty() string {
	m, err := limits.QuotaMountFor("/home")
	if err != nil {
		return ""
	}
	return m
}

// `jabali limits` groups M18 operator commands: host-prerequisite
// probes, per-user re-apply, status readback, and bulk package apply.
//
// Scope split:
//   - `check` runs entirely locally (no DB, no agent) so it's useful
//     on a fresh or broken host.
//   - `apply` / `status` / `package apply` need the agent UDS + DB,
//     so they use requireDBAndAgent and go through agent RPCs.

func newLimitsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limits",
		Short: "Per-user resource limits (cgroups v2 + POSIX quota + nginx)",
		Long: `Inspect and manage the M18 resource-limit pipeline:
disk quota (POSIX), cgroups v2 via systemd slice drop-ins, and
per-domain nginx rate/connection limits.`,
	}
	cmd.AddCommand(
		newLimitsCheckCmd(),
		newLimitsApplyCmd(),
		newLimitsStatusCmd(),
		newLimitsPackageCmd(),
		newLimitsUsageCmd(),
		newLimitsOverrideCmd(),
	)
	return cmd
}

// `jabali limits check` — pure host probe. No DB, no agent. Safe to
// run as any user (though a few kernel checks are informative-only
// without root). Returns non-zero if anything required is missing so
// it slots into ops scripts.
func newLimitsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Probe host for M18 prerequisites (cgroups v2, /home fs, nginx modules)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			ok := true

			// cgroups v2 unified hierarchy.
			if fs, err := exec.Command("stat", "-fc", "%T", "/sys/fs/cgroup").Output(); err == nil {
				got := trimNL(fs)
				if got == "cgroup2fs" {
					fmt.Fprintln(w, "cgroups v2\tOK\tunified hierarchy active")
				} else {
					fmt.Fprintf(w, "cgroups v2\tFAIL\t/sys/fs/cgroup is %s (need cgroup2fs)\n", got)
					ok = false
				}
			} else {
				fmt.Fprintf(w, "cgroups v2\tFAIL\tstat failed: %v\n", err)
				ok = false
			}

			// /home filesystem.
			fsType, err := limits.DetectFilesystem("/home")
			if err != nil {
				fmt.Fprintf(w, "/home fs\tFAIL\tdetect failed: %v\n", err)
				ok = false
			} else if !fsType.SupportsPOSIXQuota() {
				fmt.Fprintf(w, "/home fs\tFAIL\t%s — not supported (ext2/3/4 or xfs required)\n", fsType)
				ok = false
			} else {
				fmt.Fprintf(w, "/home fs\tOK\t%s\n", fsType)
			}

			// /home mount path (for setquota explicit-mount invariant).
			if mount, err := limits.QuotaMountFor("/home"); err != nil {
				fmt.Fprintf(w, "quota mount\tFAIL\t%v\n", err)
				ok = false
			} else {
				fmt.Fprintf(w, "quota mount\tOK\t%s\n", mount)
			}

			// nginx modules — limit_req_module and limit_conn_module are
			// stock default-on modules: `nginx -V` only lists optional
			// `--with-*` and explicitly disabled `--without-*` flags, so
			// their absence from -V doesn't prove absence from the binary.
			// Correct probe: flag FAIL only when -V mentions the explicit
			// `--without-http_<mod>_module` disable flag; otherwise assume
			// compiled in (true for stock builds from Debian, Ubuntu, RHEL,
			// Alpine, upstream nginx.org). This matches what `nginx -t`
			// against a config using the directive would report.
			if out, err := exec.Command("nginx", "-V").CombinedOutput(); err == nil {
				s := string(out)
				for _, mod := range []string{"limit_req", "limit_conn"} {
					disableFlag := "--without-http_" + mod + "_module"
					if indexOfAny(s, []string{disableFlag}) {
						fmt.Fprintf(w, "nginx %s\tFAIL\tdisabled via %s (rebuild nginx without that flag)\n", mod, disableFlag)
						ok = false
					} else {
						fmt.Fprintf(w, "nginx %s\tOK\tcompiled in (default)\n", mod)
					}
				}
			} else {
				fmt.Fprintf(w, "nginx\tWARN\tnginx binary not found in PATH; skipping module probe\n")
			}

			_ = w.Flush()
			if !ok {
				return fmt.Errorf("one or more prerequisite checks failed")
			}
			return nil
		},
	}
}

func indexOfAny(s string, needles []string) bool {
	for _, n := range needles {
		for i := 0; i+len(n) <= len(s); i++ {
			if s[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}

func trimNL(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// `jabali limits apply <username>` — calls user.limits.apply with the
// user's currently-effective limits. Useful after a manual drop-in
// edit or when the reconciler hasn't ticked yet.
func newLimitsApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "apply <username>",
		Short:   "Re-apply effective limits for one user",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			return applyForUsername(ctx, args[0], diskQuotaEnabled(ctx))
		},
	}
}

// `jabali limits status <username>` — calls user.limits.report and
// formats for a human reader. Uses tab-aligned columns without color
// (avoids terminal-detection dance in CLI contexts like SSH log reads).
func newLimitsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status <username>",
		Short:   "Show live resource usage for one user",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			if looksLikeULID(username) {
				return fmt.Errorf("argument %q looks like a user ULID; this command takes the OS username (try `jabali users get %s` to look it up)", username, username)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, "user.limits.report", map[string]any{
				"username":    username,
				"quota_mount": quotaMountOrEmpty(),
			})
			if err != nil {
				if strings.Contains(err.Error(), "user not found") || strings.Contains(err.Error(), "no such user") {
					return fmt.Errorf("user %q not found on this host (this command expects the OS username, not user ID)", username)
				}
				return fmt.Errorf("agent: %w", err)
			}
			if jsonOutput {
				fmt.Println(string(raw))
				return nil
			}
			var report struct {
				Username string `json:"username"`
				Disk     *struct {
					UsedKB  uint64 `json:"used_kb"`
					LimitKB uint64 `json:"limit_kb"`
				} `json:"disk,omitempty"`
				Memory *struct {
					CurrentBytes uint64 `json:"current_bytes"`
					MaxBytes     uint64 `json:"max_bytes"`
				} `json:"memory,omitempty"`
				CPU *struct {
					UsageNsec    uint64 `json:"usage_nsec"`
					QuotaPercent uint32 `json:"quota_percent"`
				} `json:"cpu,omitempty"`
				Tasks *struct {
					Current uint64 `json:"current"`
					Max     uint64 `json:"max"`
				} `json:"tasks,omitempty"`
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				return fmt.Errorf("parse agent response: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "user\t%s\n", report.Username)
			if report.Disk != nil {
				fmt.Fprintf(w, "disk\t%s / %s\n", fmtKB(report.Disk.UsedKB), fmtKBLimit(report.Disk.LimitKB))
			}
			if report.Memory != nil {
				fmt.Fprintf(w, "memory\t%s / %s\n", fmtBytes(report.Memory.CurrentBytes), fmtBytesLimit(report.Memory.MaxBytes))
			}
			if report.CPU != nil {
				q := "unlimited"
				if report.CPU.QuotaPercent > 0 {
					q = fmt.Sprintf("%d%%", report.CPU.QuotaPercent)
				}
				fmt.Fprintf(w, "cpu\t%s total, quota %s\n", humanCPUNs(report.CPU.UsageNsec), q)
			}
			if report.Tasks != nil {
				fmt.Fprintf(w, "tasks\t%d / %s\n", report.Tasks.Current, fmtUint(report.Tasks.Max))
			}
			return w.Flush()
		},
	}
}

// `jabali limits package apply <package_id>` — walks every user with
// that package and calls user.limits.apply. `--dry-run` shows the
// effective changes without touching anything (review's counter to
// the decrease-squeeze risk — an admin about to shrink a package can
// see the blast radius first).
func newLimitsPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Bulk limits operations across every user of a package",
	}
	dryRun := false
	apply := &cobra.Command{
		Use:     "apply <package_id>",
		Short:   "Re-apply limits to every user of the given package",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			pkgID := args[0]
			users := userRepo()

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			all, _, err := users.List(ctx, repository.ListOptions{Limit: 10000})
			if err != nil {
				return fmt.Errorf("list users: %w", err)
			}
			// Read the disk-quota toggle once for the whole fan-out (mirrors the
			// reconciler's once-per-pass read).
			quotaEnabled := diskQuotaEnabled(ctx)
			matched := 0
			for i := range all {
				u := &all[i]
				if u.PackageID == nil || *u.PackageID != pkgID {
					continue
				}
				if u.Username == nil || *u.Username == "" {
					fmt.Printf("skip %s (no linux account)\n", u.ID)
					continue
				}
				matched++
				if dryRun {
					fmt.Printf("would apply: %s\n", *u.Username)
					continue
				}
				if err := applyForUsername(ctx, *u.Username, quotaEnabled); err != nil {
					fmt.Printf("FAIL %s: %v\n", *u.Username, err)
				} else {
					fmt.Printf("ok   %s\n", *u.Username)
				}
			}
			if !dryRun {
				cliAuditOK(ctx, "limits.apply", "package", pkgID, nil)
			}
			fmt.Printf("\n%d users matched package %s (dry-run=%v)\n", matched, pkgID, dryRun)
			return nil
		},
	}
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be applied without making agent calls")
	cmd.AddCommand(apply)
	return cmd
}

// applyForUsername is the shared worker called by `apply` and
// `package apply`. Hydrates package + override, resolves effective
// limits via the pure resolver, calls user.limits.apply.
//
// `username` is misnamed for back-compat: any of email / username /
// user-id is accepted (delegated to resolveUser).
func applyForUsername(ctx context.Context, username string, quotaEnabled bool) error {
	pkgs := packageRepoFromDB()
	// Direct new constructor — no helper yet because this is the only
	// CLI consumer; if we grow more we'll factor it into root.go.
	ovRepo := repository.NewUserLimitOverrideRepository(sharedDB)

	user, err := resolveUser(ctx, username)
	if err != nil {
		return fmt.Errorf("find user %s: %w", username, err)
	}
	// Agent's user.limits.apply validates the username field against
	// POSIX rules (^[a-z_][a-z0-9_-]{0,31}$) — pass the resolved
	// username, NOT the raw input which may have been an email or ULID.
	if user.Username == nil || *user.Username == "" {
		return fmt.Errorf("user %s has no POSIX username (admin-only?) — limits apply requires a Linux account", username)
	}
	posixUsername := *user.Username
	var pkgL *limits.PackageLimits
	if user.PackageID != nil && *user.PackageID != "" {
		pkg, err := pkgs.FindByID(ctx, *user.PackageID)
		if err == nil {
			pkgL = &limits.PackageLimits{
				DiskQuotaMB:     pkg.DiskQuotaMB,
				CPUQuotaPercent: pkg.CPUQuotaPercent,
				MemoryLimitMB:   pkg.MemoryLimitMB,
				IOReadMbps:      pkg.IOReadMbps,
				IOWriteMbps:     pkg.IOWriteMbps,
				MaxTasks:        pkg.MaxTasks,
			}
		}
	}
	var ovL *limits.OverrideLimits
	if ov, err := ovRepo.FindByUserID(ctx, user.ID); err == nil && ov != nil {
		ovL = &limits.OverrideLimits{
			DiskQuotaMB:     ov.DiskQuotaMB,
			CPUQuotaPercent: ov.CPUQuotaPercent,
			MemoryLimitMB:   ov.MemoryLimitMB,
			IOReadMbps:      ov.IOReadMbps,
			IOWriteMbps:     ov.IOWriteMbps,
			MaxTasks:        ov.MaxTasks,
		}
	}

	// JAB-309: dispatch the shared Apply/Clear plan the reconciler uses. A user
	// with no package and no override is CLEARED (the CLI previously always
	// applied — leaving a zeros drop-in instead of removing it), and the
	// disk-quota feature gate is honoured (the CLI previously ignored it).
	plan := limits.PlanFor(pkgL, ovL, quotaEnabled, quotaMountOrEmpty())
	if err := plan.Execute(ctx, sharedAgent, posixUsername); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

// diskQuotaEnabled reads the global disk-quota feature toggle
// (server_settings.disk_quota_enabled). Defaults to enabled on a read failure,
// matching the reconciler.
func diskQuotaEnabled(ctx context.Context) bool {
	if s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx); err == nil && s != nil {
		return s.DiskQuotaEnabled
	}
	return true
}

// humanBytes renders a byte count using SI scaling so disk + memory
// readouts share the same units (KB, MB, GB, TB). Goes hand-in-hand
// with humanCPUNs to fix the historical mixed-units output where
// `limits status` showed 16 KB / 4673536 B / 258299000 ns side-by-side.
func humanBytes(b uint64) string {
	const k = 1024
	if b < k {
		return fmt.Sprintf("%d B", b)
	}
	v := float64(b)
	suffixes := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for v >= k && i < len(suffixes)-1 {
		v /= k
		i++
	}
	return fmt.Sprintf("%.2f %s", v, suffixes[i])
}

// humanCPUNs renders nanoseconds in human units (ms / s / min / h).
func humanCPUNs(ns uint64) string {
	switch {
	case ns < 1_000_000:
		return fmt.Sprintf("%d µs", ns/1_000)
	case ns < 1_000_000_000:
		return fmt.Sprintf("%.2f ms", float64(ns)/1_000_000)
	case ns < 60_000_000_000:
		return fmt.Sprintf("%.2f s", float64(ns)/1_000_000_000)
	case ns < 3_600_000_000_000:
		return fmt.Sprintf("%.2f min", float64(ns)/60_000_000_000)
	default:
		return fmt.Sprintf("%.2f h", float64(ns)/3_600_000_000_000)
	}
}

func fmtKB(kb uint64) string { return humanBytes(kb * 1024) }
func fmtKBLimit(kb uint64) string {
	if kb == 0 {
		return "unlimited"
	}
	return humanBytes(kb * 1024)
}
func fmtBytes(b uint64) string { return humanBytes(b) }
func fmtBytesLimit(b uint64) string {
	if b == 0 {
		return "unlimited"
	}
	return humanBytes(b)
}
func fmtUint(v uint64) string {
	if v == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", v)
}

// looksLikeULID returns true when arg matches Crockford-base32 ULID
// shape (26 chars, [0-9A-HJKMNP-TV-Z]). OS usernames cap at 32 chars
// per useradd but rarely match this exact pattern, so a true here is
// a strong signal the operator typed a panel user_id by mistake.
func looksLikeULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	upper := strings.ToUpper(s)
	for _, c := range upper {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
		switch c {
		case 'I', 'L', 'O', 'U':
			return false
		}
	}
	return true
}
