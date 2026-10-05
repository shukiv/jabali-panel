package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #357: a box installed without the mail module has no Stalwart admin token
// and no Stalwart. mail.domain.purge_accounts must say so with the distinct
// precondition answer, so a deleted domain's teardown completes instead of
// failing the token read forever.

// setMailAbsenceSeams points the token read and the Stalwart markers at test
// values for one test.
func setMailAbsenceSeams(t *testing.T, tokenErr error, markers []string) {
	t.Helper()
	origToken, origMarkers := stalwartAdminTokenFunc, stalwartMarkers
	t.Cleanup(func() { stalwartAdminTokenFunc, stalwartMarkers = origToken, origMarkers })
	stalwartAdminTokenFunc = func() (string, error) {
		if tokenErr != nil {
			return "", tokenErr
		}
		return "test-token", nil
	}
	stalwartMarkers = markers
}

func missingTokenErr(dir string) error {
	return fmt.Errorf("read %s: %w", filepath.Join(dir, "stalwart-admin.token"), fs.ErrNotExist)
}

func TestMailServerAbsent(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "jabali-stalwart.service")
	bin := filepath.Join(dir, "stalwart")
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		tokenErr error
		markers  []string
		want     bool
	}{
		{"no token, no stalwart", missingTokenErr(dir), []string{unit, bin}, true},
		{"token present", nil, []string{unit, bin}, false},
		{"no token but stalwart unit present", missingTokenErr(dir), []string{present, bin}, false},
		{"no token but stalwart dir present", missingTokenErr(dir), []string{unit, dir}, false},
		{"token unreadable for another reason", fmt.Errorf("read token: %w", fs.ErrPermission), []string{unit, bin}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setMailAbsenceSeams(t, tc.tokenErr, tc.markers)
			if got := mailServerAbsent(); got != tc.want {
				t.Fatalf("mailServerAbsent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMailDomainPurge_NoMailServerAnswersPrecondition(t *testing.T) {
	dir := t.TempDir()
	setMailAbsenceSeams(t, missingTokenErr(dir), []string{
		filepath.Join(dir, "jabali-stalwart.service"), filepath.Join(dir, "stalwart"),
	})
	_, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"gone.example","remove_domain":true}`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || ae.Message != agentwire.MsgMailServerNotInstalled {
		t.Fatalf("want %s %q, got %v", agentwire.CodeFailedPrecondition, agentwire.MsgMailServerNotInstalled, err)
	}
}

// A box that HAS Stalwart but lost its token is broken, not mail-less: the
// purge must keep failing so the teardown retries and the old owner's mail is
// never left behind.
func TestMailDomainPurge_MissingTokenWithStalwartStaysAnError(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "jabali-stalwart.service")
	if err := os.WriteFile(unit, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setMailAbsenceSeams(t, missingTokenErr(dir), []string{unit, filepath.Join(dir, "stalwart")})
	_, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"gone.example","remove_domain":true}`))
	if err == nil {
		t.Fatal("a box with Stalwart and no token must not report the purge as done")
	}
	var ae *agentwire.AgentError
	if errors.As(err, &ae) && ae.Code == agentwire.CodeFailedPrecondition {
		t.Fatalf("a box with Stalwart must not get the no-mail-server answer: %v", err)
	}
}
