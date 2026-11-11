package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// sigSummary lists domainID's signatures as sorted "type/selector" pairs.
func sigSummary(reg *fakeDkim2Registry, domainID string) string {
	var out []string
	for _, s := range reg.sigs {
		if s["domainId"] == domainID {
			out = append(out, s["@type"].(string)+"/"+s["selector"].(string))
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// sigOfType returns domainID's signature of type typ, or nil.
func sigOfType(reg *fakeDkim2Registry, domainID, typ string) map[string]any {
	for _, s := range reg.sigs {
		if s["domainId"] == domainID && s["@type"] == typ {
			return s
		}
	}
	return nil
}

func wireEnsureServer(t *testing.T, reg *fakeDkim2Registry, domainIDs ...string) {
	t.Helper()
	routes := reg.routes()
	routes["x:Domain/query"] = jmapHandlerReturning(jmapQueryResult{IDs: domainIDs, Total: uint64(len(domainIDs))})
	srv := newJMAPServer(t, routes)
	t.Cleanup(srv.Close)
	wireJMAP(t, srv)
}

func callDkimEnsure(t *testing.T, domain string) (dkimEnsureResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(dkimEnsureParams{DomainName: domain})
	out, err := dkimEnsureHandler(context.Background(), raw)
	if err != nil {
		return dkimEnsureResponse{}, err
	}
	return out.(dkimEnsureResponse), nil
}

// A domain with no signature gets both; the RSA signature carries the key
// that was written to disk, and the published TXT is that key's.
func TestDkimEnsure_NoSignatures_CreatesBoth(t *testing.T) {
	dir := wireDKIMDir(t)
	reg := &fakeDkim2Registry{}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resp.Created, ","); got != "Dkim1Ed25519Sha256,Dkim1RsaSha256" {
		t.Errorf("created = %q", got)
	}
	if got := sigSummary(reg, "dom1"); got != "Dkim1Ed25519Sha256/jabali Dkim1RsaSha256/jabalirsa" {
		t.Fatalf("signatures = %q", got)
	}

	rsaPath := filepath.Join(dir, "example.com.rsa.key")
	pemKey, err := dkim.LoadRSA(rsaPath)
	if err != nil {
		t.Fatalf("RSA key file: %v", err)
	}
	if fi, _ := os.Stat(rsaPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("RSA key mode = %v, want 0600", fi.Mode().Perm())
	}
	pk, _ := sigOfType(reg, "dom1", "Dkim1RsaSha256")["privateKey"].(map[string]any)
	if pk["secret"] != string(pemKey) {
		t.Errorf("the RSA signature's key is not the key on disk: %v", pk)
	}
	want, _ := dkim.PublicRSADKIMTxtFromPEM(pemKey)
	if resp.RSAPublicKey != string(want) || resp.RSASelector != "jabalirsa" {
		t.Errorf("RSA TXT = %q, want %q", resp.RSAPublicKey, want)
	}
	if !strings.HasPrefix(resp.Ed25519PublicKey, "v=DKIM1; k=ed25519; p=") || resp.Ed25519Selector != "jabali" {
		t.Errorf("Ed25519 = %q %q", resp.Ed25519Selector, resp.Ed25519PublicKey)
	}
}

// A pre-#2475 domain has the Ed25519 signature only: it gets the RSA one,
// and its Ed25519 key is neither regenerated nor re-pushed.
func TestDkimEnsure_Ed25519Only_AddsRSAAndKeepsEd25519(t *testing.T) {
	dir := wireDKIMDir(t)
	edPath := filepath.Join(dir, "example.com.key")
	edTXT, err := ensureDKIMKey(edPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(edPath)
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		"old": {"id": "old", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali"},
	}}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resp.Created, ","); got != "Dkim1RsaSha256" {
		t.Errorf("created = %q, want the RSA signature only", got)
	}
	if _, ok := reg.sigs["old"]; !ok || len(reg.sigs) != 2 {
		t.Fatalf("signatures = %v", reg.sigs)
	}
	if after, _ := os.ReadFile(edPath); string(after) != string(before) {
		t.Error("the Ed25519 key file changed under an existing signature")
	}
	if resp.Ed25519PublicKey != edTXT {
		t.Errorf("Ed25519 TXT = %q, want the key file's %q", resp.Ed25519PublicKey, edTXT)
	}
}

// Both signatures present: nothing is created, an RSA key file that exists
// is read, not replaced, and a missing one is not generated (a new key
// would be published while the signature keeps signing with the old one).
func TestDkimEnsure_BothPresent_CreatesNothing(t *testing.T) {
	dir := wireDKIMDir(t)
	k, err := dkim.GenerateRSA()
	if err != nil {
		t.Fatal(err)
	}
	rsaPath := filepath.Join(dir, "example.com.rsa.key")
	if err := dkim.WritePrivate(rsaPath, k.PrivatePEM); err != nil {
		t.Fatal(err)
	}
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		"a": {"id": "a", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali"},
		"b": {"id": "b", "@type": "Dkim1RsaSha256", "domainId": "dom1", "selector": "jabalirsa"},
		// Another domain's signatures don't count.
		"c": {"id": "c", "@type": "Dkim1RsaSha256", "domainId": "dom2", "selector": "jabalirsa"},
	}}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Created) != 0 || reg.nextID != 0 {
		t.Fatalf("created %v on a domain with both signatures", resp.Created)
	}
	if resp.RSAPublicKey != string(k.PublicDKIMTxt) {
		t.Errorf("RSA TXT = %q, want the key file's", resp.RSAPublicKey)
	}
	// The Ed25519 key file is missing: reported unknown, not generated.
	if resp.Ed25519PublicKey != "" {
		t.Errorf("Ed25519 TXT = %q, want \"\" for a signature whose key file is missing", resp.Ed25519PublicKey)
	}
	if _, err := os.Stat(filepath.Join(dir, "example.com.key")); !os.IsNotExist(err) {
		t.Error("an Ed25519 key was generated under an existing signature")
	}
}

