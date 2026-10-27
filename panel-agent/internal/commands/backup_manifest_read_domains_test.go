package commands

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #2303: the restore picker lists every domain of the backup for its
// files, not only the domains with custom DNS records.
func TestManifestDomainLists(t *testing.T) {
	meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{
		{Name: "plain.example"},
		{Name: "dns.example", DNSRecords: []backup.MetadataDNSRecord{{Name: "www", Type: "CNAME", Content: "dns.example."}}},
	}}
	all, withDNS := manifestDomainLists(meta)
	if want := []string{"plain.example", "dns.example"}; !reflect.DeepEqual(all, want) {
		t.Errorf("all = %v, want %v", all, want)
	}
	if want := []string{"dns.example"}; !reflect.DeepEqual(withDNS, want) {
		t.Errorf("withDNS = %v, want %v", withDNS, want)
	}

	all, withDNS = manifestDomainLists(&backup.AccountMetadata{})
	if all == nil || withDNS == nil {
		t.Error("an account without domains must give empty lists, not null")
	}
}
