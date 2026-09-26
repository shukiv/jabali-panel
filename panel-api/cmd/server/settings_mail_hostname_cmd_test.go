package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailhostops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390: install.sh renders the Bulwark JMAP URL and the /webmail redirects
// from `jabali settings mail-hostname --applied`, so a `jabali update` after a
// shared-mail-hostname switchover keeps the applied name instead of reverting
// every render to mail.<hostname>. The output is interpolated into config
// files: only a validated, normalized bare FQDN may ever be printed.
func TestPrintMailHostname(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name        string
		hostname    string
		stored      *string
		appliedOnly bool
		want        string
		wantErr     bool
	}{
		{name: "derived, effective", hostname: "mx.example.com", want: "mail.mx.example.com\n"},
		{name: "derived, applied only prints nothing", hostname: "mx.example.com", appliedOnly: true, want: ""},
		{name: "custom, effective", hostname: "mx.example.com", stored: str(" MX.Example.NET "), want: "mx.example.net\n"},
		{name: "custom, applied only", hostname: "mx.example.com", stored: str("MX.Example.NET"), appliedOnly: true, want: "mx.example.net\n"},
		{name: "invalid stored value falls back to derived", hostname: "mx.example.com", stored: str("https://evil.example/"), want: "mail.mx.example.com\n"},
		{name: "invalid stored value is never printed as applied", hostname: "mx.example.com", stored: str("https://evil.example/"), appliedOnly: true, want: ""},
		{name: "config injection is never printed", hostname: "mx.example.com", stored: str("mx.example.net;\nreturn 301 https://evil"), appliedOnly: true, want: ""},
		{name: "no hostname and no custom name is an error", hostname: "", wantErr: true},
		{name: "no hostname, applied only prints nothing", hostname: "", appliedOnly: true, want: ""},
		{name: "no hostname, custom name still resolves", hostname: "", stored: str("mx.example.net"), want: "mx.example.net\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := printMailHostname(&out, &models.ServerSettings{Hostname: tc.hostname, MailHostname: tc.stored}, tc.appliedOnly)
			if tc.wantErr {
				require.Error(t, err)
				require.Empty(t, out.String(), "nothing may be printed on error")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, out.String())
		})
	}
}

func TestSettingsMailHostnameCmdRegistered(t *testing.T) {
	cmd, _, err := newSettingsCmd().Find([]string{"mail-hostname"})
	require.NoError(t, err)
	require.Equal(t, "mail-hostname", cmd.Name())
	require.NotNil(t, cmd.Flags().Lookup("applied"), "install.sh calls `settings mail-hostname --applied`")
	require.NotNil(t, cmd.PreRunE, "the command reads the DB")
}

// JAB-390 setter on the CLI: `--set NAME` records a switchover request,
// `--cancel` withdraws one, `--status` prints its progress. Each is audited,
// and the modes are mutually exclusive with each other and with --applied.

