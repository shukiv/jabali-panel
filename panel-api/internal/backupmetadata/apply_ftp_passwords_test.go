package backupmetadata

import (
	"context"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: the agent restores an uploaded backup's FTP passwords only into
// an account the restore created. Each other FTP subaccount the restore
// brings back gets a new password, and the report says so.
func TestApply_ReportsUploadedFtpAccountsRestoredWithoutTheirPassword(t *testing.T) {
	alice := "alice"
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Username: &alice},
		FtpAccounts: []internalbackup.MetadataFtpAccount{
			{ID: "f1", Username: "alice_web", HomePath: "/home/alice/site"},
			{ID: "f2", Username: "alice_ro", HomePath: "/home/alice/site"},
		},
	}
	const note = "restored without its password; set a new one under FTP Accounts"

	for _, c := range []struct {
		name      string
		untrusted bool
		staged    map[string]bool
		want      []string
	}{
		{"uploaded, one staged", true, map[string]bool{"alice_web": true}, []string{"alice_ro"}},
		{"uploaded, none staged", true, nil, []string{"alice_web", "alice_ro"}},
		{"own backup", false, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			ftp := &rhFtp{}
			r := Apply(context.Background(), meta, Deps{
				Users: namedUsersRepo{username: alice}, FtpAccounts: ftp,
				Untrusted: c.untrusted, FtpPasswordsStaged: c.staged,
			})
			if len(ftp.created) != 2 {
				t.Fatalf("ftp created = %+v (errors %v), want both", ftp.created, r.Errors)
			}
			var got []string
			for _, e := range r.Errors {
				if strings.Contains(e, note) {
					got = append(got, strings.TrimSuffix(strings.Fields(e)[1], ":"))
				}
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("report names %v, want %v (errors %v)", got, c.want, r.Errors)
			}
		})
	}
}
