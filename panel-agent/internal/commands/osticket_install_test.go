package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// osticketAdminErr runs the install handler up to its argument checks and
// returns the error message. The docroot is outside /home/<user>/domains, so
// a request that passes the admin checks stops at the docroot check before it
// touches the disk.
func osticketAdminErr(t *testing.T, user, pass string) string {
	t.Helper()
	params, _ := json.Marshal(osticketInstallReq{
		OSUser: "alice", Docroot: "/nonexistent", SiteURL: "https://help.example.test",
		HelpdeskEmail: "support@example.test", AdminEmail: "owner@example.test",
		AdminUsername: user, AdminPass: pass,
		DBName: "db", DBUser: "dbu", DBPassword: "dbp",
	})
	_, err := osticketInstallHandler(context.Background(), params)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("user %q: want an invalid-argument error, got %v", user, err)
	}
	return ae.Message
}

// TestOSTicketInstall_RefusesAdminTheInstallerRefuses: osTicket 1.18.4's own
// installer refuses these admin usernames and passwords after the database
// exists; the agent refuses them first with a message that names the field.
func TestOSTicketInstall_RefusesAdminTheInstallerRefuses(t *testing.T) {
	for _, user := range []string{"admin", "ADMINS", "Username", "osTicket", "12345", "1e5", "-5", ".5", "a", "ab c", "ab@c"} {
		if msg := osticketAdminErr(t, user, "goodpass"); !strings.Contains(msg, "admin_username") {
			t.Errorf("admin_username %q: got %q, want an admin_username error", user, msg)
		}
	}
	for _, pass := range []string{"12345", strings.Repeat("p", 129), strings.Repeat("ü", 65)} {
		if msg := osticketAdminErr(t, "helpdesk", pass); !strings.Contains(msg, "admin_pass") {
			t.Errorf("%d-byte admin_pass: got %q, want an admin_pass error", len(pass), msg)
		}
	}
	for _, ok := range []struct{ user, pass string }{
		{"helpdesk", "123456"},
		{"ab", strings.Repeat("p", 128)},
		{"agent.smith_2-x", "üüü"},
		{"Ünal", "goodpass"},
	} {
		if msg := osticketAdminErr(t, ok.user, ok.pass); strings.Contains(msg, "admin_") {
			t.Errorf("user %q with a %d-byte password: refused with %q", ok.user, len(ok.pass), msg)
		}
	}
}
