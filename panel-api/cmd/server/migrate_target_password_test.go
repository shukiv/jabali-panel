package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeTargetUsers struct {
	user *models.User
	err  error
}

func (f fakeTargetUsers) FindByUsername(context.Context, string) (*models.User, error) {
	return f.user, f.err
}

// An existing destination user keeps its password: nothing is generated, and
// no password line reaches the output (the import unit's output is the
// journal).
func TestFindOrPrepareTargetUser_ExistingUserGetsNoPassword(t *testing.T) {
	name := "acct"
	existing := &models.User{ID: "u-1", Username: &name}
	var out bytes.Buffer

	user, pw, err := findOrPrepareTargetUser(context.Background(), fakeTargetUsers{user: existing},
		&out, false, "acct", "", true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if user != existing || pw != "" {
		t.Fatalf("got user=%v pw=%q, want the existing user and no password", user, pw)
	}
	if strings.Contains(out.String(), "target-password") {
		t.Fatalf("password line printed for an existing user: %q", out.String())
	}
}

// A non-interactive run that has to create the user still gets a usable
// password to create it with, but the password never reaches the output.
func TestFindOrPrepareTargetUser_NonInteractiveCreateNeverPrintsPassword(t *testing.T) {
	var out bytes.Buffer

	user, pw, err := findOrPrepareTargetUser(context.Background(),
		fakeTargetUsers{err: repository.ErrNotFound}, &out, false, "acct", "", true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if user != nil {
		t.Fatalf("want nil user (to be created), got %v", user)
	}
	if len(pw) < 16 {
		t.Fatalf("generated password %d chars, want 16", len(pw))
	}
	if strings.Contains(out.String(), pw) {
		t.Fatalf("generated password printed in a non-interactive run: %q", out.String())
	}
	if !strings.Contains(out.String(), "not shown") || !strings.Contains(out.String(), "`jabali user password acct`") {
		t.Fatalf("operator not told how to set a password: %q", out.String())
	}
}

// At a terminal the operator sees the password once, to hand to the customer.
func TestFindOrPrepareTargetUser_InteractiveCreateShowsPassword(t *testing.T) {
	var out bytes.Buffer

	_, pw, err := findOrPrepareTargetUser(context.Background(),
		fakeTargetUsers{err: repository.ErrNotFound}, &out, true, "acct", "", false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pw == "" || !strings.Contains(out.String(), pw) {
		t.Fatalf("interactive run must show the generated password once: pw=%q out=%q", pw, out.String())
	}
}

// An operator-supplied password is used as is and never echoed.
func TestFindOrPrepareTargetUser_SuppliedPasswordUsedNotPrinted(t *testing.T) {
	supplied := strings.Repeat("s", 12)
	var out bytes.Buffer

	_, pw, err := findOrPrepareTargetUser(context.Background(),
		fakeTargetUsers{err: repository.ErrNotFound}, &out, true, "acct", supplied, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pw != supplied {
		t.Fatalf("got %q, want the supplied password", pw)
	}
	if out.Len() != 0 {
		t.Fatalf("supplied password path printed: %q", out.String())
	}
}

// A lookup failure other than not-found fails the run without minting a
// password.
func TestFindOrPrepareTargetUser_LookupErrorFails(t *testing.T) {
	var out bytes.Buffer

	user, pw, err := findOrPrepareTargetUser(context.Background(),
		fakeTargetUsers{err: errors.New("db unavailable")}, &out, false, "acct", "", false)
	if err == nil || user != nil || pw != "" {
		t.Fatalf("want an error and nothing else, got user=%v pw=%q err=%v", user, pw, err)
	}
	if out.Len() != 0 {
		t.Fatalf("output on a failed lookup: %q", out.String())
	}
}
