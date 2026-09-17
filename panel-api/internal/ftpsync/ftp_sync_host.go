package ftpsync

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// FtpAccountLister is the minimal interface SyncFtpHostAccess needs to read the
// full account set (repo-free for testing). Satisfied by repository.FtpAccountRepository.
type FtpAccountLister interface {
	List(ctx context.Context) ([]models.FtpAccount, error)
}

// SyncFtpHostAccess re-renders the sshd drop-in from the full desired set and
// flips the tenant home to the M12 chroot layout. Called synchronously after
// every mutation so login state matches the DB. This is the shared
// implementation used by both the API's immediate syncHostAccess and the
// suspend/unsuspend/teardown paths, ensuring all dispatch sites render
// identical effective-access sets (JAB-276).
func SyncFtpHostAccess(
	ctx context.Context,
	ag agent.AgentInterface,
	ftpRepo FtpAccountLister,
	userRepo repository.UserRepository,
	pkgRepo PackageGetter,
	log *slog.Logger,
	tenantUsername string,
) {
	if ag == nil || ftpRepo == nil || userRepo == nil || tenantUsername == "" {
		return
	}

	// Home chroot flip; agent will retry on the next reconcile tick if this fails.
	if _, err := ag.Call(ctx, "ssh.user.home_chown", map[string]any{
		"username": tenantUsername,
		"mode":     "sftp",
	}); err != nil && log != nil {
		log.Warn("ftp: home chroot flip failed (reconciler will retry)", "tenant", tenantUsername, "err", err)
	}

	// Stamp the generation BEFORE reading the snapshot. The agent's stale-drop
	// gate is only sound if stamp order precedes read order: a sync that can
	// override a revocation carries a higher generation, so it was stamped after
	// the revocation's stamp, so — stamping before reading — it read after the
	// revocation committed and its own snapshot already reflects the revocation.
	// Stamping at dispatch (after the read) reopens the JAB-267 inversion.
	gen := NextGeneration()
	rows, err := ftpRepo.List(ctx)
	if err != nil {
		return
	}

	// Group rows by tenant and resolve each owner's eligibility ONCE with the
	// SAME shared projection the reconciler applies (JAB-276). Before this, the
	// immediate sync filtered on each row's own IsEnabled/SFTPAccess alone —
	// eligibility-blind — so a mutation on ANY tenant re-dispatched the whole
	// table under a fresh (higher) generation that could re-emit the sshd Match
	// block of a suspended / package-dropped / over-cap account the reconciler
	// had already dropped. Clamping eligibility here keeps both dispatch sites'
	// effective-access sets identical, so the immediate sync can never be wider
	// than the periodic one.
	tenantByUserID := map[string]string{}
	rowsByTenant := map[string][]models.FtpAccount{}
	eligByTenant := map[string]Eligibility{}
	for _, a := range rows {
		uname, seen := tenantByUserID[a.UserID]
		if !seen {
			u, uerr := userRepo.FindByID(ctx, a.UserID)
			if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
				tenantByUserID[a.UserID] = ""
				continue
			}
			uname = *u.Username
			tenantByUserID[a.UserID] = uname
			eligByTenant[uname] = OwnerEligibility(ctx, pkgRepo, u)
		}
		if uname == "" {
			continue
		}
		rowsByTenant[uname] = append(rowsByTenant[uname], a)
	}

	// A cancelled request makes the owner lookups above fail closed, which would
	// narrow the dispatched set for a reason unrelated to policy AND stamp that
	// narrower set with a fresh generation. Bail exactly as a failed List does —
	// never publish a snapshot shaped by cancellation; the periodic reconciler
	// converges regardless.
	if ctx.Err() != nil {
		return
	}

	type syncAccount struct {
		Username  string `json:"username"`
		ChrootDir string `json:"chroot_dir"`
		StartDir  string `json:"start_dir"`
	}
	desired := []syncAccount{}
	for tenant, accts := range rowsByTenant {
		eff := EffectiveEnabled(accts, eligByTenant[tenant])
		for _, a := range accts {
			if !eff[a.Username] || !a.SFTPAccess {
				continue
			}
			var chroot, start string
			if a.Isolated && a.JailPath != "" {
				// GH #1145: isolated accounts chroot to their root-owned jail; the
				// selected sub-tree is bind-mounted at /data inside it.
				chroot = a.JailPath
				start = "/data"
			} else {
				chroot = "/home/" + tenant
				start = "/"
				if rel, rerr := filepath.Rel(chroot, a.HomePath); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					start = "/" + rel
				}
			}
			desired = append(desired, syncAccount{Username: a.Username, ChrootDir: chroot, StartDir: start})
		}
	}
	if _, err := ag.Call(ctx, "ftpaccount.sshd_sync", map[string]any{
		"accounts":   desired,
		"generation": gen,
	}); err != nil && log != nil {
		log.Warn("ftp: sshd_sync failed (reconciler will retry)", "err", err)
	}
}
