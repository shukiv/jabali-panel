package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// targetUserFinder is the one lookup findOrPrepareTargetUser needs
// (repository.UserRepository satisfies it).
type targetUserFinder interface {
	FindByUsername(ctx context.Context, username string) (*models.User, error)
}

// findOrPrepareTargetUser looks up a migration's destination user.
//
// An existing user is returned as is: no password is generated or printed for
// it, since it keeps the one it has. Only when the user does not exist is the
// password to create it with resolved: the operator's (flag or
// JABALI_TARGET_PASSWORD) when given, else a generated one (newTargetPassword).
// It then returns a nil user and that password. A lookup failure other than
// not-found is returned as an error.
func findOrPrepareTargetUser(
	ctx context.Context,
	users targetUserFinder,
	out io.Writer,
	interactive bool,
	username, suppliedPassword string,
	sourceHadHash bool,
) (*models.User, string, error) {
	user, err := users.FindByUsername(ctx, username)
	if err == nil {
		return user, "", nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, "", fmt.Errorf("destination user %q lookup: %w", username, err)
	}
	if suppliedPassword != "" {
		return nil, suppliedPassword, nil
	}
	pw, err := newTargetPassword(out, interactive, username, sourceHadHash)
	if err != nil {
		return nil, "", fmt.Errorf("generate a password for destination user %q: %w", username, err)
	}
	return nil, pw, nil
}

// newTargetPassword generates the password a destination user is created with
// when the operator gave none, and says so on out.
//
// The password itself is printed only when interactive is true (a person at a
// terminal, who can hand it to the customer). A non-interactive run — the
// import unit that pull-source dispatches, a script, a redirect to a file —
// writes its stdout to the journal or a log, where a working password must not
// land; the operator is told how to set one instead. The password is never
// stored.
func newTargetPassword(out io.Writer, interactive bool, username string, sourceHadHash bool) (string, error) {
	pw, err := randomPassword(16)
	if err != nil {
		return "", err
	}
	if interactive {
		fmt.Fprintf(out, "  → target-password auto-generated: %s   (share with customer; reset via Kratos when needed)\n", pw)
	} else {
		fmt.Fprintf(out, "  → target-password generated and not shown (non-interactive run); set one with `jabali user password %s`\n", username)
	}
	if sourceHadHash {
		fmt.Fprintf(out, "    (source had a crypt(3) hash but Kratos uses Argon2; original password not recoverable)\n")
	}
	return pw, nil
}
