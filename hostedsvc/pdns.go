package hostedsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DNSBackend is what the API handlers need from DNS. The production
// implementation talks to PowerDNS's HTTP API on loopback; tests use a fake.
type DNSBackend interface {
	// EnsureA upserts `<label>.<base> A ip`.
	EnsureA(ctx context.Context, label, ipv4 string) error
	// EnsureWildcardA upserts `*.<label>.<base> A ip`. Written alongside the
	// apex A at claim time so `mail.<label>`, `autoconfig.<label>`, and every
	// preview subdomain resolve to the box — which lets the panel's EXISTING
	// HTTP-01 cert machinery issue for those names with no DNS-01 broker in
	// v1 (JAB-213 phase 3). A wildcard *certificate* still needs DNS-01;
	// that's phase 3b, using SetChallenge below.
	EnsureWildcardA(ctx context.Context, label, ipv4 string) error
	// SetChallenge upserts `_acme-challenge.<label>.<base> TXT value`.
	SetChallenge(ctx context.Context, label, value string) error
	// ClearChallenge removes the challenge RRset for the label.
	ClearChallenge(ctx context.Context, label string) error
	// RemoveLabel deletes the label's A, wildcard A, and any challenge.
	RemoveLabel(ctx context.Context, label string) error
}

// PDNS implements DNSBackend against the PowerDNS authoritative HTTP API.
// Records are written through the API only — never the backend database
// directly — and every write is followed by the API's implicit cache
// invalidation (the jabali "pdns cache after backend write" scar is why this
// client exists at all).
type PDNS struct {
	// URL is the API root, e.g. http://127.0.0.1:8081.
	URL    string
	APIKey string
	Zone   string // "jabalihosted.com."
	HTTP   *http.Client
}

func NewPDNS(url, apiKey string) *PDNS {
	return &PDNS{URL: url, APIKey: apiKey, Zone: BaseDomain + ".",
		HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type rrset struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	TTL        int      `json:"ttl,omitempty"`
	ChangeType string   `json:"changetype"`
	Records    []record `json:"records,omitempty"`
}

type record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

func (p *PDNS) patch(ctx context.Context, sets []rrset) error {
	body, err := json.Marshal(map[string]any{"rrsets": sets})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v1/servers/localhost/zones/%s", p.URL, p.Zone)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("pdns patch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("pdns patch: HTTP %d: %s", resp.StatusCode, buf.String())
	}
	return nil
}

func (p *PDNS) EnsureA(ctx context.Context, label, ipv4 string) error {
	return p.patch(ctx, []rrset{{
		Name: FQDN(label) + ".", Type: "A", TTL: 300, ChangeType: "REPLACE",
		Records: []record{{Content: ipv4}},
	}})
}

func (p *PDNS) EnsureWildcardA(ctx context.Context, label, ipv4 string) error {
	return p.patch(ctx, []rrset{{
		Name: "*." + FQDN(label) + ".", Type: "A", TTL: 300, ChangeType: "REPLACE",
		Records: []record{{Content: ipv4}},
	}})
}

// SetChallenge ADDS a value to the challenge rrset. A wildcard+apex cert needs
// two values live at once at the same name, so we merge with any existing
// records (a PowerDNS rrset holds multiple records) rather than replacing.
func (p *PDNS) SetChallenge(ctx context.Context, label, value string) error {
	name := "_acme-challenge." + FQDN(label) + "."
	quoted := fmt.Sprintf("%q", value)
	existing, err := p.getRRSet(ctx, name, "TXT")
	if err != nil {
		return err
	}
	// Same cap as the Cloudflare backend: add-only challenges must not let a
	// token holder accumulate unbounded TXT records in the shared zone. Two is
	// the legitimate maximum (apex + wildcard); the constant leaves headroom
	// for a retry that raced cleanup.
	if len(existing) >= maxChallengeRecordsPerLabel {
		return fmt.Errorf("too many pending ACME challenges for %s (%d) — run cleanup first",
			name, len(existing))
	}
	recs := []record{{Content: quoted}}
	for _, c := range existing {
		if c != quoted {
			recs = append(recs, record{Content: c})
		}
	}
	return p.patch(ctx, []rrset{{
		Name: name, Type: "TXT", TTL: 60, ChangeType: "REPLACE", Records: recs,
	}})
}

func (p *PDNS) ClearChallenge(ctx context.Context, label string) error {
	return p.patch(ctx, []rrset{{
		Name: "_acme-challenge." + FQDN(label) + ".", Type: "TXT", ChangeType: "DELETE",
	}})
}

// getRRSet returns the record contents currently in an rrset (empty if none).
func (p *PDNS) getRRSet(ctx context.Context, name, rtype string) ([]string, error) {
	url := fmt.Sprintf("%s/api/v1/servers/localhost/zones/%s", p.URL, p.Zone)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", p.APIKey)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdns get zone: %w", err)
	}
	defer resp.Body.Close()
	var z struct {
		RRSets []struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Records []record `json:"records"`
		} `json:"rrsets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&z); err != nil {
		return nil, fmt.Errorf("pdns get zone decode: %w", err)
	}
	var out []string
	for _, rr := range z.RRSets {
		if rr.Name == name && rr.Type == rtype {
			for _, r := range rr.Records {
				out = append(out, r.Content)
			}
		}
	}
	return out, nil
}

func (p *PDNS) RemoveLabel(ctx context.Context, label string) error {
	return p.patch(ctx, []rrset{
		{Name: FQDN(label) + ".", Type: "A", ChangeType: "DELETE"},
		{Name: "*." + FQDN(label) + ".", Type: "A", ChangeType: "DELETE"},
		{Name: "_acme-challenge." + FQDN(label) + ".", Type: "TXT", ChangeType: "DELETE"},
	})
}
