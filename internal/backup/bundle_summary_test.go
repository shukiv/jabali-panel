package backup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// GH #1993: the restore preflight reads a summary of the backup's metadata.

func TestSummarizeMetadata(t *testing.T) {
	m := &AccountMetadata{
		PHPPools: []MetadataPHPPool{{PHPVersion: "8.3"}, {PHPVersion: "7.4"}, {PHPVersion: "8.3"}},
		PHPExtensions: map[string][]string{
			"8.3": {"redis", "intl", "redis"},
			"8.1": {"imagick"}, // no pool uses it
		},
		Databases: []MetadataDatabase{
			{ID: "d1", Name: "alice_shop", Engine: "postgres"},
			{ID: "d2", Name: "alice_wp", Engine: "mariadb"},
			{ID: "", Name: "alice_crm", Engine: "postgres"},
		},
		DatabaseUsers: []MetadataDatabaseUser{
			{Username: "alice_pg", Engine: "postgres"},
			{Username: "alice_my", Engine: "mariadb", Grants: []MetadataDatabaseUserGrant{{DatabaseID: "d1"}}},
			// Older bundles carry no engine: one granted on a PostgreSQL
			// database is a PostgreSQL user, by id or by name.
			{Username: "alice_old1", Grants: []MetadataDatabaseUserGrant{{DatabaseID: "d1"}}},
			{Username: "alice_old2", Grants: []MetadataDatabaseUserGrant{{DatabaseName: "alice_crm"}}},
			// An empty id is no id: it doesn't match alice_crm's.
			{Username: "alice_old3", Grants: []MetadataDatabaseUserGrant{{DatabaseID: "", DatabaseName: "alice_wp"}}},
		},
		Domains: []MetadataDomain{
			{Mailboxes: []MetadataMailbox{{}, {}}, Forwarders: []MetadataForwarder{{}}, DNSRecords: []MetadataDNSRecord{{}, {}, {}}},
			{Mailboxes: []MetadataMailbox{{}}},
		},
		DockerApps: []MetadataDockerApp{
			{Slug: "n8n", InstanceSlug: "n8n-2"},
			{Slug: "gitea"},
			{Slug: "uptime-kuma", ServerLevel: true},
		},
	}
	got := SummarizeMetadata(m)
	want := BundleSummary{
		PHPVersions:       []string{"7.4", "8.3"},
		PHPExtensions:     map[string][]string{"8.3": {"intl", "redis"}},
		PostgresDatabases: []string{"alice_crm", "alice_shop"},
		PostgresUsers:     3,
		Mailboxes:         3,
		Forwarders:        1,
		DNSRecords:        3,
		DockerApps:        []string{"gitea", "n8n-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summary\n got %+v\nwant %+v", got, want)
	}
}

func TestSummarizeMetadata_ExtensionsNotRecorded(t *testing.T) {
	got := SummarizeMetadata(&AccountMetadata{PHPPools: []MetadataPHPPool{{PHPVersion: "8.3"}}})
	if got.PHPExtensions != nil {
		t.Errorf("a bundle without php_extensions summarized as %v, want nil (not recorded)", got.PHPExtensions)
	}
	if empty := SummarizeMetadata(nil); empty.PHPVersions == nil || empty.PostgresDatabases == nil || empty.DockerApps == nil {
		t.Errorf("nil metadata: %+v, want empty lists, not null", empty)
	}
}

// The bundle is the uploader's file: the summary bounds what it repeats.
func TestSummarizeMetadata_Bounds(t *testing.T) {
	m := &AccountMetadata{PHPPools: []MetadataPHPPool{{PHPVersion: strings.Repeat("9", 100)}}}
	for i := 0; i < 60; i++ {
		m.Databases = append(m.Databases, MetadataDatabase{Name: fmt.Sprintf("alice_db%02d", i), Engine: "postgres"})
	}
	got := SummarizeMetadata(m)
	if len(got.PostgresDatabases) != summaryListMax {
		t.Errorf("%d database names, want %d", len(got.PostgresDatabases), summaryListMax)
	}
	if len(got.PHPVersions[0]) != summaryNameMax {
		t.Errorf("a %d-byte version name, want it clipped to %d", len(got.PHPVersions[0]), summaryNameMax)
	}
}

// The summary goes to the browser; the bundle holds password hashes.
func TestSummarizeMetadata_HoldsNoSecret(t *testing.T) {
	m := &AccountMetadata{
		User:          MetadataUser{PasswordHash: "SECRET-user"},
		Databases:     []MetadataDatabase{{ID: "d1", Name: "alice_shop", Engine: "postgres"}},
		DatabaseUsers: []MetadataDatabaseUser{{Username: "alice_pg", Engine: "postgres", PasswordHash: "SECRET-row", PostgresPasswordVerifier: "SECRET-scram", NativePasswordHash: "SECRET-native"}},
		Domains:       []MetadataDomain{{Mailboxes: []MetadataMailbox{{PasswordHash: "SECRET-mail"}}}},
	}
	out, _ := json.Marshal(SummarizeMetadata(m))
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("summary carries a secret: %s", out)
	}
}
