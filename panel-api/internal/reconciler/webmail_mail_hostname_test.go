package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-390: every mail vhost's sub_filter must rewrite the panel mail
// hostname Bulwark's JMAP URL carries — the applied custom shared mail
// hostname when one is applied, else mail.<hostname> — and the panel-primary
// mail vhost must also answer a custom name. The reconciler sends both to
// webmail.vhost_apply.

func TestPanelMailHostnameVhostParams(t *testing.T) {
	str := func(s string) *string { return &s }
	tenant := &models.Domain{Name: "tenant.com"}
	primary := &models.Domain{Name: "mx.jabali-panel.com", IsPanelPrimary: true}
	cases := []struct {
		name     string
		settings *models.ServerSettings
		domain   *models.Domain
		want     map[string]any
	}{
		{name: "derived, tenant", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com"}, domain: tenant,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mail.mx.jabali-panel.com"}},
		{name: "derived, panel-primary: no extra names", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com"}, domain: primary,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mail.mx.jabali-panel.com"}},
		{name: "custom, tenant: sub_filter only", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com", MailHostname: str("MX.Example.NET")}, domain: tenant,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mx.example.net"}},
		{name: "custom, panel-primary answers it", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com", MailHostname: str("mx.example.net")}, domain: primary,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mx.example.net", "extra_server_names": []string{"mx.example.net"}}},
		{name: "custom equal to the derived name adds nothing", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com", MailHostname: str("mail.mx.jabali-panel.com")}, domain: primary,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mail.mx.jabali-panel.com"}},
		{name: "invalid stored value is ignored", settings: &models.ServerSettings{Hostname: "mx.jabali-panel.com", MailHostname: str("https://evil.example/")}, domain: primary,
			want: map[string]any{"panel_hostname": "mx.jabali-panel.com", "panel_mail_hostname": "mail.mx.jabali-panel.com"}},
		{name: "no hostname, nothing applied", settings: &models.ServerSettings{}, domain: tenant,
			want: map[string]any{}},
		{name: "no settings row", settings: nil, domain: tenant,
			want: map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			addPanelMailHostnameParams(params, tc.settings, tc.domain)
			assert.Equal(t, tc.want, params)
		})
	}
}

// paramsWebmailAgent records the params of every webmail.vhost_apply call.
type paramsWebmailAgent struct {
	fakeWebmailAgent
	applies []map[string]any
}

func (f *paramsWebmailAgent) Call(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "webmail.vhost_apply" {
		if m, ok := params.(map[string]any); ok {
			f.applies = append(f.applies, m)
		}
	}
	return f.fakeWebmailAgent.Call(ctx, cmd, params)
}

// The sweep sends what addPanelMailHostnameParams builds.
func TestWebmailVhostApply_SendsPanelMailHostname(t *testing.T) {
	ag := &paramsWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "tenant.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	certs := newFakeSSLCertRepo()
	certs.byDomain["d1"] = &models.SSLCertificate{DomainID: "d1", Status: models.SSLStatusIssued,
		CertPath: wmPtr("/etc/letsencrypt/live/tenant.com/fullchain.pem"), KeyPath: wmPtr("/etc/letsencrypt/live/tenant.com/privkey.pem")}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).WithPackages(&webmailGatePkgRepo{})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		WebmailEnabled: true, Hostname: "mx.jabali-panel.com", MailHostname: wmPtr("mx.example.net"),
	}}

	r.reconcileWebmailVhosts(context.Background())

	require.Len(t, ag.applies, 1, "one webmail.vhost_apply for the one webmail domain")
	assert.Equal(t, "mx.example.net", ag.applies[0]["panel_mail_hostname"])
	assert.Equal(t, "mx.jabali-panel.com", ag.applies[0]["panel_hostname"])
	assert.NotContains(t, ag.applies[0], "extra_server_names", "a tenant vhost never answers extra names")
}
