package mailboxops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// okReleaser releases every address.
type okReleaser struct{}

func (okReleaser) ReleaseAddress(context.Context, string) error { return nil }

// recordingReleaser records each address it is asked to release, and whether
// the mailbox row already existed at that moment.
type recordingReleaser struct {
	repo       *fakeMBRepo
	addresses  []string
	afterWrite bool
	err        error
}

func (r *recordingReleaser) ReleaseAddress(_ context.Context, address string) error {
	r.addresses = append(r.addresses, address)
	if r.repo != nil && r.repo.created != nil {
		r.afterWrite = true
	}
	return r.err
}

// Stalwart keeps every alias it has seen on the account that had it, and a
// mailbox created at such an address signs in to that account. Each create
// door clears the address first, and refuses when it cannot.
func TestCreates_ReleaseTheAddressBeforeTheRowIsWritten(t *testing.T) {
	key := ssokey.Key{}
	doors := map[string]func(Deps) error{
		"Create": func(d Deps) error {
			_, _, err := Create(context.Background(), d, CreateInput{Domain: enabledDomain(), LocalPart: "Sales"}, nil)
			return err
		},
		"CreateSystem": func(d Deps) error {
			_, _, err := CreateSystem(context.Background(), d, SystemCreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil)
			return err
		},
		"CreateForRestore": func(d Deps) error {
			_, err := CreateForRestore(context.Background(), d, RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "sales", PasswordHash: "$2a$10$x"})
			return err
		},
	}
	for name, door := range doors {
		t.Run(name, func(t *testing.T) {
			repo := &fakeMBRepo{}
			rel := &recordingReleaser{repo: repo}
			if err := door(Deps{Mailboxes: repo, SSOKey: &key, Addresses: rel}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(rel.addresses) != 1 || rel.addresses[0] != "sales@example.com" {
				t.Fatalf("released %v, want the canonical sales@example.com once", rel.addresses)
			}
			if rel.afterWrite {
				t.Fatal("the address must be released before the mailbox row is written")
			}
			if repo.created == nil {
				t.Fatal("the mailbox row was not written")
			}
		})
	}
}

func TestCreates_RefuseWhenTheAddressCannotBeReleased(t *testing.T) {
	key := ssokey.Key{}
	for _, tc := range []struct {
		name string
		rel  AddressReleaser
	}{
		{"no mail server client", nil},
		{"mail server error", &recordingReleaser{err: errors.New("connection refused")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeMBRepo{}
			d := Deps{Mailboxes: repo, SSOKey: &key, Addresses: tc.rel}
			if _, _, err := Create(context.Background(), d, CreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil); !errors.Is(err, ErrMailServer) {
				t.Errorf("Create: err = %v, want ErrMailServer", err)
			}
			if _, _, err := CreateSystem(context.Background(), d, SystemCreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil); !errors.Is(err, ErrMailServer) {
				t.Errorf("CreateSystem: err = %v, want ErrMailServer", err)
			}
			if _, err := CreateForRestore(context.Background(), d, RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "sales", PasswordHash: "$2a$10$x"}); !errors.Is(err, ErrMailServer) {
				t.Errorf("CreateForRestore: err = %v, want ErrMailServer", err)
			}
			if repo.created != nil {
				t.Fatalf("no row may be written when the address was not released, got %+v", repo.created)
			}
		})
	}
}

// Without the domain name the restore cannot say which address to release.
func TestCreateForRestore_RequiresTheDomainName(t *testing.T) {
	repo := &fakeMBRepo{}
	_, err := CreateForRestore(context.Background(), Deps{Mailboxes: repo, Addresses: okReleaser{}},
		RestoreCreateInput{DomainID: "d1", LocalPart: "sales", PasswordHash: "$2a$10$x"})
	if !errors.Is(err, ErrDeps) || repo.created != nil {
		t.Fatalf("err = %v, created = %+v; want ErrDeps and no row", err, repo.created)
	}
}

// The database refuses a mailbox at an alias's, group's or shared resource's
// address (migration 000306); each door reports it as ErrAddressInUse.
func TestCreates_MapTheDatabaseRefusal(t *testing.T) {
	key := ssokey.Key{}
	d := Deps{Mailboxes: &fakeMBRepo{createErr: repository.ErrAddressInUse}, SSOKey: &key, Addresses: okReleaser{}}
	if _, _, err := Create(context.Background(), d, CreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil); !errors.Is(err, ErrAddressInUse) {
		t.Errorf("Create: err = %v, want ErrAddressInUse", err)
	}
	if _, _, err := CreateSystem(context.Background(), d, SystemCreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil); !errors.Is(err, ErrAddressInUse) {
		t.Errorf("CreateSystem: err = %v, want ErrAddressInUse", err)
	}
	if _, err := CreateForRestore(context.Background(), d, RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "sales", PasswordHash: "$2a$10$x"}); !errors.Is(err, ErrAddressInUse) {
		t.Errorf("CreateForRestore: err = %v, want ErrAddressInUse", err)
	}
}

