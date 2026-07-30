package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeWebmailAgent records service.* calls.
type fakeWebmailAgent struct{ cmds []string }

func (f *fakeWebmailAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	f.cmds = append(f.cmds, cmd)
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeWebmailAgent) has(cmd string) bool {
	for _, c := range f.cmds {
		if c == cmd {
			return true
		}
	}
	return false
}

// TestWebmailDisabled_StopsAndDisables_EvenWithoutSSLCerts is the #760
// regression: disabling webmail must stop AND disable the jabali-webmail daemon
// even when sslCerts is nil (an install without ACME wired). The old code
// returned at the `sslCerts == nil` guard before ever stopping the daemon, so
// the Node process kept running after the operator turned webmail off.
func TestWebmailDisabled_StopsAndDisables_EvenWithoutSSLCerts(t *testing.T) {
	ag := &fakeWebmailAgent{}
	dr := newFakeDomainRepo()
	r := New(dr, nil, ag, slog.Default(), Config{}) // NOTE: no WithSSLCerts → sslCerts stays nil
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{WebmailEnabled: false}}

	r.reconcileWebmailVhosts(context.Background())

	assert.True(t, ag.has("service.stop"), "expected service.stop for jabali-webmail when disabled")
	assert.True(t, ag.has("service.disable"), "expected service.disable for jabali-webmail when disabled")
	assert.False(t, ag.has("service.start"), "must not start webmail while disabled")
}

// TestWebmailEnabled_StartsAndEnables: with an email+webmail-enabled domain and
// SSL wired, the daemon is started AND enabled (survives reboot).
func TestWebmailEnabled_StartsAndEnables(t *testing.T) {
	ag := &fakeWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "example.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	sc := newFakeSSLCertRepo()
	r := New(dr, nil, ag, slog.Default(), Config{}).WithSSLCerts(sc)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{WebmailEnabled: true}}

	r.reconcileWebmailVhosts(context.Background())

	assert.True(t, ag.has("service.start"), "expected service.start when a webmail domain is active")
	assert.True(t, ag.has("service.enable"), "expected service.enable so webmail survives reboot")
	assert.False(t, ag.has("service.disable"), "must not disable while enabled")
}