func TestMailHostnameCmdMode(t *testing.T) {
	cases := []struct {
		name                       string
		set                        bool
		cancel, status, appliedOnly bool
		want                       mailHostnameMode
		wantErr                    bool
	}{
		{name: "read", want: mailHostnameRead},
		{name: "applied", appliedOnly: true, want: mailHostnameRead},
		{name: "set", set: true, want: mailHostnameSet},
		{name: "cancel", cancel: true, want: mailHostnameCancel},
		{name: "status", status: true, want: mailHostnameStatus},
		{name: "set and cancel", set: true, cancel: true, wantErr: true},
		{name: "set and applied", set: true, appliedOnly: true, wantErr: true},
		{name: "cancel and status", cancel: true, status: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mailHostnameCmdMode(tc.set, tc.cancel, tc.status, tc.appliedOnly)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

type cliSwitchover struct {
	repository.MailHostnameSwitchoverRepository
	row       *models.MailHostnameSwitchover
	requested []string
	by        []string
	cancelErr error
}

func (f *cliSwitchover) Get(context.Context) (*models.MailHostnameSwitchover, error) {
	if f.row == nil {
		return nil, repository.ErrNotFound
	}
	return f.row, nil
}

func (f *cliSwitchover) Request(_ context.Context, desired, by string, _ time.Time) error {
	f.requested, f.by = append(f.requested, desired), append(f.by, by)
	return nil
}

func (f *cliSwitchover) Cancel(context.Context, time.Time) error { return f.cancelErr }

type cliSettings struct {
	repository.ServerSettingsRepository
}

func (cliSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: "panel.example.com", AdminEmail: "admin@example.com", WebmailEnabled: true}, nil
}

type cliCerts struct {
	repository.PanelCertificateRepository
	useLE bool
}

func (f cliCerts) GetByKind(_ context.Context, kind string) (*models.PanelCertificate, error) {
	return &models.PanelCertificate{Kind: kind, UseLE: f.useLE}, nil
}

type cliDomains struct {
	repository.DomainRepository
}

func (cliDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if name == "panel.example.com" {
		return cliPrimary(), nil
	}
	return nil, repository.ErrNotFound
}

func (cliDomains) FindPanelPrimary(context.Context) (*models.Domain, error) { return cliPrimary(), nil }

func cliPrimary() *models.Domain {
	return &models.Domain{Name: "panel.example.com", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true}
}

type cliAuditLog struct{ entries []string }

func (a *cliAuditLog) record(action, target, result string) {
	a.entries = append(a.entries, action+" "+target+" "+result)
}

func cliMailHostDeps(sw *cliSwitchover, useLE bool) mailhostops.RequestDeps {
	return mailhostops.RequestDeps{Settings: cliSettings{}, PanelCerts: cliCerts{useLE: useLE}, Domains: cliDomains{}, Switchover: sw}
}

func TestRunMailHostnameSet(t *testing.T) {
	sw, log, out := &cliSwitchover{}, &cliAuditLog{}, &bytes.Buffer{}
	require.NoError(t, runMailHostnameSet(context.Background(), out, cliMailHostDeps(sw, true), log.record, "MX.Example.ORG"))
	require.Equal(t, []string{"mx.example.org"}, sw.requested)
	require.Equal(t, []string{"cli"}, sw.by)
	require.Contains(t, out.String(), "mx.example.org")
	require.Equal(t, []string{"settings.mail_hostname.request mx.example.org ok"}, log.entries)

	sw, log, out = &cliSwitchover{}, &cliAuditLog{}, &bytes.Buffer{}
	err := runMailHostnameSet(context.Background(), out, cliMailHostDeps(sw, false), log.record, "mx.example.org")
	require.ErrorIs(t, err, mailhostops.ErrNotReady)
	require.Empty(t, sw.requested)
	require.Equal(t, []string{"settings.mail_hostname.request mx.example.org denied"}, log.entries)
}

func TestRunMailHostnameCancel(t *testing.T) {
	sw, log, out := &cliSwitchover{}, &cliAuditLog{}, &bytes.Buffer{}
	require.NoError(t, runMailHostnameCancel(context.Background(), out, cliMailHostDeps(sw, true), log.record))
	require.Equal(t, []string{"settings.mail_hostname.cancel  ok"}, log.entries)

	sw.cancelErr = repository.ErrSwitchoverInFlight
	log.entries = nil
	require.ErrorIs(t, runMailHostnameCancel(context.Background(), out, cliMailHostDeps(sw, true), log.record), repository.ErrSwitchoverInFlight)
	require.Equal(t, []string{"settings.mail_hostname.cancel  denied"}, log.entries)
}

func TestPrintMailHostnameStatus(t *testing.T) {
	out := &bytes.Buffer{}
	require.NoError(t, printMailHostnameStatus(out, nil))
	require.Equal(t, "no mail hostname change requested\n", out.String())

	desired := "mx.example.org"
	retry := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	out.Reset()
	require.NoError(t, printMailHostnameStatus(out, &models.MailHostnameSwitchover{Desired: &desired,
		Status: models.MailHostnameSwitchoverFailed, LastError: "mx.example.org does not point at this server", NextRetryAt: &retry}))
	require.Equal(t, "desired: mx.example.org\nstatus: failed\nlast_error: mx.example.org does not point at this server\nnext_retry_at: 2026-09-27T11:00:00Z\n", out.String())
}
