package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a backup names the account's paths under the username it had on
// the server that made it. The admin restore puts the account's files under
// THIS server's username for it, and the bundle's username is only a claim:
// paths are moved onto this server's name and checked against it.

// namedUsersRepo answers that the account already exists on this server under
// username.
type namedUsersRepo struct {
	repository.UserRepository
	username string
}

func (r namedUsersRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	u := r.username
	return &models.User{ID: id, Username: &u}, nil
}

// rhFtp keeps the FTP rows Apply creates.
type rhFtp struct {
	repository.FtpAccountRepository
	created []models.FtpAccount
}

func (r *rhFtp) Create(_ context.Context, a *models.FtpAccount) error {
	r.created = append(r.created, *a)
	return nil
}

type docRootErr struct{}

func (docRootErr) Error() string { return "document root is outside the owner's home" }

// docRootCheck stands in for RestoreDomainCheck's document-root rule: the root
// must be inside /home/<owner>. It records the owner it was given.
func docRootCheck(owners *[]string) func(context.Context, *models.Domain, string) ([]string, error) {
	return func(_ context.Context, row *models.Domain, owner string) ([]string, error) {
		*owners = append(*owners, owner)
		if !pathWithin(row.DocRoot, "/home", owner) {
			return nil, docRootErr{}
		}
		return nil, nil
	}
}

func rhMeta(bundleUser, docRoot string) *internalbackup.AccountMetadata {
	return &internalbackup.AccountMetadata{
		User:    internalbackup.MetadataUser{ID: "u1", Username: &bundleUser},
		Domains: []internalbackup.MetadataDomain{{ID: "d1", Name: "alice.org", DocRoot: docRoot}},
	}
}

// SECURITY: the bundle claims the account is "bob" and puts the site in
// /home/bob. The account is "alice" here, so the root is checked against alice
// and the site lands in alice's home, never bob's.
func TestApply_DocRootIsCheckedAgainstThisServersUsername(t *testing.T) {
	doms := &ppDomains{pools: &ppPools{}}
	var owners []string
	meta := rhMeta("bob", "/home/bob/domains/alice.org/public_html")

	Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Domains: doms, CheckDomain: docRootCheck(&owners)})

	if len(owners) != 1 || owners[0] != "alice" {
		t.Fatalf("document root checked against %v, want [alice]", owners)
	}
	if len(doms.created) != 1 || doms.created[0].DocRoot != "/home/alice/domains/alice.org/public_html" {
		t.Fatalf("domains = %+v, want alice.org rooted in /home/alice", doms.created)
	}
}

// Same username on both servers: a root in another account's home stays
// refused (only /home/<bundle user> is moved).
func TestApply_DocRootInAnotherAccountsHomeIsRefused(t *testing.T) {
	doms := &ppDomains{pools: &ppPools{}}
	var owners []string
	meta := rhMeta("alice", "/home/bob/domains/alice.org/public_html")

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Domains: doms, CheckDomain: docRootCheck(&owners)})

	if len(doms.created) != 0 {
		t.Fatalf("domains = %+v, want none", doms.created)
	}
	if !hasError(r.Errors, "outside the owner's home") {
		t.Fatalf("errors %v should carry the document root refusal", r.Errors)
	}
}

// A backup of "src" restored into "alice": its FTP home and jail move with the
// account instead of being refused.
func TestApply_FtpPathsMoveToThisServersUsername(t *testing.T) {
	ftp := &rhFtp{}
	src := "src"
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Username: &src},
		FtpAccounts: []internalbackup.MetadataFtpAccount{{
			ID: "f1", Username: "src_web", HomePath: "/home/src/site", Isolated: true, QuotaMB: 100,
			JailPath: "/var/lib/jabali-ftp-jails/src/src_web",
		}},
	}

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, FtpAccounts: ftp})

	if len(ftp.created) != 1 {
		t.Fatalf("ftp created = %+v (errors %v), want f1", ftp.created, r.Errors)
	}
	got := ftp.created[0]
	if got.HomePath != "/home/alice/site" || got.JailPath != "/var/lib/jabali-ftp-jails/alice/src_web" {
		t.Fatalf("ftp paths = %q, %q; want them under alice", got.HomePath, got.JailPath)
	}
}

// Panel usernames may start with "_" (userops' rule); such an account's FTP
// rows restore like any other.
func TestApply_FtpForAnUnderscoreUsernameIsRestored(t *testing.T) {
	ftp := &rhFtp{}
	name := "_svc"
	meta := &internalbackup.AccountMetadata{
		User:        internalbackup.MetadataUser{ID: "u1", Username: &name},
		FtpAccounts: []internalbackup.MetadataFtpAccount{{ID: "f1", Username: "_svc_web", HomePath: "/home/_svc/site"}},
	}

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: name}, FtpAccounts: ftp})

	if len(ftp.created) != 1 {
		t.Fatalf("ftp created = %+v (errors %v), want f1", ftp.created, r.Errors)
	}
}
