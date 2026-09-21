package ftpsync

import (
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JailMountpointDir is the bind-mount subdir inside a GH #1145 isolated jail; an
// isolated account's post-chroot session start dir is always "/<JailMountpointDir>".
//
// MUST match panel-agent internal/commands/ftp_account_jail.go ftpJailMountpoint.
// That is a separate binary, so the value is duplicated across the panel↔agent
// boundary rather than shared. Within panel-api it now has exactly one home (this
// const); the reconciler and the API immediate-sync both read it through the
// shared renderer below, so the two panel dispatch sites can no longer diverge
// (JAB-276 AC3).
const JailMountpointDir = "data"

// SyncAccount is one entry in the ftpaccount.sshd_sync desired set. Its JSON
// shape is the panel→agent wire contract for the sshd drop-in; the agent
// (ftp_account_sshd.go) renders a Match block per entry from these three fields.
type SyncAccount struct {
	Username  string `json:"username"`
	ChrootDir string `json:"chroot_dir"`
	StartDir  string `json:"start_dir"`
}

// RenderDesiredAccounts builds the ftpaccount.sshd_sync desired set shared by the
// API immediate-sync (SyncFtpHostAccess) and the periodic reconciler, so the jail
// + start-dir projection — JAB-276 AC3's "what" — lives in one place next to the
// eligibility "who" (EffectiveEnabled). Only enabled+SFTP rows are rendered.
// StartDir is home_path relative to the tenant home; internal-sftp resolves it
// inside the chroot.
//
// Before this, the two sites rendered the same logic from two copies — the
// reconciler off ftpJailMountpointDir and the API off a "/data" string literal —
// so an edit to one could silently widen or reshape only that site's dispatch.
//
// The returned slice's order follows Go map iteration over rowsByTenant and is
// therefore nondeterministic across tenants. The agent keys on username, so
// order is not part of the wire contract. (Pre-existing behaviour; unchanged.)
func RenderDesiredAccounts(rowsByTenant map[string][]models.FtpAccount, eligByTenant map[string]Eligibility) []SyncAccount {
	desired := []SyncAccount{}
	for tenant, accts := range rowsByTenant {
		eff := EffectiveEnabled(accts, eligByTenant[tenant])
		for _, a := range accts {
			if !eff[a.Username] || !a.SFTPAccess {
				continue
			}
			var chroot, start string
			if a.Isolated && a.JailPath != "" {
				// GH #1145: isolated accounts chroot to their root-owned jail; the
				// selected sub-tree is bind-mounted at /<JailMountpointDir> inside it.
				chroot = a.JailPath
				start = "/" + JailMountpointDir
			} else {
				chroot = "/home/" + tenant
				start = "/"
				if rel, rerr := filepath.Rel(chroot, a.HomePath); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					start = "/" + rel
				}
			}
			desired = append(desired, SyncAccount{Username: a.Username, ChrootDir: chroot, StartDir: start})
		}
	}
	return desired
}
