package commands

// JAB-237 — serve_https decoupled from redirect_https. The production
// incident: a CDN-fronted domain parked on its self-signed bootstrap cert
// rendered :80-only, and Cloudflare Full (which dials origin :443) returned
// 520 for every zone. The fix serves :443 without the :80→:443 redirect —
// the redirect would loop CF Flexible zones, whose edge fetches over :80.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"text/template"
)

func renderJAB237(t *testing.T, vd vhostData) string {
	t.Helper()
	tmpl := template.Must(template.New("vhost").Parse(vhostTemplate))
	var b bytes.Buffer
	if err := tmpl.Execute(&b, vd); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The fronted shape: :443 block WITHOUT the :80 redirect.
func TestVhostJAB237_ServeWithoutRedirect(t *testing.T) {
	vd := gh896VhostData(false)
	vd.ServeHTTPS = true
	out := renderJAB237(t, vd)

	if !strings.Contains(out, "listen 443 ssl") {
		t.Error("fronted domain must render the :443 block even on a self-signed cert — CF Full dials origin :443 and 520s on a closed port")
	}
	if strings.Contains(out, "return 301 https://") {
		t.Error("fronted domain must NOT origin-redirect :80 — that loops CF Flexible zones")
	}
	if !strings.Contains(out, "location ^~ /.well-known/acme-challenge/") {
		t.Error("ACME location must survive on :80")
	}
}

// Wire-compat: a pre-JAB-237 panel that omits serve_https gets the exact
// historical coupling (serve iff redirect).
func TestVhostJAB237_AbsentServeFallsBackToCoupling(t *testing.T) {
	for _, redirect := range []bool{true, false} {
		var p domainCreateParams
		raw := `{"domain":"example.com","ssl_cert_path":"/x/fullchain.pem"`
		if !redirect {
			raw += `,"redirect_https":false`
		}
		raw += `}`
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		gotRedirect, gotServe := resolveHTTPSFlags(&p)
		if gotRedirect != redirect || gotServe != redirect {
			t.Errorf("redirect=%v: resolved (redirect=%v, serve=%v), want coupled", redirect, gotRedirect, gotServe)
		}
	}
}

func TestVhostJAB237_ExplicitFlagsResolve(t *testing.T) {
	var p domainCreateParams
	raw := `{"domain":"example.com","ssl_cert_path":"/x/fullchain.pem","redirect_https":false,"serve_https":true}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	gotRedirect, gotServe := resolveHTTPSFlags(&p)
	if gotRedirect || !gotServe {
		t.Errorf("resolved (redirect=%v, serve=%v), want (false, true) — the fronted shape", gotRedirect, gotServe)
	}
}
