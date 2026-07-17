package migrate_test

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/plesk"
)

// TestApplyPort_SetsPortOnConcreteDiscoverer — the registry hands back a
// migrate.Discoverer (interface); ApplyPort must reach the concrete Port via
// the PortSetter capability. GH #429: without this the admin test-connection /
// size-probe always dialed 22 even for a job with a custom source_port.
func TestApplyPort_SetsPortOnConcreteDiscoverer(t *testing.T) {
	d := plesk.New() // default Port 22
	if d.Port != 22 {
		t.Fatalf("default port = %d, want 22", d.Port)
	}

	var disc migrate.Discoverer = d
	migrate.ApplyPort(disc, 2222)
	if d.Port != 2222 {
		t.Errorf("after ApplyPort(2222), port = %d, want 2222", d.Port)
	}

	// Out-of-range ports are ignored (keeps the last valid value).
	migrate.ApplyPort(disc, 0)
	migrate.ApplyPort(disc, 70000)
	if d.Port != 2222 {
		t.Errorf("out-of-range ApplyPort changed port to %d, want 2222", d.Port)
	}
}
