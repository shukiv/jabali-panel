package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2078: an administrator acknowledges a profile kept in complain mode on
// purpose, and the "not enforcing" alert stops naming it until its mode changes.

func aaSummary(profiles ...[2]string) json.RawMessage {
	type prof struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	payload := struct {
		Enabled  bool   `json:"enabled"`
		Profiles []prof `json:"profiles"`
	}{Enabled: true}
	for _, p := range profiles {
		payload.Profiles = append(payload.Profiles, prof{Name: p[0], Mode: p[1]})
	}
	b, _ := json.Marshal(payload)
	return b
}

func appArmorAlert(alerts []ServerStatusAlert) *ServerStatusAlert {
	for i := range alerts {
		if alerts[i].Kind == "apparmor" {
			return &alerts[i]
		}
	}
	return nil
}

func TestSynthesizeAlerts_AppArmorAcknowledged(t *testing.T) {
	summary := map[string]json.RawMessage{"apparmor": aaSummary(
		[2]string{"jabali-fpm-app", "complain"},
		[2]string{"jabali-sendmail", "complain"},
		[2]string{"jabali-panel", "enforce"},
	)}

	t.Run("an acknowledged complain profile is left out and the count follows", func(t *testing.T) {
		a := appArmorAlert(synthesizeAlerts(summary, map[string]string{}, map[string]string{"jabali-fpm-app": "complain"}))
		if a == nil {
			t.Fatal("the unacknowledged complain profile must still alert")
		}
		if a.Detail != "1 AppArmor profile not enforcing: jabali-sendmail (complain)" {
			t.Fatalf("detail = %q", a.Detail)
		}
	})

	t.Run("every degraded profile acknowledged → no alert", func(t *testing.T) {
		acked := map[string]string{"jabali-fpm-app": "complain", "jabali-sendmail": "complain"}
		if a := appArmorAlert(synthesizeAlerts(summary, map[string]string{}, acked)); a != nil {
			t.Fatalf("all acknowledged must not alert, got %q", a.Detail)
		}
	})

	t.Run("a missing or unconfined profile is always named", func(t *testing.T) {
		faults := map[string]json.RawMessage{"apparmor": aaSummary(
			[2]string{"jabali-fpm-app", "missing"},
			[2]string{"jabali-sendmail", "unconfined"},
		)}
		acked := map[string]string{"jabali-fpm-app": "missing", "jabali-sendmail": "complain"}
		a := appArmorAlert(synthesizeAlerts(faults, map[string]string{}, acked))
		if a == nil || !strings.Contains(a.Detail, "2 AppArmor profiles") {
			t.Fatalf("faults must alert whatever was acknowledged, got %+v", a)
		}
	})
}

type fakeAppArmorAcks struct {
	rows    []models.AppArmorModeAck
	listErr error
	deleted []string
}

func (f *fakeAppArmorAcks) List(context.Context) ([]models.AppArmorModeAck, error) {
	return f.rows, f.listErr
}

func (f *fakeAppArmorAcks) Put(_ context.Context, row *models.AppArmorModeAck) error {
	f.rows = append(f.rows, *row)
	return nil
}

func (f *fakeAppArmorAcks) Delete(_ context.Context, profile string) error {
	f.deleted = append(f.deleted, profile)
	return nil
}

func TestServerStatusAppArmorAcks_EndWhenTheModeChanges(t *testing.T) {
	rows := []models.AppArmorModeAck{
		{Profile: "jabali-fpm-app", Mode: "complain"},
		{Profile: "jabali-sendmail", Mode: "complain"}, // now enforcing
		{Profile: "jabali-gone", Mode: "complain"},     // no longer listed
	}

	t.Run("a fresh summary keeps the ones still in their mode and deletes the rest", func(t *testing.T) {
		repo := &fakeAppArmorAcks{rows: rows}
		h := &adminServerStatusHandler{cfg: AdminServerStatusHandlerConfig{AppArmorAcks: repo}}
		got := h.appArmorAcks(context.Background(), aaSummary(
			[2]string{"jabali-fpm-app", "complain"},
			[2]string{"jabali-sendmail", "enforce"},
		))
		if !reflect.DeepEqual(got, map[string]string{"jabali-fpm-app": "complain"}) {
			t.Fatalf("acked = %v", got)
		}
		sort.Strings(repo.deleted)
		if !reflect.DeepEqual(repo.deleted, []string{"jabali-gone", "jabali-sendmail"}) {
			t.Fatalf("deleted = %v", repo.deleted)
		}
	})

	t.Run("no fresh summary deletes nothing", func(t *testing.T) {
		repo := &fakeAppArmorAcks{rows: rows}
		h := &adminServerStatusHandler{cfg: AdminServerStatusHandlerConfig{AppArmorAcks: repo}}
		if got := h.appArmorAcks(context.Background(), nil); len(got) != 3 {
			t.Fatalf("acked = %v", got)
		}
		if len(repo.deleted) != 0 {
			t.Fatalf("deleted %v without a fresh reading", repo.deleted)
		}
	})

	t.Run("unreadable acknowledgements name every profile", func(t *testing.T) {
		repo := &fakeAppArmorAcks{rows: rows, listErr: errors.New("db down")}
		h := &adminServerStatusHandler{cfg: AdminServerStatusHandlerConfig{AppArmorAcks: repo}}
		if got := h.appArmorAcks(context.Background(), aaSummary([2]string{"jabali-fpm-app", "complain"})); got != nil {
			t.Fatalf("acked = %v, want nil", got)
		}
	})
}

// Through the handler: the Server Status alert leaves out a profile whose
// acknowledgement holds, and still names the other one.
func TestServerStatus_AcknowledgedProfileLeavesTheAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := agent.NewMockClient().On("security.apparmor.summary", map[string]any{
		"enabled": true,
		"profiles": []map[string]string{
			{"name": "jabali-fpm-app", "mode": "complain"},
			{"name": "jabali-sendmail", "mode": "complain"},
		},
	})
	acks := &fakeAppArmorAcks{rows: []models.AppArmorModeAck{{Profile: "jabali-fpm-app", Mode: "complain"}}}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "adm", IsAdmin: true})
		c.Next()
	})
	RegisterAdminServerStatusRoutes(v1, AdminServerStatusHandlerConfig{Agent: mock, AppArmorAcks: acks})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status?view=health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env ServerStatusEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	a := appArmorAlert(env.Alerts)
	if a == nil || a.Detail != "1 AppArmor profile not enforcing: jabali-sendmail (complain)" {
		t.Fatalf("apparmor alert = %+v", a)
	}
}
