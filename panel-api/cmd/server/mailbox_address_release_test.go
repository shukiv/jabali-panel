package main

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
)

// cliTestReleaser stands in for the mail server: tests have none, and a
// mailbox create refuses when it cannot release the address.
type cliTestReleaser struct {
	released []string
	err      error
}

func (r *cliTestReleaser) ReleaseAddress(_ context.Context, address string) error {
	r.released = append(r.released, address)
	return r.err
}

func (r *cliTestReleaser) ReleaseTo(_ context.Context, address, _ string) error {
	r.released = append(r.released, address)
	return r.err
}

// okCLIReleaser releases every address and keeps no state, so the parallel
// tests can share it.
type okCLIReleaser struct{}

func (okCLIReleaser) ReleaseAddress(context.Context, string) error    { return nil }
func (okCLIReleaser) ReleaseTo(context.Context, string, string) error { return nil }

// Set before any test runs; the parallel tests only read it.
func init() { cliMailAddresses = okCLIReleaser{} }

// Stalwart keeps every alias it has seen on the account that had it, so a CLI
// mailbox at a once-aliased address would sign in to that account. The create
// clears the address first and refuses when it cannot. Not parallel: it swaps
// the package releaser, and parallel tests wait for sequential ones.
func TestCreateMailbox_ReleasesTheAddressFirst(t *testing.T) {
	prev := cliMailAddresses
	t.Cleanup(func() { cliMailAddresses = prev })

	rel := &cliTestReleaser{}
	cliMailAddresses = rel
	repo := newFakeMailboxRepo()
	if _, _, err := createMailboxDirect(context.Background(), repo, nil, nil,
		testDomain("dom1", "example.com", true), "Sales", "", 0, "", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(rel.released) != 1 || rel.released[0] != "sales@example.com" {
		t.Fatalf("released %v, want sales@example.com", rel.released)
	}

	cliMailAddresses = &cliTestReleaser{err: errors.New("connection refused")}
	repo = newFakeMailboxRepo()
	_, _, err := createMailboxDirect(context.Background(), repo, nil, nil,
		testDomain("dom1", "example.com", true), "sales", "", 0, "", false)
	if !errors.Is(err, mailboxops.ErrMailServer) {
		t.Fatalf("err = %v, want ErrMailServer", err)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("no row may be written when the address was not released, got %d", len(repo.rows))
	}
}
