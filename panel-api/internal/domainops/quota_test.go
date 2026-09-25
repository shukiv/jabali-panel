package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeCounter struct {
	count int64
	err   error
	ids   []string
}

func (f *fakeCounter) CountByUserID(_ context.Context, userID string) (int64, error) {
	f.ids = append(f.ids, userID)
	return f.count, f.err
}

type fakePackages struct {
	rows  map[string]*models.HostingPackage
	err   error
	calls int
}

func (f *fakePackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if p, ok := f.rows[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

func TestCheckDomainQuota(t *testing.T) {
	strp := func(s string) *string { return &s }
	owner := func(pkg *string) *models.User { return &models.User{ID: "u1", PackageID: pkg} }
	rows := map[string]*models.HostingPackage{
		"two":   {ID: "two", MaxDomains: 2},
		"unlim": {ID: "unlim", MaxDomains: 0},
	}

	cases := []struct {
		name        string
		owner       *models.User
		count       int64
		countErr    error
		pkgErr      error
		want        error
		wantCounted bool
	}{
		{name: "no package", owner: owner(nil), count: 9},
		{name: "empty package id", owner: owner(strp("")), count: 9},
		{name: "under the limit", owner: owner(strp("two")), count: 1, wantCounted: true},
		{name: "at the limit", owner: owner(strp("two")), count: 2, want: ErrDomainQuotaExceeded, wantCounted: true},
		{name: "over the limit", owner: owner(strp("two")), count: 5, want: ErrDomainQuotaExceeded, wantCounted: true},
		{name: "no domain limit", owner: owner(strp("unlim")), count: 500, wantCounted: true},
		{name: "unreadable package does not block", owner: owner(strp("two")), count: 5, pkgErr: errors.New("db down"), wantCounted: true},
		{name: "dangling package does not block", owner: owner(strp("gone")), count: 5, wantCounted: true},
		{name: "count failure blocks", owner: owner(strp("two")), countErr: errors.New("db down"), want: ErrDomainCount, wantCounted: true},
		{name: "nil owner", owner: nil, want: ErrOwnerNil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter := &fakeCounter{count: tc.count, err: tc.countErr}
			pkgs := &fakePackages{rows: rows, err: tc.pkgErr}
			err := CheckDomainQuota(context.Background(), QuotaDeps{Domains: counter, Packages: pkgs}, tc.owner)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if (len(counter.ids) > 0) != tc.wantCounted {
				t.Fatalf("counted %v, want counted=%v", counter.ids, tc.wantCounted)
			}
			if tc.wantCounted && counter.ids[0] != "u1" {
				t.Fatalf("counted owner %q, want u1", counter.ids[0])
			}
		})
	}

	t.Run("an exceeded quota carries the count and the limit", func(t *testing.T) {
		err := CheckDomainQuota(context.Background(), QuotaDeps{
			Domains:  &fakeCounter{count: 3},
			Packages: &fakePackages{rows: rows},
		}, owner(strp("two")))
		var q *DomainQuotaError
		if !errors.As(err, &q) || q.Count != 3 || q.Max != 2 {
			t.Fatalf("err = %v, want *DomainQuotaError{3, 2}", err)
		}
	})

	t.Run("a count failure carries the store error without a package prefix", func(t *testing.T) {
		cause := errors.New("domains: connection refused")
		err := CheckDomainQuota(context.Background(), QuotaDeps{
			Domains:  &fakeCounter{err: cause},
			Packages: &fakePackages{rows: rows},
		}, owner(strp("two")))
		if !errors.Is(err, ErrDomainCount) || !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatalf("err = %v, want the store error tagged ErrDomainCount", err)
		}
	})

	t.Run("the count runs before the package lookup", func(t *testing.T) {
		pkgs := &fakePackages{rows: rows}
		_ = CheckDomainQuota(context.Background(), QuotaDeps{
			Domains:  &fakeCounter{err: errors.New("db down")},
			Packages: pkgs,
		}, owner(strp("two")))
		if pkgs.calls != 0 {
			t.Fatal("a failed count must stop before the package lookup")
		}
	})

	t.Run("unwired dependencies fail closed", func(t *testing.T) {
		err := CheckDomainQuota(context.Background(), QuotaDeps{}, owner(strp("two")))
		if !errors.Is(err, ErrQuotaDeps) {
			t.Fatalf("err = %v, want ErrQuotaDeps", err)
		}
		if err := CheckDomainQuota(context.Background(), QuotaDeps{}, owner(nil)); err != nil {
			t.Fatalf("an owner with no package needs no dependencies, got %v", err)
		}
	})
}
