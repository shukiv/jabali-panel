package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func ftpRow(id, user string, enabled bool, created time.Time) models.FtpAccount {
	return models.FtpAccount{ID: id, UserID: "u1", Username: user, IsEnabled: enabled, FTPAccess: true, SFTPAccess: true, CreatedAt: created}
}

// JAB-254/258: an ineligible owner (suspended, or package dropped the
// feature) forces every alias effective-disabled regardless of its row.
func TestFtpEffectiveEnabled_IneligibleLocksAll(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_one", true, base),
		ftpRow("b", "shop_two", true, base.Add(time.Minute)),
	}
	eff := ftpEffectiveEnabled(rows, ftpOwnerEligibility{eligible: false})
	for _, a := range rows {
		if eff[a.Username] {
			t.Fatalf("%s enabled under ineligible owner", a.Username)
		}
	}
}

// JAB-258: an over-cap owner keeps only its OLDEST `cap` enabled accounts.
func TestFtpEffectiveEnabled_OverCapLocksNewest(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_old", true, base),
		ftpRow("b", "shop_mid", true, base.Add(time.Hour)),
		ftpRow("c", "shop_new", true, base.Add(2*time.Hour)),
	}
	eff := ftpEffectiveEnabled(rows, ftpOwnerEligibility{eligible: true, cap: 2})
	if !eff["shop_old"] || !eff["shop_mid"] {
		t.Fatalf("oldest two should stay enabled: %+v", eff)
	}
	if eff["shop_new"] {
		t.Fatalf("newest (over-cap) should be disabled: %+v", eff)
	}
}

// Eligible owner within cap: row's own IsEnabled is respected.
func TestFtpEffectiveEnabled_EligibleRespectsRow(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_on", true, base),
		ftpRow("b", "shop_off", false, base.Add(time.Minute)),
	}
	eff := ftpEffectiveEnabled(rows, ftpOwnerEligibility{eligible: true, cap: 10})
	if !eff["shop_on"] || eff["shop_off"] {
		t.Fatalf("row flags not respected: %+v", eff)
	}
}

// JAB-254 flow: a suspended owner's live+unlocked host alias is driven to
// locked + FTP-degrouped by the reconcile pass.
func TestReconcileFtp_SuspendedOwnerLocksAlias(t *testing.T) {
	rows := []models.FtpAccount{ftpRow("a", "shop_dev", true, time.Now())}
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			{Username: "shop_dev", FTPAccess: true, Locked: false}, // live on host
		}),
		"ftpaccount.list_all":   hostListAllResult(t, []agentFtpListEntry{{Username: "shop_dev"}}),
		"ftpaccount.set_access": json.RawMessage(`{}`),
		"ftpaccount.sshd_sync":  json.RawMessage(`{}`),
		"ssh.user.home_chown":   json.RawMessage(`{}`),
	}}
	// Suspended owner + eligible package — suspension must still win.
	uname := "shop"
	pid := "pkg-ftp"
	r := New(nil, &ftpUsersRepo{byID: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pid, Suspended: true},
	}}, agent, slog.Default(), Config{})
	r.WithFtpAccounts(&fakeFtpAccountRepo{rows: rows})
	r.WithPackages(&ftpPkgRepo{pkg: &models.HostingPackage{ID: "pkg-ftp", MaxFTPAccounts: 100}})

	r.reconcileFtpAccounts(context.Background())

	calls := ftpCallsByMethod(agent)
	sa := calls["ftpaccount.set_access"]
	if len(sa) != 1 {
		t.Fatalf("expected 1 set_access to lock the alias, got %d", len(sa))
	}
	p := sa[0].params.(map[string]any)
	if p["enabled"] != false || p["ftp_access"] != false {
		t.Fatalf("suspended owner's alias not fully locked+degrouped: %+v", p)
	}
	// A suspended owner's SFTP account must be dropped from the sshd sync.
	sync := calls["ftpaccount.sshd_sync"]
	if len(sync) != 1 {
		t.Fatalf("expected sshd_sync call")
	}
	sp := sync[0].params.(map[string]any)
	accts, _ := sp["accounts"].([]any)
	if len(accts) != 0 {
		t.Fatalf("suspended owner's SFTP alias still in sshd sync: %+v", accts)
	}
}

// JAB-258 flow: package with zero FTP cap locks all aliases.
func TestReconcileFtp_ZeroCapLocksAlias(t *testing.T) {
	rows := []models.FtpAccount{ftpRow("a", "shop_dev", true, time.Now())}
	agent := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"ftpaccount.list": hostListResult(t, []agentFtpListEntry{
			{Username: "shop_dev", FTPAccess: true, Locked: false},
		}),
		"ftpaccount.list_all":   hostListAllResult(t, []agentFtpListEntry{{Username: "shop_dev"}}),
		"ftpaccount.set_access": json.RawMessage(`{}`),
		"ftpaccount.sshd_sync":  json.RawMessage(`{}`),
		"ssh.user.home_chown":   json.RawMessage(`{}`),
	}}
	uname := "shop"
	pid := "pkg-ftp"
	r := New(nil, &ftpUsersRepo{byID: map[string]*models.User{
		"u1": {ID: "u1", Username: &uname, PackageID: &pid},
	}}, agent, slog.Default(), Config{})
	r.WithFtpAccounts(&fakeFtpAccountRepo{rows: rows})
	r.WithPackages(&ftpPkgRepo{pkg: &models.HostingPackage{ID: "pkg-ftp", MaxFTPAccounts: 0}})

	r.reconcileFtpAccounts(context.Background())

	sa := ftpCallsByMethod(agent)["ftpaccount.set_access"]
	if len(sa) != 1 || sa[0].params.(map[string]any)["enabled"] != false {
		t.Fatalf("zero-cap package did not lock the alias: %+v", sa)
	}
}
