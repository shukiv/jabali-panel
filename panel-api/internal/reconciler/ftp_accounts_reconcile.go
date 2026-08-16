package reconciler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// FTP/SFTP subaccount convergence (GH #1053 step 3,
// plans/gh1053-ftp-accounts.md). DB rows are the truth; the host's passwd
// aliases, jabali-ftp memberships, lock states, and the sshd jabali-xfer
// drop-in all converge to them every tick (hash-gated to a no-op in steady
// state, force-resynced on a bounded interval to heal out-of-band drift).
//
// The API paths do their host mutations synchronously (create/delete talk
// to the agent first, row second), so this pass is the drift healer:
// missing aliases after a DR restore, hand-run userdel, group edits, or a
// stale drop-in all get repaired here.

const ftpAccountsReDispatchInterval = 15 * time.Minute

type ftpDispatchState struct {
	Hash string
	At   time.Time
}

// desiredFtpHash covers every reconcile input: each row's identity +
// flags + home, and the owning tenant's username (renames change the
// rendered config without touching ftp_accounts rows).
func desiredFtpHash(rows []models.FtpAccount, tenantByUserID map[string]string) string {
	lines := make([]string, 0, len(rows))
	for _, a := range rows {
		lines = append(lines, strings.Join([]string{
			a.ID, a.Username, tenantByUserID[a.UserID], a.HomePath,
			fmt.Sprintf("%t|%t|%t", a.FTPAccess, a.SFTPAccess, a.IsEnabled),
		}, "^"))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\nn=" + fmt.Sprint(len(lines))))
	return hex.EncodeToString(sum[:])
}

// randomThrowawayPassword returns a high-entropy password for recreating a
// drifted alias whose real password lives only in the lost /etc/shadow
// entry. Nobody knows it — the account exists but cannot authenticate
// until the tenant resets the password through the panel. That is the
// correct DR posture: silently minting a KNOWN password would be worse.
func randomThrowawayPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entropy: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

type agentFtpListEntry struct {
	Username  string `json:"username"`
	HomePath  string `json:"home_path"`
	FTPAccess bool   `json:"ftp_access"`
	Locked    bool   `json:"locked"`
}

// reconcileFtpAccounts converges host state to the ftp_accounts table.
// Never returns an error — SSL-reconciler convention: log and let the
// rest of the tick proceed.
// ftpOwnerEligibility captures whether a tenant may currently HOLD active
// FTP/SFTP credentials and how many. Ineligible owners (JAB-254 suspended,
// JAB-258 package dropped the feature) must have every alias locked +
// degrouped, and an over-cap owner (JAB-258 downgrade to a lower cap)
// keeps only its oldest `cap` accounts.
type ftpOwnerEligibility struct {
	eligible bool // false => lock ALL of this tenant's aliases
	cap      int  // max simultaneously-enabled accounts (0 when !eligible)
}

// ftpEffectiveEnabled applies owner eligibility on top of a row's own
// IsEnabled. accts MUST be the tenant's full set; over-cap accounts
// (oldest-first) beyond `cap` are forced disabled deterministically.
// Returns username -> effective-enabled.
func ftpEffectiveEnabled(accts []models.FtpAccount, elig ftpOwnerEligibility) map[string]bool {
	out := make(map[string]bool, len(accts))
	if !elig.eligible {
		for _, a := range accts {
			out[a.Username] = false
		}
		return out
	}
	// Oldest-first so a cap reduction disables the NEWEST accounts,
	// keeping the tenant's long-standing credentials working.
	ordered := append([]models.FtpAccount(nil), accts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})
	kept := 0
	for _, a := range ordered {
		if !a.IsEnabled {
			out[a.Username] = false
			continue
		}
		if elig.cap > 0 && kept >= elig.cap {
			out[a.Username] = false // over-cap: excess disabled
			continue
		}
		out[a.Username] = true
		kept++
	}
	return out
}

// ftpOwnerEligibility resolves whether a tenant may hold active FTP/SFTP
// credentials right now: NOT administratively suspended (JAB-254) AND the
// hosting package still includes the feature (max_ftp_accounts > 0,
// JAB-258). A missing package or lookup failure is treated as ineligible
// (fail closed) — a same-uid credential is privileged surface.
func (r *Reconciler) ftpOwnerEligibility(ctx context.Context, u *models.User) ftpOwnerEligibility {
	if u == nil || u.Suspended {
		return ftpOwnerEligibility{eligible: false}
	}
	if r.packages == nil || u.PackageID == nil || *u.PackageID == "" {
		return ftpOwnerEligibility{eligible: false}
	}
	pkg, err := r.packages.FindByID(ctx, *u.PackageID)
	if err != nil || pkg == nil || pkg.MaxFTPAccounts == 0 {
		return ftpOwnerEligibility{eligible: false}
	}
	return ftpOwnerEligibility{eligible: true, cap: int(pkg.MaxFTPAccounts)}
}

