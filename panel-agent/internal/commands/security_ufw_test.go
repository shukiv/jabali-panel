package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestUFWRuleAdd_Validation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantMsg string
	}{
		{"empty", `{}`, "action must be"},
		{"bad action", `{"action":"bogus","port":"80"}`, "action must be"},
		{"bad proto", `{"action":"allow","port":"80","proto":"sctp"}`, "proto must be"},
		{"bad port format", `{"action":"allow","port":"abc"}`, "port must be"},
		{"port 0", `{"action":"allow","port":"0"}`, "port out of range"},
		{"port 70000", `{"action":"allow","port":"70000"}`, "port out of range"},
		{"reversed range", `{"action":"allow","port":"100:50"}`, "port range"},
		{"bad from", `{"action":"allow","port":"22","from":"not-an-ip"}`, "from must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ufwRuleAddHandler(context.Background(), json.RawMessage(tc.payload))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestUFWRuleDelete_Validation(t *testing.T) {
	cases := []struct {
		payload string
		wantMsg string
	}{
		{`{}`, "out of range"},
		{`{"num":0}`, "out of range"},
		{`{"num":2000}`, "out of range"},
		{`not-json`, "parse params"},
	}
	for _, tc := range cases {
		t.Run(tc.payload, func(t *testing.T) {
			_, err := ufwRuleDeleteHandler(context.Background(), json.RawMessage(tc.payload))
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("got %v, want substring %q", err, tc.wantMsg)
			}
		})
	}
}

func TestUFWDefaultSet_Validation(t *testing.T) {
	cases := []struct {
		payload string
		wantMsg string
	}{
		{`{"chain":"ingoing","policy":"allow"}`, "chain must be"},
		{`{"chain":"incoming","policy":"reject_all"}`, "policy must be"},
	}
	for _, tc := range cases {
		t.Run(tc.payload, func(t *testing.T) {
			_, err := ufwDefaultSetHandler(context.Background(), json.RawMessage(tc.payload))
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("got %v, want substring %q", err, tc.wantMsg)
			}
		})
	}
}

// TestUFWEnable_RequiresConfirm guards the lockout failsafe — if this
// regresses, an admin misclick on the panel can drop the firewall.
func TestUFWEnable_RequiresConfirm(t *testing.T) {
	if _, err := ufwEnableHandler(context.Background(), json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "confirm must be") {
		t.Fatalf("ufw.enable without confirm did not reject: %v", err)
	}
	if _, err := ufwEnableHandler(context.Background(), json.RawMessage(`{"confirm":"yes"}`)); err == nil ||
		!strings.Contains(err.Error(), "confirm must be") {
		t.Fatalf("ufw.enable with confirm=yes (lowercase) did not reject: %v", err)
	}
}

func TestUFWDisable_RequiresConfirm(t *testing.T) {
	if _, err := ufwDisableHandler(context.Background(), json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "confirm must be") {
		t.Fatalf("ufw.disable without confirm did not reject: %v", err)
	}
}

func TestParseUfwStatus_Smoke(t *testing.T) {
	sample := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), deny (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 80/tcp                     ALLOW IN    Anywhere
[ 3] 443/tcp                    ALLOW IN    Anywhere
`
	resp := parseUfwStatus(sample)
	if !resp.Active {
		t.Errorf("expected Active=true")
	}
	if resp.DefaultIn != "deny" || resp.DefaultOut != "allow" {
		t.Errorf("default mismatch: in=%q out=%q", resp.DefaultIn, resp.DefaultOut)
	}
	if len(resp.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(resp.Rules))
	}
	if resp.Rules[0].Num != 1 || resp.Rules[0].Port != "22" || resp.Rules[0].Proto != "tcp" {
		t.Errorf("rule[0] parsed wrong: %+v", resp.Rules[0])
	}
}

// GH #2271: a disabled firewall was reported as active, so the page offered
// only "Disable firewall" and the firewall could be enabled again only with
// `ufw enable`. ufw prints "Status: inactive" for both status calls when it is
// off, and "inactive" contains "active".
func TestUfwStatusHandler_ReportsWhetherTheFirewallIsOn(t *testing.T) {
	active := "Status: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), deny (routed)\nNew profiles: skip\n\n" +
		"     To                         Action      From\n     --                         ------      ----\n" +
		"[ 1] 22/tcp                     ALLOW IN    Anywhere\n"
	for _, tc := range []struct {
		name   string
		out    string
		active bool
	}{
		{"disabled", "Status: inactive\n", false},
		{"enabled", active, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := execCommandContext
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "ufw" || len(args) == 0 || args[0] != "status" {
					t.Fatalf("unexpected command %s %v", name, args)
				}
				return exec.CommandContext(ctx, "printf", "%s", tc.out)
			}
			t.Cleanup(func() { execCommandContext = prev })

			got, err := ufwStatusHandler(context.Background(), nil)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if resp := got.(ufwStatusResponse); resp.Active != tc.active {
				t.Fatalf("active = %v, want %v", resp.Active, tc.active)
			}
		})
	}
}
