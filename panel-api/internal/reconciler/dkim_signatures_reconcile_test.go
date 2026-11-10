package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDkimEnsureAgent answers domain_email.dkim_ensure per domain.
type fakeDkimEnsureAgent struct {
	calls []string
	resp  map[string]string // domain -> raw JSON response
	err   map[string]error
}

func (f *fakeDkimEnsureAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "domain_email.dkim_ensure" {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	name, _ := params.(map[string]any)["domain_name"].(string)
	f.calls = append(f.calls, name)
	if e := f.err[name]; e != nil {
		return nil, e
	}
	return json.RawMessage(f.resp[name]), nil
}

type fakeDkimSigDomainRepo struct {
	repository.DomainRepository
	rows    []models.Domain
	updates int
}

func (f *fakeDkimSigDomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	out := append([]models.Domain(nil), f.rows...)
	return out, int64(len(out)), nil
}

func (f *fakeDkimSigDomainRepo) UpdateDKIMPublicKeys(_ context.Context, id string, ed, rsa *string) error {
	f.updates++
	for i := range f.rows {
		if f.rows[i].ID == id {
			if ed != nil {
				f.rows[i].DkimPublicKey = ed
			}
			if rsa != nil {
				f.rows[i].DkimRSAPublicKey = rsa
			}
			return nil
		}
	}
	return repository.ErrNotFound
}

func dkimMailDomain(id, name, edKey string) models.Domain {
	return models.Domain{
		ID: id, Name: name, OwnershipState: verifiedOwnership,
		EmailEnabled: true, MailProvider: models.MailProviderJabali,
		DkimSelector: strp("jabali"), DkimPublicKey: strp(edKey),
	}
}

func dkimSigReconciler(ag *fakeDkimEnsureAgent, doms *fakeDkimSigDomainRepo, recs *fakeDNSRecordRepo, zones *fakeDNSZoneRepo) *Reconciler {
	return &Reconciler{
		domains:        doms,
		dnsZones:       zones,
		dnsRecords:     recs,
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{}},
		agent:          ag,
		log:            slog.New(slog.DiscardHandler),
		dirty:          map[ResourceKey]struct{}{},
		wake:           make(chan struct{}, 1),
	}
}

func ensureResp(ed, rsa string, created ...string) string {
	b, _ := json.Marshal(map[string]any{
		"ok": true, "ed25519_public_key": ed, "rsa_public_key": rsa, "created": created,
	})
	return string(b)
}

func txtAt(recs *fakeDNSRecordRepo, zoneID, name string) []models.DNSRecord {
	var out []models.DNSRecord
	for _, r := range recs.records {
		if r.ZoneID == zoneID && r.Name == name && r.Type == "TXT" {
			out = append(out, *r)
		}
	}
	return out
}

var longRSA = "v=DKIM1; k=rsa; p=" + strings.Repeat("Q", 392)