// ftpEligHash folds owner eligibility into the reconcile hash so a
// suspension or package downgrade forces a re-dispatch instead of being
// masked by the steady-state cache.
func ftpEligHash(elig map[string]ftpOwnerEligibility) string {
	keys := make([]string, 0, len(elig))
	for k := range elig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		e := elig[k]
		fmt.Fprintf(&b, "%s=%t:%d;", k, e.eligible, e.cap)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func (r *Reconciler) reconcileFtpAccounts(ctx context.Context) {
	if r.ftpAccounts == nil || r.users == nil || r.agent == nil {
		return
	}
	rows, err := r.ftpAccounts.List(ctx)
	if err != nil {
		r.log.Error("ftp: list accounts failed", "err", err)
		return
	}

	// Resolve tenant usernames once; drop rows whose owner has no Linux
	// identity (deleted user mid-flight, admin rows) with a warning —
	// they cannot converge and must not kill the pass.
	tenantByUserID := map[string]string{}
	rowsByTenant := map[string][]models.FtpAccount{}
	eligByTenant := map[string]ftpOwnerEligibility{}
	for _, a := range rows {
		uname, ok := tenantByUserID[a.UserID]
		if !ok {
			u, uerr := r.users.FindByID(ctx, a.UserID)
			if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
				r.log.Warn("ftp: account owner has no linux username — skipping row", "account", a.Username, "user_id", a.UserID)
				tenantByUserID[a.UserID] = ""
				continue
			}
			uname = *u.Username
			tenantByUserID[a.UserID] = uname
			eligByTenant[uname] = r.ftpOwnerEligibility(ctx, u)
		}
		if uname == "" {
			continue
		}
		rowsByTenant[uname] = append(rowsByTenant[uname], a)
	}

	fullHash := desiredFtpHash(rows, tenantByUserID) + ftpEligHash(eligByTenant)
	if v, ok := r.ftpDispatchCache.Load("all"); ok {
		if st, okT := v.(ftpDispatchState); okT && st.Hash == fullHash && time.Since(st.At) < ftpAccountsReDispatchInterval {
			return
		}
	}

	converged := true
	for tenant, accts := range rowsByTenant {
		if !r.reconcileFtpTenant(ctx, tenant, accts, eligByTenant[tenant]) {
			converged = false
		}
	}

	// Global stray sweep. The per-tenant diff above only sees tenants that
	// still HAVE rows — a manual row delete (or a tenant whose last row
	// vanished out-of-band) would leave a rowless alias that is a working
	// credential with no panel handle (proven live on jabalitests). One
	// ftpaccount.list_all IPC catches every alias host-wide.
	if !r.sweepStrayFtpAliases(ctx, rows) {
		converged = false
	}

	// Render the sshd drop-in from every enabled+sftp row (all tenants,
	// one file). StartDir is home_path relative to the tenant home —
	// internal-sftp resolves it inside the chroot.
	type syncAccount struct {
		Username  string `json:"username"`
		ChrootDir string `json:"chroot_dir"`
		StartDir  string `json:"start_dir"`
	}
	desired := []syncAccount{}
	for tenant, accts := range rowsByTenant {
		chroot := "/home/" + tenant
		eff := ftpEffectiveEnabled(accts, eligByTenant[tenant])
		for _, a := range accts {
			if !eff[a.Username] || !a.SFTPAccess {
				continue
			}
			start := "/"
			if rel, rerr := filepath.Rel(chroot, a.HomePath); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				start = "/" + rel
			}
			desired = append(desired, syncAccount{Username: a.Username, ChrootDir: chroot, StartDir: start})
		}
	}
	if _, err := r.agent.Call(ctx, "ftpaccount.sshd_sync", map[string]any{"accounts": desired}); err != nil {
		r.log.Warn("ftp: sshd_sync failed (will retry next pass)", "err", err)
		converged = false
	}

	if converged {
		r.ftpDispatchCache.Store("all", ftpDispatchState{Hash: fullHash, At: time.Now()})
	}
}

