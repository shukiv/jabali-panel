package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_create_quota_test.go — JAB-279. Characterizes the create door's
// package domain quota: who is limited, how the count compares to the limit,
// what a store failure does, and where the check sits in the sequence.

// quotaDomains overrides the fake store's count.
type quotaDomains struct {
	*dcDomains
	count    int64
	countErr error
	counted  []string
}

func (r *quotaDomains) CountByUserID(_ context.Context, userID string) (int64, error) {
	r.counted = append(r.counted, userID)
	return r.count, r.countErr
}

// errPackages is a PackageRepository whose lookup always fails with a store error.
type errPackages struct {
	repository.PackageRepository
}

func (errPackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return nil, errors.New("hosting_packages: connection refused")
}

func TestCreateDomainOp_DomainQuota(t *testing.T) {
	const pkgID = "pkg-2"
	packages := &abPackages{rows: map[string]*models.HostingPackage{
		pkgID:       {ID: pkgID, Name: "Two", MaxDomains: 2},
		"pkg-unlim": {ID: "pkg-unlim", Name: "Unlimited", MaxDomains: 0},
	}}
	owner := func(pkg *string) *models.User {
		uname := "alice"
		return &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname, PackageID: pkg}
	}
	strp := func(s string) *string { return &s }

	cases := []struct {
		name        string
		pkg         *string
		packages    repository.PackageRepository
		count       int64
		countErr    error
		wantCode    string // "" = created
		wantStatus  int
		wantCounted bool
	}{
		{name: "at the limit", pkg: strp(pkgID), count: 2,
			wantCode: "domain_quota_exceeded", wantStatus: http.StatusConflict, wantCounted: true},
		{name: "over the limit", pkg: strp(pkgID), count: 3,
			wantCode: "domain_quota_exceeded", wantStatus: http.StatusConflict, wantCounted: true},
		{name: "under the limit", pkg: strp(pkgID), count: 1, wantCounted: true},
		{name: "package with no domain limit", pkg: strp("pkg-unlim"), count: 50, wantCounted: true},
		{name: "no package is unrestricted (GH #282)", pkg: nil, count: 50},
		{name: "empty package id is no package", pkg: strp(""), count: 50},
		{name: "count failure", pkg: strp(pkgID), countErr: errors.New("domains: connection refused"),
			wantCode: "internal", wantStatus: http.StatusInternalServerError, wantCounted: true},
		// Current behavior, shared with the database-user quota: a package that
		// cannot be read does not block the create.
		{name: "unreadable package", pkg: strp(pkgID), packages: errPackages{}, count: 5, wantCounted: true},
		{name: "dangling package id", pkg: strp("pkg-gone"), count: 5, wantCounted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dom := &quotaDomains{dcDomains: newDCDomains(), count: tc.count, countErr: tc.countErr}
			pk := tc.packages
			if pk == nil {
				pk = packages
			}
			o := owner(tc.pkg)
			h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(o), Domains: dom, Packages: pk}}
			d, oerr := createDomainOp(context.Background(), h, createDomainInput{
				OwnerID: o.ID, Name: "shop.example.com", SkipInlineSSL: true,
			})
			if (len(dom.counted) > 0) != tc.wantCounted {
				t.Fatalf("counted = %v, want counted=%v", dom.counted, tc.wantCounted)
			}
			if tc.wantCounted && dom.counted[0] != o.ID {
				t.Fatalf("counted owner %q, want %q", dom.counted[0], o.ID)
			}
			if tc.wantCode == "" {
				if oerr != nil {
					t.Fatalf("want created, got %+v", oerr)
				}
				if d == nil || len(dom.created) != 1 {
					t.Fatalf("want one persisted row, got %d", len(dom.created))
				}
				return
			}
			if oerr == nil || oerr.Code != tc.wantCode || oerr.Status != tc.wantStatus || oerr.Detail != "" {
				t.Fatalf("got %+v, want {%d %q \"\"}", oerr, tc.wantStatus, tc.wantCode)
			}
			if len(dom.created) != 0 {
				t.Fatalf("a rejected create must persist nothing, got %d rows", len(dom.created))
			}
		})
	}

	t.Run("owner eligibility runs before the quota", func(t *testing.T) {
		o := owner(strp(pkgID))
		o.Suspended = true
		dom := &quotaDomains{dcDomains: newDCDomains(), count: 9}
		h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(o), Domains: dom, Packages: packages}}
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{OwnerID: o.ID, Name: "shop.example.com"})
		if oerr == nil || oerr.Code != "user_suspended" {
			t.Fatalf("want user_suspended, got %+v", oerr)
		}
		if len(dom.counted) != 0 {
			t.Fatal("a suspended owner must be rejected before the quota count")
		}
	})

	t.Run("the quota runs before the mail posture", func(t *testing.T) {
		o := owner(strp(pkgID))
		dom := &quotaDomains{dcDomains: newDCDomains(), count: 2}
		h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(o), Domains: dom, Packages: packages}}
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{OwnerID: o.ID, Name: "shop.example.com", MailProvider: "exchange"})
		if oerr == nil || oerr.Code != "domain_quota_exceeded" {
			t.Fatalf("want domain_quota_exceeded, got %+v", oerr)
		}
	})
}
