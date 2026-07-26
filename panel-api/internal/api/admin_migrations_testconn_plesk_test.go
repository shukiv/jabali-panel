package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestPanelDiscoverer_SupportsPlesk guards GH #429: plesk was missing from the
// panelDiscoverer switch, so the wizard's Test Connection (and the per-account
// describe) returned "unsupported_kind" for a Plesk source.
func TestPanelDiscoverer_SupportsPlesk(t *testing.T) {
	if d := panelDiscoverer(models.MigrationSourcePlesk, false); d == nil {
		t.Fatal("panelDiscoverer(plesk) = nil — Test Connection would return unsupported_kind")
	}
	if got := panelLabel(models.MigrationSourcePlesk); got != "Plesk" {
		t.Fatalf("panelLabel(plesk) = %q, want %q", got, "Plesk")
	}
}
