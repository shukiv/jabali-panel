package ftpsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func ftpRow(id, user string, enabled bool, created time.Time) models.FtpAccount {
	return models.FtpAccount{ID: id, UserID: "u1", Username: user, IsEnabled: enabled, FTPAccess: true, SFTPAccess: true, CreatedAt: created}
}

// JAB-254/258: an ineligible owner (suspended, or package dropped the
// feature) forces every alias effective-disabled regardless of its row.
func TestEffectiveEnabled_IneligibleLocksAll(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_one", true, base),
		ftpRow("b", "shop_two", true, base.Add(time.Minute)),
	}
	eff := EffectiveEnabled(rows, Eligibility{Eligible: false})
	for _, a := range rows {
		if eff[a.Username] {
			t.Fatalf("%s enabled under ineligible owner", a.Username)
		}
	}
}

// JAB-258: an over-cap owner keeps only its OLDEST cap enabled accounts.
func TestEffectiveEnabled_OverCapLocksNewest(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_old", true, base),
		ftpRow("b", "shop_mid", true, base.Add(time.Hour)),
		ftpRow("c", "shop_new", true, base.Add(2*time.Hour)),
	}
	eff := EffectiveEnabled(rows, Eligibility{Eligible: true, Cap: 2})
	if !eff["shop_old"] || !eff["shop_mid"] {
		t.Fatalf("oldest two should stay enabled: %+v", eff)
	}
	if eff["shop_new"] {
		t.Fatalf("newest (over-cap) should be disabled: %+v", eff)
	}
}

// Eligible owner within cap: row's own IsEnabled is respected.
func TestEffectiveEnabled_EligibleRespectsRow(t *testing.T) {
	base := time.Now()
	rows := []models.FtpAccount{
		ftpRow("a", "shop_on", true, base),
		ftpRow("b", "shop_off", false, base.Add(time.Minute)),
	}
	eff := EffectiveEnabled(rows, Eligibility{Eligible: true, Cap: 10})
	if !eff["shop_on"] || eff["shop_off"] {
		t.Fatalf("row flags not respected: %+v", eff)
	}
}

type fakePkgGetter struct {
	pkg *models.HostingPackage
	err error
}

func (f *fakePkgGetter) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, f.err
}

func strptr(s string) *string { return &s }

// OwnerEligibility is the fail-closed clamp: every ineligible reason must
// return Eligible=false so a same-uid credential is never re-emitted. These
// had no direct unit coverage before the extraction (only reconciler
// integration tests) — JAB-276 adds it at the shared function's own layer.
func TestOwnerEligibility_FailClosed(t *testing.T) {
	ctx := context.Background()
	pid := "pkg-1"
	okPkg := &fakePkgGetter{pkg: &models.HostingPackage{ID: pid, MaxFTPAccounts: 3}}

	cases := []struct {
		name string
		user *models.User
		pkgs PackageGetter
		want Eligibility
	}{
		{"nil user", nil, okPkg, Eligibility{}},
		{"suspended", &models.User{PackageID: &pid, Suspended: true}, okPkg, Eligibility{}},
		{"nil pkgs", &models.User{PackageID: &pid}, nil, Eligibility{}},
		{"no package id", &models.User{PackageID: nil}, okPkg, Eligibility{}},
		{"empty package id", &models.User{PackageID: strptr("")}, okPkg, Eligibility{}},
		{"lookup error", &models.User{PackageID: &pid}, &fakePkgGetter{err: errors.New("db down")}, Eligibility{}},
		{"package missing", &models.User{PackageID: &pid}, &fakePkgGetter{pkg: nil}, Eligibility{}},
		{"zero cap package", &models.User{PackageID: &pid}, &fakePkgGetter{pkg: &models.HostingPackage{ID: pid, MaxFTPAccounts: 0}}, Eligibility{}},
		{"eligible", &models.User{PackageID: &pid}, okPkg, Eligibility{Eligible: true, Cap: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OwnerEligibility(ctx, tc.pkgs, tc.user)
			if got != tc.want {
				t.Fatalf("OwnerEligibility(%s) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}
