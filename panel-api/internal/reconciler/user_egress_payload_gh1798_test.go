package reconciler

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1798: buildEgressUserPayload folds the per-package SSH-out + ICMP
// allowances into one user's slot of the user.egress.apply payload.

// sshExtras returns the :22 TCP extras in a rendered payload.
func sshExtras(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["allowed_extra"].([]map[string]any)
	if !ok {
		t.Fatalf("allowed_extra missing or wrong type: %T", payload["allowed_extra"])
	}
	var out []map[string]any
	for _, e := range raw {
		if p, ok := e["port"].(int); ok && p == 22 {
			out = append(out, e)
		}
	}
	return out
}

func TestBuildEgressUserPayload_NullPackageDeniesBoth(t *testing.T) {
	// A NULL package COALESCEs to false/"" upstream. The payload must grant
	// neither SSH-out nor ping — the #282 "privileged feature → deny when no
	// package" rule.
	p := repository.PolicyForReconcile{
		Username:     "alice",
		State:        models.UserEgressStateEnforced,
		EgressSSHOut: false, EgressICMP: false, EgressSSHOutCIDRs: "",
	}
	payload, err := buildEgressUserPayload(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := sshExtras(t, payload); len(got) != 0 {
		t.Fatalf("NULL package must add no :22 extras, got %v", got)
	}
	if payload["allow_ping"] != false {
		t.Fatalf("NULL package must not allow ping, got %v", payload["allow_ping"])
	}
}

func TestBuildEgressUserPayload_SSHOutDefaultCIDRs(t *testing.T) {
	p := repository.PolicyForReconcile{
		Username:     "bob",
		State:        models.UserEgressStateEnforced,
		EgressSSHOut: true, EgressSSHOutCIDRs: "", // empty = anywhere v4+v6
	}
	payload, err := buildEgressUserPayload(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := sshExtras(t, payload)
	if len(got) != 2 {
		t.Fatalf("want 2 :22 extras (v4+v6), got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		if e["protocol"] != "tcp" {
			t.Fatalf(":22 extra must be tcp, got %v", e["protocol"])
		}
		seen[e["cidr"].(string)] = true
	}
	if !seen["0.0.0.0/0"] || !seen["::/0"] {
		t.Fatalf("want anywhere v4+v6, got %v", seen)
	}
}

func TestBuildEgressUserPayload_SSHOutScopedCIDRs(t *testing.T) {
	p := repository.PolicyForReconcile{
		Username:     "carol",
		State:        models.UserEgressStateEnforced,
		EgressSSHOut: true, EgressSSHOutCIDRs: `["140.82.112.0/20"]`,
	}
	payload, err := buildEgressUserPayload(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := sshExtras(t, payload)
	if len(got) != 1 || got[0]["cidr"] != "140.82.112.0/20" {
		t.Fatalf("want a single scoped :22 extra, got %v", got)
	}
}

func TestBuildEgressUserPayload_ICMPAllowed(t *testing.T) {
	p := repository.PolicyForReconcile{
		Username: "dave", State: models.UserEgressStateEnforced,
		EgressICMP: true,
	}
	payload, err := buildEgressUserPayload(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if payload["allow_ping"] != true {
		t.Fatalf("EgressICMP=true must set allow_ping, got %v", payload["allow_ping"])
	}
}

func TestBuildEgressUserPayload_CorruptCIDRsFailClosed(t *testing.T) {
	// A corrupt column must DROP the SSH-out extras (fail closed) and return the
	// parse error for the caller to log — never fall open to "anywhere".
	p := repository.PolicyForReconcile{
		Username:     "eve",
		State:        models.UserEgressStateEnforced,
		EgressSSHOut: true, EgressSSHOutCIDRs: `["1.2.3.0/24",`, // truncated JSON
	}
	payload, err := buildEgressUserPayload(p)
	if err == nil {
		t.Fatal("corrupt CIDRs must return an error")
	}
	if got := sshExtras(t, payload); len(got) != 0 {
		t.Fatalf("corrupt CIDRs must add no :22 extras (fail closed), got %v", got)
	}
}

func TestBuildEgressUserPayload_BasePolicyPreserved(t *testing.T) {
	// The user's own allowed_extra must always pass through untouched.
	port := 6379
	p := repository.PolicyForReconcile{
		Username: "frank", State: models.UserEgressStateEnforced,
		AllowedExtra: []models.EgressDestination{
			{CIDR: "10.0.0.0/8", Port: &port, Protocol: "tcp"},
		},
	}
	payload, err := buildEgressUserPayload(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	extras := payload["allowed_extra"].([]map[string]any)
	if len(extras) != 1 || extras[0]["cidr"] != "10.0.0.0/8" {
		t.Fatalf("base extra dropped: %v", extras)
	}
}
