package models

import "testing"

func TestIsValidBackupRetentionPolicy(t *testing.T) {
	for _, ok := range []string{"", "reject", "prune"} {
		if !IsValidBackupRetentionPolicy(ok) {
			t.Errorf("IsValidBackupRetentionPolicy(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"REJECT", "delete", "keep", "x", "purge"} {
		if IsValidBackupRetentionPolicy(bad) {
			t.Errorf("IsValidBackupRetentionPolicy(%q) = true, want false", bad)
		}
	}
}

// BackupRetentionPrunes must FAIL SAFE: only an explicit "prune" auto-deletes
// tenant data. Empty / unknown / mis-cased values must mean reject, so a
// misconfiguration never silently destroys backups (GH #454).
func TestBackupRetentionPrunes_FailsSafe(t *testing.T) {
	prune := HostingPackage{BackupRetentionPolicy: "prune"}
	if !prune.BackupRetentionPrunes() {
		t.Error("policy=prune should prune")
	}
	for _, safe := range []string{"", "reject", "REJECT", "Prune", "delete", "x"} {
		p := HostingPackage{BackupRetentionPolicy: safe}
		if p.BackupRetentionPrunes() {
			t.Errorf("policy=%q must NOT prune (fail-safe to reject)", safe)
		}
	}
}