// heldMBRepo is a mailbox repository that answers the one-owner check: held
// says an alias, group or shared resource already has the address.
type heldMBRepo struct {
	*fakeMBRepo
	held  bool
	err   error
	asked []string
}

func (h *heldMBRepo) AddressHeld(_ context.Context, domainID, localPart string) (bool, error) {
	h.asked = append(h.asked, domainID+"/"+localPart)
	return h.held, h.err
}

// createDoors runs each create door for sales@example.com.
func createDoors() map[string]func(Deps) error {
	return map[string]func(Deps) error{
		"Create": func(d Deps) error {
			_, _, err := Create(context.Background(), d, CreateInput{Domain: enabledDomain(), LocalPart: "Sales"}, nil)
			return err
		},
		"CreateSystem": func(d Deps) error {
			_, _, err := CreateSystem(context.Background(), d, SystemCreateInput{Domain: enabledDomain(), LocalPart: "sales"}, nil)
			return err
		},
		"CreateForRestore": func(d Deps) error {
			_, err := CreateForRestore(context.Background(), d, RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "sales", PasswordHash: "$2a$10$x"})
			return err
		},
	}
}

// A create the database would refuse must not reach the mail server. Clearing
// the address there first took a live alias off its account in Stalwart's
// registry (test box, 2026-09-30: a refused mailbox at a live alias's address
// emptied the alias owner's alias list).
func TestCreates_RefuseAHeldAddressBeforeTheMailServer(t *testing.T) {
	key := ssokey.Key{}
	for name, door := range createDoors() {
		t.Run(name, func(t *testing.T) {
			repo := &heldMBRepo{fakeMBRepo: &fakeMBRepo{}, held: true}
			rel := &recordingReleaser{}
			if err := door(Deps{Mailboxes: repo, SSOKey: &key, Addresses: rel}); !errors.Is(err, ErrAddressInUse) {
				t.Fatalf("err = %v, want ErrAddressInUse", err)
			}
			if len(rel.addresses) != 0 {
				t.Fatalf("released %v on the mail server; a held address must not be released", rel.addresses)
			}
			if len(repo.asked) != 1 || repo.asked[0] != "d1/sales" {
				t.Fatalf("asked %v, want the canonical d1/sales once", repo.asked)
			}
			if repo.created != nil {
				t.Fatalf("no row may be written, got %+v", repo.created)
			}
		})
	}
}

// A free address is still released before the row is written.
func TestCreates_ReleaseAFreeAddressAfterTheCheck(t *testing.T) {
	key := ssokey.Key{}
	for name, door := range createDoors() {
		t.Run(name, func(t *testing.T) {
			repo := &heldMBRepo{fakeMBRepo: &fakeMBRepo{}}
			rel := &recordingReleaser{repo: repo.fakeMBRepo}
			if err := door(Deps{Mailboxes: repo, SSOKey: &key, Addresses: rel}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(repo.asked) != 1 || len(rel.addresses) != 1 || rel.addresses[0] != "sales@example.com" || rel.afterWrite {
				t.Fatalf("asked %v, released %v (after write %v); want one check, then one release before the row", repo.asked, rel.addresses, rel.afterWrite)
			}
			if repo.created == nil {
				t.Fatal("the mailbox row was not written")
			}
		})
	}
}

// When the check itself fails the create stops, fail closed, before the mail
// server.
func TestCreates_RefuseWhenTheAddressCheckFails(t *testing.T) {
	key := ssokey.Key{}
	for name, door := range createDoors() {
		t.Run(name, func(t *testing.T) {
			repo := &heldMBRepo{fakeMBRepo: &fakeMBRepo{}, err: errors.New("db down")}
			rel := &recordingReleaser{}
			if err := door(Deps{Mailboxes: repo, SSOKey: &key, Addresses: rel}); !errors.Is(err, ErrInternal) {
				t.Fatalf("err = %v, want ErrInternal", err)
			}
			if len(rel.addresses) != 0 || repo.created != nil {
				t.Fatalf("released %v, created %+v; want neither", rel.addresses, repo.created)
			}
		})
	}
}
