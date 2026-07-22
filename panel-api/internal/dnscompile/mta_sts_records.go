package dnscompile

import (
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MTAStsRecordsManagedBy is the marker stamped into dns_records.managed_by
// for the two MTA-STS records emitted per domain. Used by the disable
// path to clean up just these rows without disturbing M4 bootstrap or
// M6 email records (which use ManagedBy="m6" / NULL respectively).
const MTAStsRecordsManagedBy = "mta-sts"

// BuildMTAStsRecords returns the per-domain DNS records that publish a
// MTA-STS policy (ADR-0109). Two records:
//
//	mta-sts          A     <panel-ipv4>
//	_mta-sts         TXT   "v=STSv1; id=<mta_sts_id>"
//
// The id field doubles as the policy version cookie — receivers re-fetch
// the policy file whenever the TXT value changes. The repo bumps id on
// every enable so re-toggling forces caches to invalidate.
//
// panelIPv4 is the public IP the agent's mta-sts vhost listens on. We
// could publish a CNAME to the panel hostname instead, but an A is
// faster (one lookup vs two) and matches the rest of jabali's
// bootstrap-record shape.
//
// Returns nil + a hint string when panelIPv4 is empty — the operator
// will see this in the UI status so they know to set the public IP in
// server settings before MTA-STS will work end-to-end.
func BuildMTAStsRecords(
	zoneID, zoneName, panelIPv4 string,
	mtaStsID uint64,
	srv *models.ServerSettings,
	idNew func() string,
	now time.Time,
) []models.DNSRecord {
	if panelIPv4 == "" || mtaStsID == 0 {
		return nil
	}
	marker := MTAStsRecordsManagedBy
	mk := func(name, typ, content string) models.DNSRecord {
		return models.DNSRecord{
			ID:        idNew(),
			ZoneID:    zoneID,
			Name:      name,
			Type:      typ,
			Content:   content,
			TTL:       models.EffectiveDNSTTL(srv),
			Priority:  0,
			Managed:   true,
			ManagedBy: &marker,
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	return []models.DNSRecord{
		mk("mta-sts", "A", panelIPv4),
		mk("_mta-sts", "TXT", `"v=STSv1; id=`+strconv.FormatUint(mtaStsID, 10)+`"`),
	}
}
