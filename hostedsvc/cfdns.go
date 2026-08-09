package hostedsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// CloudflareDNS implements DNSBackend against the Cloudflare DNS API (v4).
// Chosen over self-hosted PowerDNS for anycast HA (a single DNS box is the
// blueprint's stated failure-domain risk). Every record is written
// proxied=false (DNS-only): a label's A record must resolve to the CUSTOMER's
// real box, never a Cloudflare edge — proxying it would break the customer's
// panel/mail ports and hide the very IP the label encodes. ACME TXT records
// are DNS-only by nature.
type CloudflareDNS struct {
	Token  string // API token scoped to Zone:DNS:Edit on this zone only
	ZoneID string
	HTTP   *http.Client
	api    string // override for tests; defaults to the CF API root
}

func NewCloudflareDNS(token, zoneID string) *CloudflareDNS {
	return &CloudflareDNS{
		Token:  token,
		ZoneID: zoneID,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
		api:    "https://api.cloudflare.com/client/v4",
	}
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

type cfListResp struct {
	Success bool       `json:"success"`
	Errors  []cfErr    `json:"errors"`
	Result  []cfRecord `json:"result"`
}

type cfWriteResp struct {
	Success bool    `json:"success"`
	Errors  []cfErr `json:"errors"`
}

type cfErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *CloudflareDNS) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

// listRecords returns every record for name+type (a name can hold multiple
// TXT records — needed for the dual-value wildcard challenge).
func (c *CloudflareDNS) listRecords(ctx context.Context, name, rtype string) ([]cfRecord, error) {
	q := url.Values{"name": {name}, "type": {rtype}}
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?%s", c.ZoneID, q.Encode()), nil)
	if err != nil {
		return nil, fmt.Errorf("cf list: %w", err)
	}
	defer resp.Body.Close()
	var lr cfListResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("cf list decode: %w", err)
	}
	if !lr.Success {
		return nil, fmt.Errorf("cf list: %v", lr.Errors)
	}
	return lr.Result, nil
}

// findID returns the record id for name+type, or "" when absent.
func (c *CloudflareDNS) findID(ctx context.Context, name, rtype string) (string, error) {
	recs, err := c.listRecords(ctx, name, rtype)
	if err != nil || len(recs) == 0 {
		return "", err
	}
	return recs[0].ID, nil
}

// upsert creates or replaces the single record for name+type.
func (c *CloudflareDNS) upsert(ctx context.Context, rec cfRecord) error {
	id, err := c.findID(ctx, rec.Name, rec.Type)
	if err != nil {
		return err
	}
	method, path := http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", c.ZoneID)
	if id != "" {
		method, path = http.MethodPut, fmt.Sprintf("/zones/%s/dns_records/%s", c.ZoneID, id)
	}
	resp, err := c.do(ctx, method, path, rec)
	if err != nil {
		return fmt.Errorf("cf write: %w", err)
	}
	defer resp.Body.Close()
	var wr cfWriteResp
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return fmt.Errorf("cf write decode: %w", err)
	}
	if !wr.Success {
		return fmt.Errorf("cf write %s %s: %v", rec.Type, rec.Name, wr.Errors)
	}
	return nil
}

func (c *CloudflareDNS) deleteRecord(ctx context.Context, name, rtype string) error {
	id, err := c.findID(ctx, name, rtype)
	if err != nil {
		return err
	}
	if id == "" {
		return nil // already gone — delete is idempotent
	}
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", c.ZoneID, id), nil)
	if err != nil {
		return fmt.Errorf("cf delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("cf delete %s: HTTP %d", name, resp.StatusCode)
	}
	return nil
}

func (c *CloudflareDNS) EnsureA(ctx context.Context, label, ipv4 string) error {
	return c.upsert(ctx, cfRecord{Type: "A", Name: FQDN(label), Content: ipv4, TTL: 300, Proxied: false})
}

func (c *CloudflareDNS) EnsureWildcardA(ctx context.Context, label, ipv4 string) error {
	return c.upsert(ctx, cfRecord{Type: "A", Name: "*." + FQDN(label), Content: ipv4, TTL: 300, Proxied: false})
}

// SetChallenge ADDS a challenge value (does not replace). A wildcard+apex
// certificate produces TWO challenge values at the SAME
// _acme-challenge.<label> name — Let's Encrypt requires both present at once —
// so a replace would clobber the first. On CF, multiple values = multiple TXT
// records at one name; we create one per value, skipping an exact duplicate.
func (c *CloudflareDNS) SetChallenge(ctx context.Context, label, value string) error {
	name := "_acme-challenge." + FQDN(label)
	existing, err := c.listRecords(ctx, name, "TXT")
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Content == value {
			return nil // already present
		}
	}
	// SetChallenge is add-only by design (a wildcard cert needs the apex and
	// the wildcard challenge live at the SAME name simultaneously), so without
	// a cap a token holder could script distinct values and accumulate
	// thousands of TXT records in the shared jabalihosted.com zone, degrading
	// zone size and list performance for every other tenant. Two is the
	// legitimate maximum; maxChallengeRecordsPerLabel leaves headroom for a
	// retry that raced cleanup.
	if len(existing) >= maxChallengeRecordsPerLabel {
		return fmt.Errorf("too many pending ACME challenges for %s (%d) — run cleanup first",
			name, len(existing))
	}
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", c.ZoneID),
		cfRecord{Type: "TXT", Name: name, Content: value, TTL: 60, Proxied: false})
	if err != nil {
		return fmt.Errorf("cf add challenge: %w", err)
	}
	defer resp.Body.Close()
	var wr cfWriteResp
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return fmt.Errorf("cf add challenge decode: %w", err)
	}
	if !wr.Success {
		return fmt.Errorf("cf add challenge %s: %v", name, wr.Errors)
	}
	return nil
}

// ClearChallenge removes ALL challenge TXT records at the label's name.
func (c *CloudflareDNS) ClearChallenge(ctx context.Context, label string) error {
	name := "_acme-challenge." + FQDN(label)
	recs, err := c.listRecords(ctx, name, "TXT")
	if err != nil {
		return err
	}
	for _, r := range recs {
		resp, derr := c.do(ctx, http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", c.ZoneID, r.ID), nil)
		if derr != nil {
			return fmt.Errorf("cf clear challenge: %w", derr)
		}
		resp.Body.Close()
	}
	return nil
}

func (c *CloudflareDNS) RemoveLabel(ctx context.Context, label string) error {
	if err := c.deleteRecord(ctx, FQDN(label), "A"); err != nil {
		return err
	}
	if err := c.deleteRecord(ctx, "*."+FQDN(label), "A"); err != nil {
		return err
	}
	return c.ClearChallenge(ctx, label) // removes ALL challenge TXTs
}
