package api

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// GH #2056: "Website sends email" is a package entitlement that new packages
// start without (existing ones got it from migration 000317). An omitted
// field means off; an explicit true is honored.
func TestPackageCreate_WebsiteSendsEmailOffUnlessAsked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &packageHandler{cfg: PackageHandlerConfig{Repo: &mockPackageRepo{}}}

	got := createPackageForTest(t, h, `{"name":"omit"}`)
	require.Equal(t, false, got["website_sends_email"], "a new package's sites can't send mail unless asked")

	got = createPackageForTest(t, h, `{"name":"on","website_sends_email":true}`)
	require.Equal(t, true, got["website_sends_email"])
}
