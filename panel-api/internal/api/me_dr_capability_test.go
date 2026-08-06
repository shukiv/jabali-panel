package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #331 Step 3: /me/server-capabilities exposes is_standby (+ peer label) so
// every signed-in user's SPA can render the read-only-replica banner.
func TestServerCapabilities_DRStandbyFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	call := func(role, peer string) map[string]any {
		h := &meExtHandler{cfg: MeHandlerConfig{
			ServerSettings: &mockServerSettingsRepo{getResult: &models.ServerSettings{
				ID: 1, ServerRole: role, DRPeerLabel: peer,
			}},
		}}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/me/server-capabilities", nil)
		h.serverCapabilities(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	standby := call(models.ServerRoleStandby, "primary-mx.example.com")
	if standby["is_standby"] != true {
		t.Errorf("standby: is_standby = %v, want true", standby["is_standby"])
	}
	if standby["dr_peer_label"] != "primary-mx.example.com" {
		t.Errorf("standby: dr_peer_label = %v", standby["dr_peer_label"])
	}

	primary := call(models.ServerRolePrimary, "")
	if primary["is_standby"] != false {
		t.Errorf("primary: is_standby = %v, want false", primary["is_standby"])
	}
}