// sweepStrayFtpAliases removes host subaccount aliases that no ftp_accounts
// row accounts for, across ALL tenants — including tenants with zero
// remaining rows, which the per-tenant diff can never see. Returns false on
// failure so the hash gate stays open.
func (r *Reconciler) sweepStrayFtpAliases(ctx context.Context, rows []models.FtpAccount) bool {
	raw, err := r.agent.Call(ctx, "ftpaccount.list_all", map[string]any{})
	if err != nil {
		r.log.Warn("ftp: list_all failed (stray sweep skipped)", "err", err)
		return false
	}
	var resp struct {
		Accounts []struct {
			TenantUsername string `json:"tenant_username"`
			Username       string `json:"username"`
		} `json:"accounts"`
		// Pattern-matching passwd entries WITHOUT the panel's GECOS
		// ownership marker — operator property the agent refused to
		// classify as ours. Surfaced, never touched.
		SkippedUnmarked int `json:"skipped_unmarked"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("ftp: list_all parse failed", "err", err)
		return false
	}
	if resp.SkippedUnmarked > 0 {
		r.log.Warn("ftp: passwd entries look like subaccount aliases but lack the ownership marker — left untouched (operator-managed?)", "count", resp.SkippedUnmarked)
	}
	wanted := map[string]struct{}{}
	for _, a := range rows {
		wanted[a.Username] = struct{}{}
	}
	ok := true
	for _, h := range resp.Accounts {
		if _, want := wanted[h.Username]; want {
			continue
		}
		if _, err := r.agent.Call(ctx, "ftpaccount.delete", map[string]any{
			"tenant_username": h.TenantUsername,
			"username":        h.Username,
		}); err != nil {
			r.log.Warn("ftp: stray alias removal failed", "account", h.Username, "err", err)
			ok = false
			continue
		}
		r.log.Info("ftp: removed stray subaccount with no panel row", "account", h.Username, "tenant", h.TenantUsername)
	}
	return ok
}

// reconcileFtpTenant diffs one tenant's desired rows against the host's
// passwd view and repairs drift. Returns false when any repair failed
// (keeps the hash gate open so the next tick retries).
func (r *Reconciler) reconcileFtpTenant(ctx context.Context, tenant string, accts []models.FtpAccount, elig ftpOwnerEligibility) bool {
	// JAB-254/258: owner eligibility overrides each row's own IsEnabled —
	// a suspended owner or a package that dropped the feature locks every
	// alias; an over-cap downgrade locks the newest excess.
	eff := ftpEffectiveEnabled(accts, elig)
	raw, err := r.agent.Call(ctx, "ftpaccount.list", map[string]any{"tenant_username": tenant})
	if err != nil {
		r.log.Warn("ftp: host list failed", "tenant", tenant, "err", err)
		return false
	}
	var listResp struct {
		Accounts []agentFtpListEntry `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		r.log.Warn("ftp: host list parse failed", "tenant", tenant, "err", err)
		return false
	}
	host := map[string]agentFtpListEntry{}
	for _, h := range listResp.Accounts {
		host[h.Username] = h
	}
	desired := map[string]models.FtpAccount{}
	for _, a := range accts {
		desired[a.Username] = a
	}

	ok := true
	// Missing or drifted aliases.
	for name, want := range desired {
		got, exists := host[name]
		if !exists {
			pw, perr := randomThrowawayPassword()
			if perr != nil {
				r.log.Error("ftp: throwaway password", "err", perr)
				return false
			}
			if _, err := r.agent.Call(ctx, "ftpaccount.create", map[string]any{
				"tenant_username": tenant,
				"username":        name,
				"home_path":       want.HomePath,
				"password":        pw,
				"ftp_access":      want.FTPAccess,
			}); err != nil {
				r.log.Warn("ftp: recreate missing alias failed", "account", name, "err", err)
				ok = false
				continue
			}
			r.log.Info("ftp: recreated missing subaccount — password reset required before it can log in", "account", name, "tenant", tenant)
			got = agentFtpListEntry{Username: name, FTPAccess: want.FTPAccess, Locked: false}
		}
		enabled := eff[name]
		// Ineligible/over-cap => also drop the jabali-ftp group so the
		// vsftpd PAM gate closes, not just the shadow lock.
		wantFTP := want.FTPAccess && enabled
		wantLocked := !enabled
		if got.FTPAccess != wantFTP || got.Locked != wantLocked {
			if _, err := r.agent.Call(ctx, "ftpaccount.set_access", map[string]any{
				"tenant_username": tenant,
				"username":        name,
				"ftp_access":      wantFTP,
				"enabled":         enabled,
			}); err != nil {
				r.log.Warn("ftp: access drift repair failed", "account", name, "err", err)
				ok = false
			}
		}
	}
	// Stray-alias removal deliberately does NOT live here: the per-tenant
	// diff can only see tenants that still have rows. sweepStrayFtpAliases
	// (one ftpaccount.list_all per pass) owns stray removal host-wide.

	// M12 chroot precondition: sshd only chroots into a root-owned home.
	// Tenants who never used SFTP themselves may still have a
	// tenant-owned /home/<tenant>; flip it via the existing M12 verb when
	// this tenant has at least one SFTP-enabled subaccount.
	for _, a := range accts {
		if eff[a.Username] && a.SFTPAccess {
			if _, err := r.agent.Call(ctx, "ssh.user.home_chown", map[string]any{
				"username": tenant,
				"mode":     "sftp",
			}); err != nil {
				r.log.Warn("ftp: tenant home chroot-perms flip failed", "tenant", tenant, "err", err)
				ok = false
			}
			break
		}
	}
	return ok
}
