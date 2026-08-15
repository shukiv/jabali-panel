package directadmin

import (
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
)

// ToCpanelParsed produces a *cpanel.ParsedTarball view over a
// DAParsedTarball so the cpanel restore writers (ImportDatabases,
// ImportSSHKeys, ImportCron) run unchanged against DA-extracted
// content. Per-area mapping:
//
//	DAParsedTarball.MySQLDumps     → cpanel.ParsedTarball.MySQLDumps
//	DAParsedTarball.SSHAuthorized  → cpanel.ParsedTarball.SSHAuthorized (wrap to list)
//	DAParsedTarball.CronFile       → cpanel.ParsedTarball.CronFiles (wrap to list)
//	DAParsedTarball.HomeDir        → cpanel.ParsedTarball.HomeDir
//	DAParsedTarball.SourceUser     → cpanel.ParsedTarball.SourceUser
//
// Fields cpanel writers care about that DA doesn't populate:
//   - ZoneFiles: DA backup tar doesn't contain BIND zones; DNS
//     migration is operator-driven (run dns.zone.upsert manually
//     against parsed `da admin user.show -d <dom>` output).
//
// ImportHome (cpanel rsync) still needs an adapter wrapper because
// DA's homedir layout differs (<user>/domains/<dom> vs cpanel's
// <user>/public_html). That adapter ships in a follow-up — for v1
// the operator hand-rsyncs the DA domains/ subtree.
// targetUsername is the dest jabali username. Used to seed DocRoots
// so the domain.create call (nginx vhost) points at the same
// /home/<target>/domains/<dom>/public_html that ImportHome rsync'd
// the DA content into.
func ToCpanelParsed(da *DAParsedTarball, targetUsername string) *cpanel.ParsedTarball {
	if da == nil {
		return nil
	}
	out := &cpanel.ParsedTarball{
		SourceUser: da.SourceUser,
		ExtractDir: da.ExtractDir,
		HomeDir:    da.HomeDir,
		MailRoot:   da.MailRoot, // DA stores at <user>/email/, cpanel at <user>/mail/
		MySQLDumps: da.MySQLDumps,
		Skipped:    da.Skipped,
	}
	if da.SSHAuthorized != "" {
		out.SSHAuthorized = []string{da.SSHAuthorized}
	}
	if da.CronFile != "" {
		out.CronFiles = []string{da.CronFile}
	}
	// Map source-side DA domain dirs onto target-side panel layout.
	// Source: <extract>/<user>/domains/<dom>/public_html
	// Target: /home/<target>/domains/<dom>/public_html
	// We can't know targetUsername here (adapter is pre-restore-stage
	// — the target user is bound when the restore callback runs). So
	// we leave DocRoots empty here; ImportDomains uses its default
	// docroot for DA (covered by the fallback path in
	// cpanel.ImportDomains when DocRoots[name] is missing).
	if len(da.DomainDirs) > 0 {
		out.DomainNames = make([]string, 0, len(da.DomainDirs))
		out.DocRoots = make(map[string]string, len(da.DomainDirs))
		for name := range da.DomainDirs {
			out.DomainNames = append(out.DomainNames, name)
			if targetUsername != "" {
				// Post-rsync target path. ImportHome ran first and
				// copied <extract>/<user>/domains/<dom>/ →
				// /home/<target>/domains/<dom>/.
				out.DocRoots[name] = filepath.Join("/home", targetUsername, "domains", name, "public_html")
			}
		}
	}
	return out
}
