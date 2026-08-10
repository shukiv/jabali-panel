package hostedsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// NewCloudflareAPI returns a client bound to no particular zone — the
// customer-zone ACME DNS-01 path (JAB-235) resolves the zone id per domain
// via FindZoneID and passes it to the *TXT methods explicitly.
func NewCloudflareAPI(token string) *CloudflareDNS {
	return NewCloudflareDNS(token, "")
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
	return c.listRecordsIn(ctx, c.ZoneID, name, rtype)
}

func (c *CloudflareDNS) listRecordsIn(ctx context.Context, zoneID, name, rtype string) ([]cfRecord, error) {
	q := url.Values{"name": {name}, "type": {rtype}}
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?%s", zoneID, q.Encode()), nil)
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
	return c.SetChallengeTXT(ctx, c.ZoneID, "_acme-challenge."+FQDN(label), value)
}

// SetChallengeTXT adds a challenge value at an explicit record name in an
// explicit zone — the JAB-235 customer-zone path, where the name is a full
// customer FQDN (_acme-challenge.example.co.il) rather than a hostedsvc
// label. Same semantics as SetChallenge: add-only (a wildcard cert needs
// the apex and the wildcard challenge live at the SAME name at once, so a
// replace would clobber the first), duplicate-value idempotent, and capped —
// without the cap a token holder could script distinct values and
// accumulate thousands of TXT records, degrading the zone for everyone.
// maxChallengeRecordsPerLabel leaves headroom for a retry that raced
// cleanup.
func (c *CloudflareDNS) SetChallengeTXT(ctx context.Context, zoneID, name, value string) error {
	existing, err := c.listRecordsIn(ctx, zoneID, name, "TXT")
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Content == value {
			return nil // already present
		}
	}
	if len(existing) >= maxChallengeRecordsPerLabel {
		return fmt.Errorf("too many pending ACME challenges for %s (%d) — run cleanup first",
			name, len(existing))
	}
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID),
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
	return c.ClearChallengeTXT(ctx, c.ZoneID, "_acme-challenge."+FQDN(label))
}

// ClearChallengeTXT removes ALL challenge TXT records at an explicit record
// name in an explicit zone. Idempotent — clearing an absent name is a no-op.
func (c *CloudflareDNS) ClearChallengeTXT(ctx context.Context, zoneID, name string) error {
	recs, err := c.listRecordsIn(ctx, zoneID, name, "TXT")
	if err != nil {
		return err
	}
	for _, r := range recs {
		resp, derr := c.do(ctx, http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, r.ID), nil)
		if derr != nil {
			return fmt.Errorf("cf clear challenge: %w", derr)
		}
		resp.Body.Close()
	}
	return nil
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfZoneListResp struct {
	Success    bool    `json:"success"`
	Errors     []cfErr `json:"errors"`
	Result     []cfZone `json:"result"`
	ResultInfo struct {
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

// FindZoneID resolves the zone id for an EXACT zone name via the stored
// token. Returns "" (no error) when the token has no access to the zone —
// the caller surfaces that as "customer must grant access", not a failure.
func (c *CloudflareDNS) FindZoneID(ctx context.Context, name string) (string, error) {
	q := url.Values{"name": {name}, "status": {"active"}, "per_page": {"5"}}
	resp, err := c.do(ctx, http.MethodGet, "/zones?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("cf zone lookup: %w", err)
	}
	defer resp.Body.Close()
	var zr cfZoneListResp
	if err := json.NewDecoder(resp.Body).Decode(&zr); err != nil {
		return "", fmt.Errorf("cf zone lookup decode: %w", err)
	}
	if !zr.Success {
		return "", fmt.Errorf("cf zone lookup %s: %v", name, zr.Errors)
	}
	for _, z := range zr.Result {
		if strings.EqualFold(z.Name, name) {
			return z.ID, nil
		}
	}
	return "", nil
}

// VerifyToken checks the token against Cloudflare's verify endpoint and
// reports how many zones it can see — shown to the operator so "token
// saved" also says "and it covers N zones".
func (c *CloudflareDNS) VerifyToken(ctx context.Context) (zones int, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil)
	if err != nil {
		return 0, fmt.Errorf("cf token verify: %w", err)
	}
	var vr cfWriteResp
	verr := json.NewDecoder(resp.Body).Decode(&vr)
	resp.Body.Close()
	if verr != nil {
		return 0, fmt.Errorf("cf token verify decode: %w", verr)
	}
	if !vr.Success {
		return 0, fmt.Errorf("cloudflare rejected the token: %v", vr.Errors)
	}
	zresp, err := c.do(ctx, http.MethodGet, "/zones?per_page=1", nil)
	if err != nil {
		return 0, fmt.Errorf("cf zone count: %w", err)
	}
	defer zresp.Body.Close()
	var zr cfZoneListResp
	if err := json.NewDecoder(zresp.Body).Decode(&zr); err != nil {
		return 0, fmt.Errorf("cf zone count decode: %w", err)
	}
	if !zr.Success {
		return 0, fmt.Errorf("cf zone count: %v", zr.Errors)
	}
	return zr.ResultInfo.TotalCount, nil
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
