package stalwartadmin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// The report types below are the parts of Stalwart's DmarcExternalReport,
// TlsExternalReport and ArfExternalReport objects the panel stores, as
// x:<Type>/get returns them. They were pinned against real reports on the
// .60 test box (2026-09-29) and `stalwart-cli describe`:
//
//   - A list inside a report (DMARC records, TLS policies and failure
//     details, DKIM/SPF results) comes back as an object keyed "0", "1", …
//     IndexedList reads that shape.
//   - Enums are camelCase: DMARC results pass|fail|unspecified, dispositions
//     none|pass|quarantine|reject|unspecified; TLS result types
//     startTlsNotSupported, certificateExpired, … other; ARF feedback types
//     abuse|authFailure|fraud|notSpam|virus|other.
//   - ARF addresses keep their angle brackets ("<a@example.com>").
//
// Everything in a report comes from whoever sent it, so the ingest treats it
// as untrusted input.

// DmarcExternalReport is a DMARC aggregate (RUA) report Stalwart received.
type DmarcExternalReport struct {
	ID         string      `json:"id"`
	From       string      `json:"from"`
	ReceivedAt time.Time   `json:"receivedAt"`
	Report     DmarcReport `json:"report"`
}

// DmarcReport is the RFC 7489 Appendix C report body.
type DmarcReport struct {
	OrgName        string                   `json:"orgName"`
	Email          string                   `json:"email"`
	ReportID       string                   `json:"reportId"`
	PolicyDomain   string                   `json:"policyDomain"`
	DateRangeBegin time.Time                `json:"dateRangeBegin"`
	DateRangeEnd   time.Time                `json:"dateRangeEnd"`
	Records        IndexedList[DmarcRecord] `json:"records"`
}

// DmarcRecord is one <record>: a source IP, how many messages it sent, and
// the policy evaluation for them.
type DmarcRecord struct {
	SourceIP             string `json:"sourceIp"`
	Count                uint64 `json:"count"`
	HeaderFrom           string `json:"headerFrom"`
	EvaluatedDisposition string `json:"evaluatedDisposition"`
	EvaluatedDkim        string `json:"evaluatedDkim"`
	EvaluatedSpf         string `json:"evaluatedSpf"`
}

// TlsExternalReport is an SMTP TLS report (RFC 8460) Stalwart received.
type TlsExternalReport struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	ReceivedAt time.Time `json:"receivedAt"`
	Report     TlsReport `json:"report"`
}

// TlsReport is the RFC 8460 report body.
type TlsReport struct {
	OrganizationName string                 `json:"organizationName"`
	ContactInfo      string                 `json:"contactInfo"`
	ReportID         string                 `json:"reportId"`
	DateRangeStart   time.Time              `json:"dateRangeStart"`
	DateRangeEnd     time.Time              `json:"dateRangeEnd"`
	Policies         IndexedList[TlsPolicy] `json:"policies"`
}

// TlsPolicy is one policy block: the domain, the session totals and the
// failure details.
type TlsPolicy struct {
	PolicyDomain            string                        `json:"policyDomain"`
	PolicyType              string                        `json:"policyType"`
	TotalSuccessfulSessions uint64                        `json:"totalSuccessfulSessions"`
	TotalFailedSessions     uint64                        `json:"totalFailedSessions"`
	FailureDetails          IndexedList[TlsFailureDetail] `json:"failureDetails"`
}

// TlsFailureDetail is one failure-details entry.
type TlsFailureDetail struct {
	ResultType          string `json:"resultType"`
	FailedSessionCount  uint64 `json:"failedSessionCount"`
	ReceivingMxHostname string `json:"receivingMxHostname"`
	SendingMtaIP        string `json:"sendingMtaIp"`
}

// ArfExternalReport is an ARF (RFC 5965) feedback report Stalwart received:
// a receiver telling the sender that one of its messages was reported.
type ArfExternalReport struct {
	ID         string            `json:"id"`
	From       string            `json:"from"`
	ReceivedAt time.Time         `json:"receivedAt"`
	Report     ArfFeedbackReport `json:"report"`
}

// ArfFeedbackReport is the RFC 5965 report body.
type ArfFeedbackReport struct {
	FeedbackType     string     `json:"feedbackType"`
	UserAgent        string     `json:"userAgent"`
	ReportingMta     string     `json:"reportingMta"`
	OriginalMailFrom string     `json:"originalMailFrom"`
	OriginalRcptTo   string     `json:"originalRcptTo"`
	SourceIP         string     `json:"sourceIp"`
	Incidents        uint64     `json:"incidents"`
	ArrivalDate      *time.Time `json:"arrivalDate"`
}

// IndexedList is a list Stalwart serializes as an object keyed by position
// ("0", "1", …). It also reads a plain JSON array, and null as empty.
type IndexedList[T any] []T

// UnmarshalJSON implements json.Unmarshaler.
func (l *IndexedList[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*l = nil
		return nil
	case len(b) > 0 && b[0] == '[':
		var items []T
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		*l = items
		return nil
	}
	var byKey map[string]T
	if err := json.Unmarshal(b, &byKey); err != nil {
		return err
	}
	type entry struct {
		pos  int
		item T
	}
	entries := make([]entry, 0, len(byKey))
	for k, v := range byKey {
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 {
			return fmt.Errorf("stalwartadmin: list key %q is not a position", k)
		}
		entries = append(entries, entry{n, v})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].pos < entries[j].pos })
	items := make([]T, 0, len(entries))
	for _, e := range entries {
		items = append(items, e.item)
	}
	*l = items
	return nil
}
