package commands

import (
	"encoding/json"
	"testing"
)

// GH #1646: a system backup must apply the operator's chosen restic compression
// to the config used for every system-stage snapshot, the same way the account
// path (backup_create.go:203) does. Empty stays restic's default (auto).
func TestSystemResticConfig_AppliesCompression(t *testing.T) {
	cfg, err := systemResticConfig(systemBackupParams{Compression: "max"})
	if err != nil {
		t.Fatalf("systemResticConfig: %v", err)
	}
	if cfg.Compression != "max" {
		t.Errorf("Compression = %q, want max", cfg.Compression)
	}

	cfg, err = systemResticConfig(systemBackupParams{})
	if err != nil {
		t.Fatalf("systemResticConfig: %v", err)
	}
	if cfg.Compression != "" {
		t.Errorf("empty request must leave Compression unset (restic default), got %q", cfg.Compression)
	}
}

// The panel dispatches the level as the JSON key "compression"; the agent's
// param struct must bind it (shared panel↔agent wire contract).
func TestSystemBackupParams_UnmarshalsCompression(t *testing.T) {
	var p systemBackupParams
	if err := json.Unmarshal([]byte(`{"job_id":"01HXXXXXXXXXXXXXXXXXXXXXXX","compression":"max"}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Compression != "max" {
		t.Errorf("Compression = %q, want max (json tag must bind)", p.Compression)
	}
}
