package backupmetadata

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Stalwart keeps every alias it has seen on the account that had it, so a
// restored mailbox at a once-aliased address would sign in to that account.
// Apply clears the address on the mail server before it stores the mailbox,
// and refuses the mailbox when it cannot.

func allowDomains(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }

func TestApply_ReleasesAMailboxAddressBeforeStoringIt(t *testing.T) {
	_, mb, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	rel := deps.MailAddresses.(*dcReleaser)

	r := Apply(context.Background(), dcMeta(), deps)

	if mb.created != 2 || r.Mailboxes != 2 {
		t.Fatalf("mailboxes created = %d (result %d), want 2: %v", mb.created, r.Mailboxes, r.Errors)
	}
	if len(rel.released) != 2 || rel.released[0] != "info@good.org" || rel.released[1] != "info@bad.org" {
		t.Fatalf("released %v, want info@good.org and info@bad.org", rel.released)
	}
	if rel.afterWrite {
		t.Fatal("an address must be released before its mailbox row is stored")
	}
}

func TestApply_RefusesAMailboxWhoseAddressCannotBeReleased(t *testing.T) {
	for _, tc := range []struct {
		name string
		rel  AddressReleaser
		want string
	}{
		{"no mail server client", nil, "mailbox mb-good: not restored: the mail server client is not wired"},
		{"mail server error", &dcReleaser{err: errors.New("connection refused")}, "mailbox mb-good: not restored: clear info@good.org on the mail server: connection refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mb, _, deps := dcDeps()
			deps.CheckDomain = allowDomains
			deps.MailAddresses = tc.rel

			r := Apply(context.Background(), dcMeta(), deps)

			if mb.created != 0 || r.Mailboxes != 0 {
				t.Fatalf("mailboxes stored = %d, want none", mb.created)
			}
			if !hasError(r.Errors, tc.want) {
				t.Fatalf("refusal not reported as %q: %v", tc.want, r.Errors)
			}
		})
	}
}

// heldMailboxes answers the one-owner check: an alias, group or shared
// resource already has the address in domain heldDomain.
type heldMailboxes struct {
	*dcMailboxes
	heldDomain string
}

func (h *heldMailboxes) AddressHeld(_ context.Context, domainID, _ string) (bool, error) {
	return domainID == h.heldDomain, nil
}

// A mailbox the database would refuse is not restored, and its address is
// not released on the mail server: that would take a live alias off its
// account in Stalwart's registry.
func TestApply_SkipsAHeldMailboxAddressWithoutReleasingIt(t *testing.T) {
	_, mb, _, deps := dcDeps()
	deps.CheckDomain = allowDomains
	deps.Mailboxes = &heldMailboxes{dcMailboxes: mb, heldDomain: "d-good"}
	rel := deps.MailAddresses.(*dcReleaser)

	r := Apply(context.Background(), dcMeta(), deps)

	if len(rel.released) != 1 || rel.released[0] != "info@bad.org" {
		t.Fatalf("released %v, want only info@bad.org", rel.released)
	}
	if mb.created != 1 || r.Mailboxes != 1 {
		t.Fatalf("mailboxes stored = %d (result %d), want 1: %v", mb.created, r.Mailboxes, r.Errors)
	}
	if want := "mailbox mb-good: not restored: info@good.org already belongs to an alias, group or shared resource"; !hasError(r.Errors, want) {
		t.Fatalf("refusal not reported as %q: %v", want, r.Errors)
	}
}
