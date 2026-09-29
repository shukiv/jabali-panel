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