func TestDkimEnsure_UnknownDomainIsNotFound(t *testing.T) {
	wireDKIMDir(t)
	wireEnsureServer(t, &fakeDkim2Registry{})
	_, err := callDkimEnsure(t, "example.com")
	requireAgentErrorCode(t, err, agentwire.CodeNotFound)
}

func TestDkimEnsure_RejectsBadDomainName(t *testing.T) {
	wireDKIMDir(t)
	wireEnsureServer(t, &fakeDkim2Registry{}, "dom1")
	for _, name := range []string{"", "../etc", "a b.com", "Example.COM/x"} {
		_, err := callDkimEnsure(t, name)
		requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	}
}

// The panel's side of the domain.email_enable, domain_email.dkim_ensure and
// domain.email_dkim_rotate contracts (GH #2475): the fixtures the panel
// decodes decode into the handlers' shapes with every field kept.
func TestDkimEnsure_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"domain_email_enable_response.json", &domainEmailEnableResponse{}},
		{"domain_email_dkim_ensure_request.json", &dkimEnsureParams{}},
		{"domain_email_dkim_ensure_response.json", &dkimEnsureResponse{}},
		{"domain_email_dkim_rotate_request.json", &domainEmailDKIMRotateParams{}},
		{"domain_email_dkim_rotate_response.json", &domainEmailDKIMRotateResponse{}},
	} {
		raw, err := os.ReadFile(dir + c.file)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.into); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		again, _ := json.Marshal(c.into)
		var got, want any
		_ = json.Unmarshal(again, &got)
		_ = json.Unmarshal(raw, &want)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Errorf("%s: the agent drops or renames fields:\nwant %s\ngot  %s", c.file, wb, gb)
		}
	}
}

// healFixture is a domain rotated before Rotate DKIM updated Stalwart: the
// key files and the published keys agree, Stalwart signs with older keys.
type healFixture struct {
	edTXT, rsaTXT, edPEM, rsaPEM string
	reg                          *fakeDkim2Registry
}

func newHealFixture(t *testing.T, edStale, rsaStale bool) *healFixture {
	t.Helper()
	dir := wireDKIMDir(t)
	f := &healFixture{}
	var err error
	if f.edTXT, err = ensureDKIMKey(filepath.Join(dir, "example.com.key")); err != nil {
		t.Fatal(err)
	}
	seed, _ := dkim.LoadEd25519(filepath.Join(dir, "example.com.key"))
	edPEM, _ := dkim.SeedToPKCS8PEM(seed)
	f.edPEM = string(edPEM)
	rsaPEM, rsaTXT, err := ensureDKIMRSAKey(filepath.Join(dir, "example.com.rsa.key"))
	if err != nil {
		t.Fatal(err)
	}
	f.rsaPEM, f.rsaTXT = string(rsaPEM), rsaTXT
	sig := func(id, typ, sel, pem, pub string) map[string]any {
		return map[string]any{"id": id, "@type": typ, "domainId": "dom1", "selector": sel,
			"privateKey": map[string]any{"@type": "Text", "secret": pem}, "publicKey": pub}
	}
	edPub, edKey := dkimTXTKey(f.edTXT), f.edPEM
	if edStale {
		edPub, edKey = "c3RhbGVlZA==", "stale-ed-key"
	}
	rsaPub, rsaKey := dkimTXTKey(f.rsaTXT), f.rsaPEM
	if rsaStale {
		rsaPub, rsaKey = "c3RhbGVyc2E=", "stale-rsa-key"
	}
	f.reg = &fakeDkim2Registry{sigs: map[string]map[string]any{
		"ed1":  sig("ed1", "Dkim1Ed25519Sha256", "jabali", edKey, edPub),
		"d2":   sig("d2", "Dkim2Ed25519Sha256", "jabali", edKey, edPub),
		"rsa1": sig("rsa1", "Dkim1RsaSha256", "jabalirsa", rsaKey, rsaPub),
	}}
	wireEnsureServer(t, f.reg, "dom1")
	return f
}

