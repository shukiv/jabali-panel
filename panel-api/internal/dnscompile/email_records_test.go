package dnscompile

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// idCounter returns a stable id generator so assertions can pin exact
// values instead of doing wildcard matches. Tests that don't care about
// id content pass the result through anyway.
func idCounter() func() string {
	n := 0
	return func() string {
		n++
		return "id" + strconv.Itoa(n)
	}
}

func TestBuildEmailRecords_ShapeAndContent(t *testing.T) {
	now := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	recs := BuildEmailRecords(
		"zone1",
		"example.com",
		"jabali",
		"v=DKIM1; k=ed25519; p=AAAA",
		nil,
		idCounter(),
		now,
	)

	require.Len(t, recs, 15, "M6 should inject 15 records — DKIM + autoconfig + autodiscover + CalDAV/CardDAV + client SRVs + TLS-RPT + 2 CAA")

	// Record 0 — DKIM TXT. Quoted content to match BootstrapRecords.
	require.Equal(t, "jabali._domainkey", recs[0].Name)
	require.Equal(t, "TXT", recs[0].Type)
	require.Equal(t, `"v=DKIM1; k=ed25519; p=AAAA"`, recs[0].Content)

	// Record 1 — autoconfig CNAME. Target is the FQDN so PDNS serves
	// a resolvable answer (short labels would be served verbatim as a
	// root-relative name and fail).
	require.Equal(t, "autoconfig", recs[1].Name)
	require.Equal(t, "CNAME", recs[1].Type)
	require.Equal(t, "mail.example.com", recs[1].Content)

	// Record 2 — _autodiscover._tcp SRV per RFC 2782. Target FQDN for
	// the same reason as the CNAME above.
	require.Equal(t, "_autodiscover._tcp", recs[2].Name)
	require.Equal(t, "SRV", recs[2].Type)
	require.Equal(t, "0 0 443 mail.example.com", recs[2].Content)

	// Records 3-6 — CalDAV/CardDAV SRV for RFC 6764 (HTTPS on 443,
	// plain HTTP on 80 as fallback).
	require.Equal(t, "_caldavs._tcp", recs[3].Name)
	require.Equal(t, "SRV", recs[3].Type)
	require.Equal(t, "0 1 443 mail.example.com", recs[3].Content)
	require.Equal(t, "_carddavs._tcp", recs[4].Name)
	require.Equal(t, "SRV", recs[4].Type)
	require.Equal(t, "0 1 443 mail.example.com", recs[4].Content)
	require.Equal(t, "_caldav._tcp", recs[5].Name)
	require.Equal(t, "SRV", recs[5].Type)
	require.Equal(t, "0 1 80 mail.example.com", recs[5].Content)
	require.Equal(t, "_carddav._tcp", recs[6].Name)
	require.Equal(t, "SRV", recs[6].Type)
	require.Equal(t, "0 1 80 mail.example.com", recs[6].Content)

	// GH #134 additions (appended after the existing set).
	require.Equal(t, "autodiscover", recs[7].Name)
	require.Equal(t, "CNAME", recs[7].Type)
	require.Equal(t, "mail.example.com", recs[7].Content)

	require.Equal(t, "_imap._tcp", recs[8].Name)
	require.Equal(t, "0 1 143 mail.example.com", recs[8].Content)
	require.Equal(t, "_imaps._tcp", recs[9].Name)
	require.Equal(t, "0 1 993 mail.example.com", recs[9].Content)
	require.Equal(t, "_submission._tcp", recs[10].Name)
	require.Equal(t, "0 1 587 mail.example.com", recs[10].Content)
	require.Equal(t, "_submissions._tcp", recs[11].Name)
	require.Equal(t, "0 1 465 mail.example.com", recs[11].Content)

	require.Equal(t, "_smtp._tls", recs[12].Name)
	require.Equal(t, "TXT", recs[12].Type)
	require.Equal(t, `"v=TLSRPTv1; rua=mailto:postmaster@example.com"`, recs[12].Content)

	require.Equal(t, "@", recs[13].Name)
	require.Equal(t, "CAA", recs[13].Type)
	require.Equal(t, `0 issue "letsencrypt.org"`, recs[13].Content)
	require.Equal(t, "@", recs[14].Name)
	require.Equal(t, "CAA", recs[14].Type)
	require.Equal(t, `0 iodef "mailto:postmaster@example.com"`, recs[14].Content)

	// All three must be flagged Managed + ManagedBy="m6" so the
	// delete-on-disable WHERE clause can find them without touching
	// M4 bootstrap records (which have ManagedBy=NULL).
	for i, r := range recs {
		require.True(t, r.Managed, "rec[%d].Managed should be true", i)
		require.NotNil(t, r.ManagedBy, "rec[%d].ManagedBy should be set", i)
		require.Equal(t, "m6", *r.ManagedBy, "rec[%d].ManagedBy", i)
		require.True(t, r.IsEnabled, "rec[%d].IsEnabled", i)
		require.Equal(t, "zone1", r.ZoneID, "rec[%d].ZoneID", i)
		require.Equal(t, now, r.CreatedAt, "rec[%d].CreatedAt", i)
	}
}

// Custom selector paths out to a different name — confirms we don't
// hardcode "jabali" downstream of the argument. When ADR-0043's
// rotation lands, the handler will pass the rotated selector through.
func TestBuildEmailRecords_HonorsSelector(t *testing.T) {
	recs := BuildEmailRecords(
		"z", "example.com", "rotated-20260101", "dummy-pub", nil, idCounter(), time.Now(),
	)
	require.Equal(t, "rotated-20260101._domainkey", recs[0].Name)
}

// idNew must be called exactly once per record — guards against a
// refactor that accidentally reuses ids across records (which would
// violate the PK and blow up Create).
func TestBuildEmailRecords_IDGeneratorCalledOncePerRecord(t *testing.T) {
	recs := BuildEmailRecords("z", "example.com", "s", "p", nil, idCounter(), time.Now())
	seen := map[string]bool{}
	for _, r := range recs {
		require.False(t, seen[r.ID], "duplicate id %q", r.ID)
		seen[r.ID] = true
	}
	require.Len(t, seen, 15)
}

// Sanity — the exported constants used across the package and the
// email-handler don't drift accidentally.
func TestExportedConstants(t *testing.T) {
	require.Equal(t, "m6", EmailRecordsManagedBy)
	require.Equal(t, "jabali", EmailRecordsSelector)
}
