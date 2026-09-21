package ftpsync

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestRenderDesiredAccounts_Golden pins the shared sshd_sync projection: an
// isolated account chroots to its jail with StartDir "/<JailMountpointDir>"; a
// non-isolated account chroots to /home/<tenant> with StartDir = home_path
// relative to that home (or "/" at the home root); disabled and non-SFTP rows
// are dropped. This is the single renderer both dispatch sites route through
// (JAB-276 AC3) — mutating it must fail this AND both site tests.
//
// One tenant keeps the assertion order-deterministic (the desired slice follows
// input order within a tenant; across tenants it follows Go map iteration).
func TestRenderDesiredAccounts_Golden(t *testing.T) {
	rows := []models.FtpAccount{
		{ID: "a1", Username: "shop_printer", IsEnabled: true, SFTPAccess: true,
			Isolated: true, JailPath: "/var/lib/jabali-ftp-jails/shop/shop_printer"},
		{ID: "a2", Username: "shop_site", IsEnabled: true, SFTPAccess: true,
			HomePath: "/home/shop/site"},
		{ID: "a3", Username: "shop_root", IsEnabled: true, SFTPAccess: true,
			HomePath: "/home/shop"},
		{ID: "a4", Username: "shop_nosftp", IsEnabled: true, SFTPAccess: false,
			HomePath: "/home/shop/x"},
		{ID: "a5", Username: "shop_disabled", IsEnabled: false, SFTPAccess: true,
			HomePath: "/home/shop/y"},
	}
	got := RenderDesiredAccounts(
		map[string][]models.FtpAccount{"shop": rows},
		map[string]Eligibility{"shop": {Eligible: true, Cap: 10}},
	)

	want := []SyncAccount{
		{Username: "shop_printer", ChrootDir: "/var/lib/jabali-ftp-jails/shop/shop_printer", StartDir: "/data"},
		{Username: "shop_site", ChrootDir: "/home/shop", StartDir: "/site"},
		{Username: "shop_root", ChrootDir: "/home/shop", StartDir: "/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered desired set mismatch:\n got  %+v\n want %+v", got, want)
	}
	// Guard the mountpoint const against a drift from the panel-agent value it
	// must mirror; a change here is a cross-boundary wire break.
	if JailMountpointDir != "data" {
		t.Fatalf("JailMountpointDir = %q, want \"data\" (must match panel-agent ftp_account_jail.go)", JailMountpointDir)
	}
}

// TestRenderDesiredAccounts_IneligibleOwnerLocksAll asserts the jail renderer
// stays subordinate to the eligibility projection — an ineligible owner
// (suspended / package-dropped) renders nothing, even for enabled+SFTP rows.
func TestRenderDesiredAccounts_IneligibleOwnerLocksAll(t *testing.T) {
	rows := []models.FtpAccount{
		{ID: "a1", Username: "shop_a", IsEnabled: true, SFTPAccess: true, HomePath: "/home/shop/a"},
	}
	got := RenderDesiredAccounts(
		map[string][]models.FtpAccount{"shop": rows},
		map[string]Eligibility{"shop": {Eligible: false}},
	)
	if len(got) != 0 {
		t.Fatalf("ineligible owner must render no accounts, got %+v", got)
	}
}