// GH #2475: a domain set up before the RSA key gets it recorded and
// published (split into 255-character strings); its Ed25519 key and record
// stay as they are, even when the key file now says something else; the
// next tick makes no call.
func TestReconcileDKIMSignatures_BackfillsRSA(t *testing.T) {
	m6 := "m6"
	recs := &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{
		"ed": {ID: "ed", ZoneID: "z1", Name: "jabali._domainkey", Type: "TXT", Content: `"v=DKIM1; k=ed25519; p=OLD"`, ManagedBy: &m6, Managed: true},
	}}
	zones := &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{"z1": {ID: "z1", DomainID: "d1", Name: "example.com"}}}
	doms := &fakeDkimSigDomainRepo{rows: []models.Domain{dkimMailDomain("d1", "example.com", "v=DKIM1; k=ed25519; p=OLD")}}
	ag := &fakeDkimEnsureAgent{resp: map[string]string{
		// The key file differs from what is published, but the Ed25519
		// signature existed: that key must not be taken.
		"example.com": ensureResp("v=DKIM1; k=ed25519; p=FILE", longRSA, "Dkim1RsaSha256"),
	}}
	r := dkimSigReconciler(ag, doms, recs, zones)

	r.reconcileDKIMSignatures(context.Background())

	if got := stringOrEmpty(doms.rows[0].DkimRSAPublicKey); got != longRSA {
		t.Fatalf("stored RSA key = %q", got)
	}
	if got := *doms.rows[0].DkimPublicKey; got != "v=DKIM1; k=ed25519; p=OLD" {
		t.Fatalf("Ed25519 key replaced with %q under an existing signature", got)
	}
	rsaRecs := txtAt(recs, "z1", "jabalirsa._domainkey")
	if len(rsaRecs) != 1 {
		t.Fatalf("jabalirsa records = %v", rsaRecs)
	}
	want := `"` + longRSA[:255] + `" "` + longRSA[255:] + `"`
	if rsaRecs[0].Content != want || rsaRecs[0].ManagedBy == nil || *rsaRecs[0].ManagedBy != "m6" {
		t.Fatalf("jabalirsa record = %+v", rsaRecs[0])
	}
	if recs.records["ed"].Content != `"v=DKIM1; k=ed25519; p=OLD"` {
		t.Fatalf("Ed25519 record changed: %q", recs.records["ed"].Content)
	}
	if _, dirty := r.dirty[ResourceKey{Kind: KindDomain, ID: "d1"}]; !dirty {
		t.Error("the zone was not scheduled for a push")
	}

	r.reconcileDKIMSignatures(context.Background())
	if len(ag.calls) != 1 {
		t.Fatalf("calls = %v, want one (steady state is call-free)", ag.calls)
	}
}

// A domain whose Stalwart entry had no signature gets a new Ed25519 key:
// it is recorded and replaces the stale record's content. A stale m6 RSA
// record is updated in place; an operator-edited one is left alone.
func TestReconcileDKIMSignatures_RecreatedKeysReplaceStaleRecords(t *testing.T) {
	m6 := "m6"
	recs := &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{
		"ed":   {ID: "ed", ZoneID: "z1", Name: "jabali._domainkey", Type: "TXT", Content: `"v=DKIM1; k=ed25519; p=OLD"`, ManagedBy: &m6},
		"rsa":  {ID: "rsa", ZoneID: "z1", Name: "jabalirsa._domainkey", Type: "TXT", Content: `"stale"`, ManagedBy: &m6},
		"user": {ID: "user", ZoneID: "z2", Name: "jabalirsa._domainkey", Type: "TXT", Content: `"mine"`},
	}}
	zones := &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{
		"z1": {ID: "z1", DomainID: "d1", Name: "a.example.com"},
		"z2": {ID: "z2", DomainID: "d2", Name: "b.example.com"},
	}}
	doms := &fakeDkimSigDomainRepo{rows: []models.Domain{
		dkimMailDomain("d1", "a.example.com", "v=DKIM1; k=ed25519; p=OLD"),
		dkimMailDomain("d2", "b.example.com", "v=DKIM1; k=ed25519; p=B"),
	}}
	ag := &fakeDkimEnsureAgent{resp: map[string]string{
		"a.example.com": ensureResp("v=DKIM1; k=ed25519; p=NEW", "v=DKIM1; k=rsa; p=A", "Dkim1Ed25519Sha256", "Dkim1RsaSha256"),
		"b.example.com": ensureResp("v=DKIM1; k=ed25519; p=B", "v=DKIM1; k=rsa; p=B"),
	}}
	r := dkimSigReconciler(ag, doms, recs, zones)

	r.reconcileDKIMSignatures(context.Background())

	if got := *doms.rows[0].DkimPublicKey; got != "v=DKIM1; k=ed25519; p=NEW" {
		t.Errorf("recreated Ed25519 key not stored: %q", got)
	}
	if got := recs.records["ed"].Content; got != `"v=DKIM1; k=ed25519; p=NEW"` {
		t.Errorf("Ed25519 record = %q", got)
	}
	if got := recs.records["rsa"].Content; got != `"v=DKIM1; k=rsa; p=A"` {
		t.Errorf("stale m6 RSA record = %q, want updated in place", got)
	}
	if got := recs.records["user"].Content; got != `"mine"` {
		t.Errorf("operator-edited record changed to %q", got)
	}
	if n := len(txtAt(recs, "z2", "jabalirsa._domainkey")); n != 1 {
		t.Errorf("records at b's jabalirsa = %d, want the operator's one only", n)
	}
	// b's key is still stored: only DNS defers to the operator.
	if got := stringOrEmpty(doms.rows[1].DkimRSAPublicKey); got != "v=DKIM1; k=rsa; p=B" {
		t.Errorf("b RSA key = %q", got)
	}
}

