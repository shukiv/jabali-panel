// Package dmarcrua decodes a DMARC RUA aggregate report (RFC 7489
// Appendix C). Pure function — no I/O, no DB — so the Wave-6 ingest
// source (and any future re-ingest tool) can hand it raw bytes
// (XML, gzip'd XML, or zip'd XML — the three transports operators
// see in the wild) and get back a slice ready for repository.
// InsertMany.
//
// Report rows fan out: one DB row per `<record>` × per
// `<auth_results>/<dkim>` x `<auth_results>/<spf>` pair-evaluated
// disposition. v1 explodes by `<record>`/`<source_ip>`/`<disposition>`
// (RFC's per-source bucket) — finer-grain pivots are a follow-up.
package dmarcrua

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Report is the decoded RUA aggregate. Reporter + domain + window pin the
// idempotency tuple the repo's ExistsForReport gates on.
type Report struct {
	Reporter    string
	Domain      string
	WindowStart time.Time
	WindowEnd   time.Time
	Rows        []models.DMARCAggregate // ready to InsertMany
}

// xmlFeedback mirrors the RFC 7489 Appendix-C schema. Only the fields
// jabali persists are decoded — others (auth_results/dkim domain,
// policy_published flags, etc.) are ignored for v1 to keep the wire
// surface small.
type xmlFeedback struct {
	XMLName        xml.Name `xml:"feedback"`
	ReportMetadata struct {
		OrgName   string `xml:"org_name"`
		DateRange struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	PolicyPublished struct {
		Domain string `xml:"domain"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP        string `xml:"source_ip"`
			Count           uint   `xml:"count"`
			PolicyEvaluated struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
	} `xml:"record"`
}

// Parse decodes one RUA report from raw bytes. Accepts:
//   - plain XML (`<?xml ...><feedback>...`)
//   - gzip'd XML (`.xml.gz` — the most common transport)
//   - ZIP archive containing a single XML entry (some senders, esp.
//     Outlook/proofpoint, ship .zip)
//
// Always returns a Report (possibly with zero Rows) or an error. The
// caller must gate the InsertMany on ExistsForReport(reporter, domain,
// windowStart, windowEnd) so re-delivery doesn't duplicate.
func Parse(raw []byte) (*Report, error) {
	xmlBytes, err := decompressIfNeeded(raw)
	if err != nil {
		return nil, fmt.Errorf("dmarcrua: decompress: %w", err)
	}
	var fb xmlFeedback
	if err := xml.Unmarshal(xmlBytes, &fb); err != nil {
		return nil, fmt.Errorf("dmarcrua: xml decode: %w", err)
	}
	if fb.ReportMetadata.OrgName == "" || fb.PolicyPublished.Domain == "" {
		return nil, fmt.Errorf("dmarcrua: missing report_metadata.org_name or policy_published.domain")
	}
	ws := time.Unix(fb.ReportMetadata.DateRange.Begin, 0).UTC()
	we := time.Unix(fb.ReportMetadata.DateRange.End, 0).UTC()
	r := &Report{
		Reporter:    fb.ReportMetadata.OrgName,
		Domain:      fb.PolicyPublished.Domain,
		WindowStart: ws,
		WindowEnd:   we,
		Rows:        make([]models.DMARCAggregate, 0, len(fb.Records)),
	}
	for _, rec := range fb.Records {
		r.Rows = append(r.Rows, models.DMARCAggregate{
			Domain:      fb.PolicyPublished.Domain,
			Reporter:    fb.ReportMetadata.OrgName,
			WindowStart: ws,
			WindowEnd:   we,
			SourceIP:    rec.Row.SourceIP,
			Disposition: defaultIfEmpty(rec.Row.PolicyEvaluated.Disposition, "none"),
			DKIM:        defaultIfEmpty(rec.Row.PolicyEvaluated.DKIM, "fail"),
			SPF:         defaultIfEmpty(rec.Row.PolicyEvaluated.SPF, "fail"),
			Cnt:         rec.Row.Count,
		})
	}
	return r, nil
}

// decompressIfNeeded peeks the first bytes to pick a decoder. gzip and
// zip have distinct magic; everything else is treated as raw XML.
func decompressIfNeeded(raw []byte) ([]byte, error) {
	switch {
	case len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b: // gzip
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)
	case len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K' && raw[2] == 0x03 && raw[3] == 0x04: // zip
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			// First .xml entry wins. RUA zips contain exactly one.
			if len(f.Name) >= 4 && f.Name[len(f.Name)-4:] == ".xml" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("zip has no .xml entry")
	default:
		return raw, nil
	}
}

func defaultIfEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// unused helper kept here so other unit tests can re-encode timestamps
// without leaking strconv as a transitive import surface.
var _ = strconv.Atoi