func (f *healFixture) secret(id string) string {
	pk, _ := f.reg.sigs[id]["privateKey"].(map[string]any)
	s, _ := pk["secret"].(string)
	return s
}

func callDkimEnsureWith(t *testing.T, p dkimEnsureParams) (dkimEnsureResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	out, err := dkimEnsureHandler(context.Background(), raw)
	if err != nil {
		return dkimEnsureResponse{}, err
	}
	return out.(dkimEnsureResponse), nil
}

// A rotation before the fix changed the key file and the published record
// but not Stalwart's signature, so every signature failed. When the panel
// publishes the key file's key and Stalwart signs with another, Stalwart
// gets the file's key: that can only make the published record verify.
func TestDkimEnsure_HealsSignaturesThatDifferFromThePublishedKey(t *testing.T) {
	f := newHealFixture(t, true, true)

	resp, err := callDkimEnsureWith(t, dkimEnsureParams{
		DomainName: "example.com", PublishedEd25519PublicKey: f.edTXT, PublishedRSAPublicKey: f.rsaTXT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.secret("ed1") != f.edPEM || f.secret("d2") != f.edPEM {
		t.Error("the Ed25519 signatures still sign with a key nothing publishes")
	}
	if f.secret("rsa1") != f.rsaPEM {
		t.Error("the RSA signature still signs with a key nothing publishes")
	}
	healed := append([]string(nil), resp.Healed...)
	sort.Strings(healed)
	if got := strings.Join(healed, ","); got != "Dkim1Ed25519Sha256,Dkim1RsaSha256,Dkim2Ed25519Sha256" {
		t.Errorf("healed = %q", got)
	}
	if len(resp.Created) != 0 {
		t.Errorf("created = %v", resp.Created)
	}
}

// Nothing is healed unless the published key is the key file's and Stalwart
// reports a different key: a signature is never given a key nobody
// publishes, and a matching or unknown key is left alone.
func TestDkimEnsure_HealOnlyUnderTheGuard(t *testing.T) {
	cases := map[string]func(f *healFixture) dkimEnsureParams{
		"no published key (mail enable, older panel)": func(*healFixture) dkimEnsureParams {
			return dkimEnsureParams{DomainName: "example.com"}
		},
		"published key is not the file's": func(f *healFixture) dkimEnsureParams {
			return dkimEnsureParams{DomainName: "example.com",
				PublishedEd25519PublicKey: "v=DKIM1; k=ed25519; p=b3RoZXI=", PublishedRSAPublicKey: "v=DKIM1; k=rsa; p=b3RoZXI="}
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			f := newHealFixture(t, true, true)
			resp, err := callDkimEnsureWith(t, params(f))
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"ed1", "d2", "rsa1"} {
				if strings.HasPrefix(f.secret(id), "-----BEGIN") {
					t.Errorf("signature %s was given the key file's key", id)
				}
			}
			if len(resp.Healed) != 0 {
				t.Errorf("healed = %v", resp.Healed)
			}
		})
	}
	t.Run("stalwart key unknown", func(t *testing.T) {
		f := newHealFixture(t, true, true)
		for _, s := range f.reg.sigs {
			delete(s, "publicKey")
		}
		resp, err := callDkimEnsureWith(t, dkimEnsureParams{
			DomainName: "example.com", PublishedEd25519PublicKey: f.edTXT, PublishedRSAPublicKey: f.rsaTXT,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Healed) != 0 || f.secret("ed1") != "stale-ed-key" {
			t.Errorf("healed %v with no key to compare", resp.Healed)
		}
	})
	t.Run("already matching", func(t *testing.T) {
		f := newHealFixture(t, false, false)
		resp, err := callDkimEnsureWith(t, dkimEnsureParams{
			DomainName: "example.com", PublishedEd25519PublicKey: f.edTXT, PublishedRSAPublicKey: f.rsaTXT,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Healed) != 0 {
			t.Errorf("healed = %v", resp.Healed)
		}
	})
}

// Stalwart refusing the healing key is logged, not an error: the signatures
// keep working as before, and the domain's other signatures still converge.
func TestDkimEnsure_HealRefused_NotAnError(t *testing.T) {
	f := newHealFixture(t, true, false)
	f.reg.refuseUpdate = map[string]bool{"ed1": true}

	resp, err := callDkimEnsureWith(t, dkimEnsureParams{
		DomainName: "example.com", PublishedEd25519PublicKey: f.edTXT, PublishedRSAPublicKey: f.rsaTXT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.secret("ed1") != "stale-ed-key" {
		t.Error("the refused signature changed")
	}
	if strings.Contains(strings.Join(resp.Healed, ","), "Dkim1Ed25519Sha256") {
		t.Errorf("healed = %v, want the refused signature left out", resp.Healed)
	}
}
