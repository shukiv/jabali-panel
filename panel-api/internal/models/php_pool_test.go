package models

import "testing"

// JAB-344: a versioned pool must clone the COMPLETE tuning model from the
// default, so a domain's PHP-version switch never silently resets capacity or
// timeout behavior. This guards against a new tuning column being added to the
// struct but forgotten in the clone (the exact drift the ticket describes).
func TestNewVersionedPHPPool_ClonesCompleteTuning(t *testing.T) {
	def := &PHPPool{
		ID:                             "default-pool",
		UserID:                         "u1",
		PHPVersion:                     "8.4",
		PmMode:                         "static",
		PmMaxChildren:                  40,
		ProcessIdleTimeoutSeconds:      120,
		PmStartServers:                 8,
		PmMinSpareServers:              4,
		PmMaxSpareServers:              12,
		PmMaxRequests:                  500,
		RequestTerminateTimeoutSeconds: 90,
		PerformanceMode:                "high-performance",
		Status:                         "active",
	}

	got := NewVersionedPHPPool("new-pool", "7.4", def)

	// Fresh identity + version + pending status.
	if got.ID != "new-pool" || got.PHPVersion != "7.4" || got.Status != "pending" {
		t.Fatalf("identity/version/status wrong: %+v", got)
	}
	if got.UserID != "u1" {
		t.Errorf("user not carried: %q", got.UserID)
	}

	// EVERY tuning field must equal the default's — spare servers, max requests,
	// request-terminate timeout, and performance_mode included (the ones the old
	// CLI/HTTP clones dropped).
	if got.PmMode != def.PmMode ||
		got.PmMaxChildren != def.PmMaxChildren ||
		got.ProcessIdleTimeoutSeconds != def.ProcessIdleTimeoutSeconds ||
		got.PmStartServers != def.PmStartServers ||
		got.PmMinSpareServers != def.PmMinSpareServers ||
		got.PmMaxSpareServers != def.PmMaxSpareServers ||
		got.PmMaxRequests != def.PmMaxRequests ||
		got.RequestTerminateTimeoutSeconds != def.RequestTerminateTimeoutSeconds ||
		got.PerformanceMode != def.PerformanceMode {
		t.Errorf("versioned pool did not clone the complete tuning model:\n default=%+v\n got=%+v", def, got)
	}
}
