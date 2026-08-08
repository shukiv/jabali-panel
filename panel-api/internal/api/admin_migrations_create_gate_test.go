package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #522 follow-on: CyberPanel was wired end-to-end (registry, pullCyberPanel,
// import arm, Discoverer, wizard picker) but never added to isKnownSourceKind,
// so the wizard advertised it while every create POST returned
// "unknown_source_kind". Also missing from panelDiscoverer, so Test Connection
// returned "unsupported_kind". Guard both, and the sibling CloudPanel's Test
// Connection which had the same panelDiscoverer gap.
func TestCreateGate_KnowsAllWizardKinds(t *testing.T) {
	// Every kind the wizard's SOURCE_OPTIONS offers must pass the create gate,
	// or the create POST 400s on a source the operator can select.
	for _, kind := range []string{
		models.MigrationSourceCpanel,
		models.MigrationSourceWHMpkgacct,
		models.MigrationSourceDirectAdmin,
		models.MigrationSourceHestia,
		models.MigrationSourceCloudPanel,
		models.MigrationSourceCyberPanel,
		models.MigrationSourcePlesk,
		models.MigrationSourceJabali,
	} {
		if !isKnownSourceKind(kind) {
			t.Errorf("isKnownSourceKind(%q) = false — wizard offers it but create would 400", kind)
		}
	}
}

func TestPanelDiscoverer_SupportsCloudAndCyberPanel(t *testing.T) {
	for _, kind := range []string{models.MigrationSourceCloudPanel, models.MigrationSourceCyberPanel} {
		if d := panelDiscoverer(kind, false); d == nil {
			t.Errorf("panelDiscoverer(%q) = nil — Test Connection would return unsupported_kind", kind)
		}
	}
	if got := panelLabel(models.MigrationSourceCyberPanel); got != "CyberPanel" {
		t.Errorf("panelLabel(cyberpanel) = %q, want CyberPanel", got)
	}
	if got := panelLabel(models.MigrationSourceCloudPanel); got != "CloudPanel" {
		t.Errorf("panelLabel(cloudpanel) = %q, want CloudPanel", got)
	}
}
