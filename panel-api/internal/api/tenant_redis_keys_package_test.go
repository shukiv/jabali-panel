package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// erroringPackageRepo fails every package lookup.
type erroringPackageRepo struct{ mockPackageRepo }

func (*erroringPackageRepo) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return nil, errors.New("db down")
}

// bobOnPackage is bobKeysConfig with bob on package p1, which allows perCall
// keys a call (packageID "" = no package).
func bobOnPackage(cfg ApplicationHandlerConfig, packageID string, perCall int) ApplicationHandlerConfig {
	u := &models.User{ID: "u1", Username: ptr("bob")}
	if packageID != "" {
		u.PackageID = ptr(packageID)
	}
	cfg.Users = &redisAccessStubUsers{user: u}
	cfg.Packages = &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"p1": {ID: "p1", Name: "Big", RedisKeysPerCall: perCall},
	}}
	return cfg
}

// The hosting package sets how many keys one listing call may ask for and one
// delete call may name (GH #2165). The listing says what the caller's maximum is.
func TestRedisKeys_PerPackageMaximum(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	seedTenantKeys(t, mr)

	for _, tc := range []struct {
		name      string
		packageID string
		perCall   int
		packages  func(ApplicationHandlerConfig) ApplicationHandlerConfig
		want      int
	}{
		{name: "the package's value", packageID: "p1", perCall: 2000, want: 2000},
		{name: "the package's lowest value", packageID: "p1", perCall: 100, want: 100},
		{name: "the package's highest value", packageID: "p1", perCall: 5000, want: 5000},
		{name: "no package: the default", want: 500},
		{name: "a package that doesn't exist: the default", packageID: "gone", perCall: 2000, want: 500},
		{name: "a stored 0: the default, never unbounded", packageID: "p1", perCall: 0, want: 500},
		{name: "a stored value over the ceiling: the default", packageID: "p1", perCall: 99999, want: 500},
		{name: "a stored value under the floor: the default", packageID: "p1", perCall: 99, want: 500},
		{name: "the package lookup fails: the default", packageID: "p1", perCall: 2000, want: 500,
			packages: func(cfg ApplicationHandlerConfig) ApplicationHandlerConfig {
				cfg.Packages = &erroringPackageRepo{}
				return cfg
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := bobOnPackage(bobKeysConfig(rdb), tc.packageID, tc.perCall)
			if tc.packages != nil {
				cfg = tc.packages(cfg)
			}
			r := newRedisAccessRouter(t, cfg, &auth.AccessClaims{UserID: "u1"})

			rec, got := getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"limit": {fmt.Sprint(tc.want)}})
			if rec.Code != http.StatusOK {
				t.Fatalf("limit=%d: want 200, got %d %s", tc.want, rec.Code, rec.Body.String())
			}
			if got.MaxLimit != tc.want {
				t.Errorf("max_limit = %d, want %d", got.MaxLimit, tc.want)
			}
			rec, _ = getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"limit": {fmt.Sprint(tc.want + 1)}})
			wantDetail := fmt.Sprintf("limit is 1 to %d", tc.want)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), wantDetail) {
				t.Errorf("limit=%d: want 422 %q, got %d %s", tc.want+1, wantDetail, rec.Code, rec.Body.String())
			}

			keys := func(n int) string {
				k := make([]string, n)
				for i := range k {
					k[i] = fmt.Sprintf(`{"db":0,"key":"jt:bob:k%d"}`, i)
				}
				return `{"keys":[` + strings.Join(k, ",") + `]}`
			}
			if rec := postKeysDelete(r, "/api/v1/me/redis-access/keys/delete", keys(tc.want)); rec.Code != http.StatusOK {
				t.Errorf("delete of %d keys: want 200, got %d %s", tc.want, rec.Code, rec.Body.String())
			}
			rec = postKeysDelete(r, "/api/v1/me/redis-access/keys/delete", keys(tc.want+1))
			wantDetail = fmt.Sprintf("send 1 to %d keys", tc.want)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), wantDetail) {
				t.Errorf("delete of %d keys: want 422 %q, got %d %s", tc.want+1, wantDetail, rec.Code, rec.Body.String())
			}
		})
	}
}

// A call that doesn't say how many keys it wants never looks for more than
// the package allows, whatever the default page is.
func TestParseRedisKeysQuery_DefaultWithinMaximum(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, maxLimit := range []int{models.RedisKeysPerCallMin, models.RedisKeysPerCallDefault, models.RedisKeysPerCallMax} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/me/redis-access/keys", nil)
		q, code, detail := parseRedisKeysQuery(c, maxLimit)
		if code != "" {
			t.Fatalf("no limit at %d: want ok, got %s %s", maxLimit, code, detail)
		}
		if q.limit < 1 || q.limit > maxLimit {
			t.Errorf("no limit at %d: looks for %d keys", maxLimit, q.limit)
		}
	}
}

// The admin view of a user's keys uses that user's package.
func TestRedisKeys_AdminRouteUsesTheUsersPackage(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	r := newRedisAccessRouter(t, bobOnPackage(bobKeysConfig(rdb), "p1", 2000), &auth.AccessClaims{UserID: "a1", IsAdmin: true})

	rec, got := getKeys(t, r, "/api/v1/users/u1/redis-access/keys", url.Values{"limit": {"2000"}})
	if rec.Code != http.StatusOK || got.MaxLimit != 2000 {
		t.Fatalf("admin listing of bob: want 200 max_limit 2000, got %d %+v", rec.Code, got.MaxLimit)
	}
}

// A refused query provisions no ACL.
func TestRedisKeys_InvalidLimitProvisionsNothing(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubKeySeams(t, mr)
	provisioned := 0
	tenantRedisProvision = func(*redisAccessHandler, context.Context, string, string) error {
		provisioned++
		return nil
	}
	r := newRedisAccessRouter(t, bobOnPackage(bobKeysConfig(rdb), "p1", 2000), &auth.AccessClaims{UserID: "u1"})

	if rec, _ := getKeys(t, r, "/api/v1/me/redis-access/keys", url.Values{"limit": {"2001"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=2001: want 422, got %d", rec.Code)
	}
	if rec := postKeysDelete(r, "/api/v1/me/redis-access/keys/delete", `{"keys":[]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty delete: want 422, got %d", rec.Code)
	}
	if provisioned != 0 {
		t.Fatalf("a refused request provisioned the ACL %d times", provisioned)
	}
}