// Domains whose mail is not hosted here, not set up, unproven or on a
// reserved name are never asked about.
func TestReconcileDKIMSignatures_SkipsNonMailDomains(t *testing.T) {
	off := dkimMailDomain("d1", "off.example.com", "k")
	off.EmailEnabled = false
	elsewhere := dkimMailDomain("d2", "google.example.com", "k")
	elsewhere.MailProvider = "google"
	unset := dkimMailDomain("d3", "new.example.com", "k")
	unset.DkimSelector = nil
	unproven := dkimMailDomain("d4", "unproven.example.com", "k")
	unproven.OwnershipState = models.OwnershipState{}
	reserved := dkimMailDomain("d5", "box.local", "k")
	doms := &fakeDkimSigDomainRepo{rows: []models.Domain{off, elsewhere, unset, unproven, reserved}}
	ag := &fakeDkimEnsureAgent{}
	r := dkimSigReconciler(ag, doms, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}}, &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}})

	r.reconcileDKIMSignatures(context.Background())
	if len(ag.calls) != 0 {
		t.Fatalf("calls = %v, want none", ag.calls)
	}
}

// A failed call is retried next tick; a domain the mail server doesn't know
// is reported once, not every tick.
func TestReconcileDKIMSignatures_RetryAndNotFound(t *testing.T) {
	doms := &fakeDkimSigDomainRepo{rows: []models.Domain{
		dkimMailDomain("d1", "flaky.example.com", "k"),
		dkimMailDomain("d2", "gone.example.com", "k"),
	}}
	ag := &fakeDkimEnsureAgent{err: map[string]error{
		"flaky.example.com": fmt.Errorf("timeout"),
		"gone.example.com":  &agent.AgentError{Code: agent.CodeNotFound, Message: "no registry entry"},
	}}
	r := dkimSigReconciler(ag, doms, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}}, &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}})

	r.reconcileDKIMSignatures(context.Background())
	r.reconcileDKIMSignatures(context.Background())
	if got := strings.Join(ag.calls, ","); got != "flaky.example.com,gone.example.com,flaky.example.com" {
		t.Fatalf("calls = %s", got)
	}
	if doms.updates != 0 {
		t.Fatalf("keys written after failed calls: %d", doms.updates)
	}
}

// An agent from before #2475 doesn't know the verb: the pass stops quietly
// for this tick and tries again on the next.
func TestReconcileDKIMSignatures_OldAgentRetriesQuietly(t *testing.T) {
	doms := &fakeDkimSigDomainRepo{rows: []models.Domain{
		dkimMailDomain("d1", "a.example.com", "k"),
		dkimMailDomain("d2", "b.example.com", "k"),
	}}
	unknown := &agent.AgentError{Code: agent.CodeUnknownCommand, Message: "unknown command"}
	ag := &fakeDkimEnsureAgent{err: map[string]error{"a.example.com": unknown, "b.example.com": unknown}}
	r := dkimSigReconciler(ag, doms, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}}, &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}})

	r.reconcileDKIMSignatures(context.Background())
	r.reconcileDKIMSignatures(context.Background())
	if got := strings.Join(ag.calls, ","); got != "a.example.com,a.example.com" {
		t.Fatalf("calls = %s, want one per tick", got)
	}
}
