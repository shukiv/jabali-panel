package cpanel

import (
	"context"
	"errors"
	"testing"
)

// recordingMailReleaser stands in for the mail server: tests have none, and
// the import refuses a mailbox whose address it cannot release.
type recordingMailReleaser struct {
	released []string
	err      error
}

func (r *recordingMailReleaser) ReleaseAddress(_ context.Context, address string) error {
	r.released = append(r.released, address)
	return r.err
}

// okMailReleaser releases every address and keeps no state, so parallel
// tests can share it.
type okMailReleaser struct{}

func (okMailReleaser) ReleaseAddress(context.Context, string) error { return nil }

func init() { mailAddressReleaser = okMailReleaser{} }

func swapMailReleaser(t *testing.T, r *recordingMailReleaser) {
	t.Helper()
	prev := mailAddressReleaser
	mailAddressReleaser = r
	t.Cleanup(func() { mailAddressReleaser = prev })
}

// Stalwart keeps every alias it has seen on the account that had it, so a
// migrated mailbox at a once-aliased address would sign in to that account.
// The import clears the address first.
func TestImportMailboxes_ReleasesTheAddressOnTheMailServer(t *testing.T) {
	rel := &recordingMailReleaser{}
	swapMailReleaser(t, rel)
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeMailboxRepoMB{}

	if _, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}, false, ""); err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if len(rel.released) != 1 || rel.released[0] != "info@itflow.app" {
		t.Fatalf("released %v, want info@itflow.app", rel.released)
	}
	if len(mb.created) != 1 {
		t.Fatalf("want 1 mailbox created, got %d", len(mb.created))
	}
}

func TestImportMailboxes_SkipsAMailboxWhoseAddressCannotBeReleased(t *testing.T) {
	swapMailReleaser(t, &recordingMailReleaser{err: errors.New("connection refused")})
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeMailboxRepoMB{}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}, false, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if len(mb.created) != 0 {
		t.Fatalf("no mailbox may be created when its address was not released, got %d", len(mb.created))
	}
	if !containsSubstr(res.Skipped, "mail server could not be reached") {
		t.Fatalf("the refusal must be reported, got %v", res.Skipped)
	}
}
