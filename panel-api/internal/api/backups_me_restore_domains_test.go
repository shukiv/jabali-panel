package api

// GH #2303: the tenant restore picker lists every domain of the backup the
// account still owns (for its files, and its DNS), and the restore passes
// "delete files added since the backup" on to the agent.

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestFilterManifestDomains(t *testing.T) {
	raw := json.RawMessage(`{"kind":"account_backup","stages":[{"name":"home","status":"ok"}],` +
		`"domains":["kept.example","gone.example","dns.example"],"dns_domains":["dns.example","gone.example"]}`)
	owned := map[string]string{"kept.example": "d1", "dns.example": "d2"}
	out := filterManifestDomains(raw, owned)

	var got struct {
		Kind       string            `json:"kind"`
		Stages     []json.RawMessage `json:"stages"`
		Domains    []string          `json:"domains"`
		DNSDomains []string          `json:"dns_domains"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("filtered manifest is not JSON: %v (%s)", err, out)
	}
	if want := []string{"kept.example", "dns.example"}; !reflect.DeepEqual(got.Domains, want) {
		t.Errorf("domains = %v, want %v", got.Domains, want)
	}
	if want := []string{"dns.example"}; !reflect.DeepEqual(got.DNSDomains, want) {
		t.Errorf("dns_domains = %v, want %v", got.DNSDomains, want)
	}
	if got.Kind != "account_backup" || len(got.Stages) != 1 {
		t.Errorf("the rest of the manifest must pass through: %s", out)
	}
}

// An older agent sends no "domains": the picker then has none to list, and
// the reply stays valid.
func TestFilterManifestDomains_OlderAgent(t *testing.T) {
	out := filterManifestDomains(json.RawMessage(`{"kind":"account_backup","dns_domains":["dns.example"]}`),
		map[string]string{"dns.example": "d2"})
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["dns_domains"]) != `["dns.example"]` {
		t.Errorf("dns_domains = %s", got["dns_domains"])
	}
}

type paramsAgent struct {
	mu     sync.Mutex
	params map[string]any
}

func (a *paramsAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "backup.restore_selective" {
		a.mu.Lock()
		a.params, _ = params.(map[string]any)
		a.mu.Unlock()
	}
	return json.RawMessage(`{"applied":["domain kept.example"]}`), nil
}

func TestRunSelectiveRestoreJob_PassesDeleteExtra(t *testing.T) {
	for _, del := range []bool{false, true} {
		jobs := newSealCapture()
		ag := &paramsAgent{}
		h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs, Agent: ag}}
		h.runSelectiveRestoreJob("job-1", "snap", "alice", "u1",
			meRestoreSelectiveRequest{Domains: []string{"kept.example"}, DeleteExtra: del, Overwrite: true},
			map[string]string{"kept.example": "d1"}, &models.BackupDestination{ID: "d1", Kind: "local"})
		jobs.wait(t)
		ag.mu.Lock()
		got := ag.params["delete_extra"]
		ag.mu.Unlock()
		if got != del {
			t.Errorf("delete_extra=%v: agent got %v", del, got)
		}
	}
}
