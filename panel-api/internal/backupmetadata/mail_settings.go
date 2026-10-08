package backupmetadata

import (
	"context"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a backup carries a domain's mail settings, and a restore brings
// them back through the checks the domain's pages run (CheckDomain). The
// mail provider is restored on a new domain only: on an existing one a
// switch republishes its mail DNS and certificate, so the overwrite leaves
// it, and the provider's tokens, as they are.

// setMetadataMailSettings records dom's mail settings in the backup.
func setMetadataMailSettings(dst *internalbackup.MetadataDomain, dom *models.Domain) {
	dst.MailProvider = dom.MailProvider
	dst.M365Onmicrosoft, dst.GoogleDKIM = dom.M365Onmicrosoft, dom.GoogleDKIM
	dst.DmarcNP, dst.DmarcTesting = dom.DmarcNP, dom.DmarcTesting
	dst.CalDAVHost, dst.CardDAVHost = dom.CalDAVHost, dom.CardDAVHost
	dst.MTASTSEnabled = dom.MTASTSEnabled
}

// hasMailSettings reports whether the backup carries the mail settings. The
// provider column is never empty, so a backup made since it was recorded
// always names one.
func hasMailSettings(dm internalbackup.MetadataDomain) bool { return dm.MailProvider != "" }

// setRestoredMailSettings gives a new domain row the backup's mail settings.
// MTA-STS is not among them: enableRestoredMTASTS turns it on once the row
// exists, which mints the policy id.
func setRestoredMailSettings(row *models.Domain, dm internalbackup.MetadataDomain) {
	row.MailProvider = dm.MailProvider
	row.M365Onmicrosoft, row.GoogleDKIM = dm.M365Onmicrosoft, dm.GoogleDKIM
	row.DmarcNP, row.DmarcTesting = dm.DmarcNP, dm.DmarcTesting
	row.CalDAVHost, row.CardDAVHost = dm.CalDAVHost, dm.CardDAVHost
}

// enableRestoredMTASTS turns MTA-STS on for row, as the domain's mail page
// does, and reports whether it did. Its two DNS records are what lets a
// receiving server find the policy, so it is turned on only when the restore
// then publishes them (RestoresDNS): never for a domain whose DNS is hosted
// elsewhere, or whose ownership isn't verified (it has no zone yet). The
// report says why it stayed off.
func enableRestoredMTASTS(ctx context.Context, d Deps, report func(string, ...any), row *models.Domain) bool {
	const later = "turn it on in the domain's mail settings"
	switch {
	case !d.RestoresDNS:
		report("MTA-STS not turned on: this restore doesn't restore DNS records, which publish its policy; " + later)
	case row.DNSDisabled:
		report("MTA-STS not turned on: DNS for this domain is hosted elsewhere; publish its MTA-STS records there, then " + later)
	case !domainops.OwnershipVerified(row):
		report("MTA-STS not turned on: the domain's ownership isn't verified yet; " + later + " once it is")
	default:
		if _, err := d.Domains.UpdateMTASTSEnabled(ctx, row.ID, true); err != nil {
			report("MTA-STS not turned on: %v", err)
			return false
		}
		return true
	}
	return false
}
