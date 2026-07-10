package api

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Pin RFC 1034 §3.6.2 enforcement: CNAME at a name must be the only
// record there. Bug discovered by operator who managed to add two
// CNAMEs for the same host. Without these tests, a future regression
// would only surface when an external DNS resolver complains.
func TestCheckDNSRecordConflict_RejectsDuplicateExact(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "A", "1.2.3.4")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "A", Content: "1.2.3.4"}
	if err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, ""); err == nil {
		t.Error("exact duplicate should be rejected")
	}
}

func TestCheckDNSRecordConflict_AllowsDifferentContent(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "A", "1.2.3.4")
	// Two A records at same name with different content = round-robin, OK.
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "A", Content: "5.6.7.8"}
	if err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, ""); err != nil {
		t.Errorf("round-robin A should be allowed, got: %v", err)
	}
}

func TestCheckDNSRecordConflict_RejectsCNAMEWhenAExists(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "A", "1.2.3.4")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "CNAME", Content: "host.example.com"}
	err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, "")
	if err == nil || !strings.Contains(err.Error(), "RFC 1034") {
		t.Errorf("CNAME-after-A should be rejected with RFC 1034 explanation, got: %v", err)
	}
}

func TestCheckDNSRecordConflict_RejectsAWhenCNAMEExists(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "CNAME", "host.example.com")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "A", Content: "1.2.3.4"}
	err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, "")
	if err == nil || !strings.Contains(err.Error(), "CNAME already exists") {
		t.Errorf("A-after-CNAME should be rejected with CNAME-precedence explanation, got: %v", err)
	}
}

func TestCheckDNSRecordConflict_RejectsTwoCNAMEsAtSameName(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "CNAME", "host1.example.com")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "CNAME", Content: "host2.example.com"}
	err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, "")
	if err == nil {
		t.Error("second CNAME at same name should be rejected (only one allowed per RFC 1034)")
	}
}

func TestCheckDNSRecordConflict_AllowsCNAMEAndAOnDifferentNames(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "A", "1.2.3.4")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "mail", Type: "CNAME", Content: "host.example.com"}
	if err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, ""); err != nil {
		t.Errorf("CNAME on different name from A should be allowed, got: %v", err)
	}
}

// Update of an existing record must skip self-conflict.
func TestCheckDNSRecordConflict_SelfExcludedOnUpdate(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "www", "CNAME", "old.example.com")
	cand := &models.DNSRecord{ID: "rec1", ZoneID: "zone1", Name: "www", Type: "CNAME", Content: "new.example.com"}
	if err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, "rec1"); err != nil {
		t.Errorf("self-edit should be exempt from conflict check, got: %v", err)
	}
}

// Case-insensitive name matching: "WWW" + "www" are the same name.
func TestCheckDNSRecordConflict_CaseInsensitiveName(t *testing.T) {
	r := newMockDNSRecordRepo()
	seedRecord(r, "rec1", "zone1", "WWW", "A", "1.2.3.4")
	cand := &models.DNSRecord{ZoneID: "zone1", Name: "www", Type: "CNAME", Content: "host.example.com"}
	err := CheckDNSRecordConflict(context.Background(), r, "zone1", cand, "")
	if err == nil {
		t.Error("CNAME at lowercase name should conflict with existing A at uppercase name (DNS names are case-insensitive)")
	}
}

func seedRecord(r *mockDNSRecordRepo, id, zoneID, name, recType, content string) {
	r.Create(context.Background(), &models.DNSRecord{
		ID: id, ZoneID: zoneID, Name: name, Type: recType, Content: content,
	})
}
