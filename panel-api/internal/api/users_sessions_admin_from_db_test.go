package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-5: the Active Sessions list must resolve the admin flag from the panel
// DB, never from the Kratos is_admin trait (which must not be trusted). Here a
// session's identity trait claims is_admin=true, but the matching panel user is
// a NON-admin — the response row must report is_admin=false (DB wins). This is
// defence-in-depth alongside disabling the self-service profile method that
// could let the trait drift in the first place.
func TestListSessions_ResolvesAdminFromPanelDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kratos admin: GET /admin/sessions → one session whose trait lies.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id":"sess-1","active":true,
			"authenticator_assurance_level":"aal1",
			"identity":{"id":"idA","traits":{"email":"user@x.test","username":"alice","is_admin":true}},
			"devices":[{"ip_address":"203.0.113.7","user_agent":"curl"}]
		}]`))
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	repo.seed(&models.User{ID: "u1", Email: "user@x.test", IsAdmin: false})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin-1", IsAdmin: true})
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:         repo,
		KratosClient: kratosclient.NewClient(srv.URL, srv.URL),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Sessions []struct {
			Email   string `json:"email"`
			IsAdmin bool   `json:"is_admin"`
		} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Sessions, 1)
	require.Equal(t, "user@x.test", body.Sessions[0].Email)
	require.False(t, body.Sessions[0].IsAdmin, "DB non-admin must win over the is_admin=true Kratos trait")
}

// JAB-5 regression guard: the Kratos self-service *profile* method must stay
// DISABLED. It is the only settings-flow method that mutates identity traits;
// re-enabling it lets any signed-in user PATCH their own username/email/is_admin
// via a crafted /.ory settings flow and desync Kratos from the authoritative
// panel DB. A live crafted-submission test needs a running Kratos; this cheap
// guard fails the build if someone flips the template back on.
func TestKratosProfileMethodDisabled(t *testing.T) {
	// panel-api/internal/api → repo root is ../../../
	raw, err := os.ReadFile("../../../install/kratos.yml.tmpl")
	require.NoError(t, err)
	tmpl := string(raw)

	// Capture the profile: block up to the next 4-space-indented method key
	// (`\n    link:` etc.). A plain "\n    " delimiter would wrongly stop at
	// the 6-space-indented comment lines inside the block.
	m := regexp.MustCompile(`(?s)\n    profile:\n(.*?)\n    [a-z]`).FindStringSubmatch(tmpl)
	require.Len(t, m, 2, "profile method block not found in kratos.yml.tmpl")
	block := m[1]
	require.NotContains(t, block, "enabled: true",
		"JAB-5: Kratos self-service profile method must be disabled (found enabled: true in the profile block)")
	require.Contains(t, block, "enabled: false",
		"JAB-5: Kratos profile block must explicitly set enabled: false")
}
