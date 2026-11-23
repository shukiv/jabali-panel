package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func patchSettingsJSON(t *testing.T, r http.Handler, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func pdnsRebindCalls(a *agent.MockClient) []map[string]any {
	var out []map[string]any
	for _, c := range a.Calls() {
		if c.Command == "dns.pdns_rebind" {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

// New nameserver addresses reach PowerDNS: after an IP swap it listened only
// on the old address until someone restarted it by hand.
func TestServerSettingsPatch_NameserverAddrChange_DispatchesPDNSRebind(t *testing.T) {
	existing := &models.ServerSettings{ID: 1, Hostname: "panel.example.com", SSHPort: 22,
		NS1Name: "ns1.example.com", NS1IPv4: "192.0.2.121", NS2Name: "ns2.example.com", NS2IPv4: "192.0.2.121"}
	a := agent.NewMockClient().On("dns.pdns_rebind", map[string]any{"restarted": true})
	r := settingsRouter(true, &mockServerSettingsRepo{getResult: existing}, a)

	patchSettingsJSON(t, r, map[string]any{"ns1_ipv4": "192.0.2.9", "ns2_ipv4": "192.0.2.9"})

	require.Eventually(t, func() bool { return len(pdnsRebindCalls(a)) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, map[string]any{"addresses": []any{"192.0.2.9"}}, pdnsRebindCalls(a)[0])
}

func TestServerSettingsPatch_NoNameserverAddrChange_NoPDNSRebind(t *testing.T) {
	existing := &models.ServerSettings{ID: 1, Hostname: "panel.example.com", SSHPort: 22,
		NS1Name: "ns1.example.com", NS1IPv4: "192.0.2.121", NS2Name: "ns2.example.com", NS2IPv4: "192.0.2.121"}
	a := agent.NewMockClient()
	r := settingsRouter(true, &mockServerSettingsRepo{getResult: existing}, a)

	patchSettingsJSON(t, r, map[string]any{"ns1_ipv4": "192.0.2.121"})

	time.Sleep(50 * time.Millisecond)
	require.Empty(t, pdnsRebindCalls(a))
}
