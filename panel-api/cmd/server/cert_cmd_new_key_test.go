package main

import (
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The agent reads these exact keys (sslPanelSelfSignParams and
// sslPanelMailSelfSignParams; pinned on that side by
// TestPanelSelfSignParams_NewKeyWireName). A renamed key would make the
// agent see new_key=false and keep the old key while the CLI says it replaced it.
func TestPanelCertNewKeyRequest_Hostname(t *testing.T) {
	verb, params, err := panelCertNewKeyRequest(models.PanelCertKindHostname,
		&models.PanelCertificate{Kind: models.PanelCertKindHostname},
		&models.ServerSettings{Hostname: "panel.example.com", PublicIPv4: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"hostname": "panel.example.com", "ip": "203.0.113.7", "new_key": true}
	if verb != "ssl.panel.selfsign" || !reflect.DeepEqual(params, want) {
		t.Fatalf("got %s %v, want ssl.panel.selfsign %v", verb, params, want)
	}
}

func TestPanelCertNewKeyRequest_MailUsesTheMailNames(t *testing.T) {
	verb, params, err := panelCertNewKeyRequest(models.PanelCertKindMail,
		&models.PanelCertificate{Kind: models.PanelCertKindMail, Hostname: "mx.example.net"},
		&models.ServerSettings{Hostname: "panel.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"names": []string{"mx.example.net", "mail.panel.example.com"}, "new_key": true}
	if verb != "ssl.panel.mail_selfsign" || !reflect.DeepEqual(params, want) {
		t.Fatalf("got %s %v, want ssl.panel.mail_selfsign %v", verb, params, want)
	}
}

func TestPanelCertNewKeyRequest_NeedsAHostname(t *testing.T) {
	for _, s := range []*models.ServerSettings{nil, {Hostname: "  "}} {
		if _, _, err := panelCertNewKeyRequest(models.PanelCertKindHostname, nil, s); err == nil {
			t.Fatalf("no error for settings %+v", s)
		}
	}
}

func TestPanelCertNewKeyMessage(t *testing.T) {
	if m := panelCertNewKeyMessage(models.PanelCertKindHostname, true, ""); !strings.Contains(m, "panel restarted") {
		t.Errorf("hostname: %q", m)
	}
	if m := panelCertNewKeyMessage(models.PanelCertKindMail, true, ""); !strings.Contains(m, "mail server restarted") {
		t.Errorf("mail: %q", m)
	}
	m := panelCertNewKeyMessage(models.PanelCertKindHostname, false, "preserved: cert issued by Let's Encrypt")
	if !strings.Contains(m, "kept") || !strings.Contains(m, "preserved: cert issued by Let's Encrypt") {
		t.Errorf("kept: %q", m)
	}
}
