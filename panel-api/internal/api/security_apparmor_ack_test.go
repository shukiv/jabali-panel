package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2078: the AppArmor routes acknowledge a profile kept in complain mode on
// purpose, and end the acknowledgement when the profile's mode changes.

// aaAgent answers the AppArmor verbs: the summary and the status list the
// profiles in modes, set_mode succeeds.
type aaAgent struct {
	modes map[string]string
	cmds  []string
}

func (a *aaAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.cmds = append(a.cmds, cmd)
	switch cmd {
	case "security.apparmor.summary", "security.apparmor.status":
		var pairs [][2]string
		for name, mode := range a.modes {
			pairs = append(pairs, [2]string{name, mode})
		}
		return aaSummary(pairs...), nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// aaAudit keeps the audit events recorded.
type aaAudit struct{ events []*models.AuditEvent }

func (a *aaAudit) Record(e *models.AuditEvent) { a.events = append(a.events, e) }

func newAppArmorRouter(ag *aaAgent, acks *fakeAppArmorAcks) *gin.Engine {
	return newAppArmorRouterAudited(ag, acks, nil)
}

func newAppArmorRouterAudited(ag *aaAgent, acks *fakeAppArmorAcks, rec *aaAudit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "01HZADMIN0000000000000000A", IsAdmin: true})
		c.Next()
	})
	cfg := SecurityAppArmorConfig{Agent: ag, Acks: acks}
	if rec != nil {
		cfg.Audit = rec
	}
	RegisterSecurityAppArmorRoutes(v1, cfg)
	return r
}

func aaDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAppArmorAcknowledge_StoresAComplainProfile(t *testing.T) {
	acks := &fakeAppArmorAcks{}
	r := newAppArmorRouter(&aaAgent{modes: map[string]string{"jabali-fpm-app": "complain"}}, acks)

	w := aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(acks.rows) != 1 || acks.rows[0].Profile != "jabali-fpm-app" || acks.rows[0].Mode != "complain" {
		t.Fatalf("stored %+v", acks.rows)
	}
	if by := acks.rows[0].AcknowledgedBy; by == nil || *by != "01HZADMIN0000000000000000A" {
		t.Fatalf("acknowledged_by = %v, want the admin", by)
	}
}

func TestAppArmorAcknowledge_RefusesAProfileNotInComplain(t *testing.T) {
	for _, mode := range []string{"enforce", "missing", "unconfined", "kernel-gated"} {
		acks := &fakeAppArmorAcks{}
		r := newAppArmorRouter(&aaAgent{modes: map[string]string{"jabali-fpm-app": mode}}, acks)
		w := aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge", "")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, want 422: %s", mode, w.Code, w.Body)
		}
		if len(acks.rows) != 0 {
			t.Fatalf("%s: stored %+v", mode, acks.rows)
		}
	}
}

func TestAppArmorAcknowledge_UnknownOrMalformedProfile(t *testing.T) {
	acks := &fakeAppArmorAcks{}
	ag := &aaAgent{modes: map[string]string{"jabali-fpm-app": "complain"}}
	r := newAppArmorRouter(ag, acks)

	if w := aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/no-such-profile/acknowledge", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: status %d, want 404", w.Code)
	}
	ag.cmds = nil
	if w := aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/bad;name/acknowledge", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed: status %d, want 400", w.Code)
	}
	if len(ag.cmds) != 0 {
		t.Fatalf("a malformed name reached the agent: %v", ag.cmds)
	}
	if len(acks.rows) != 0 {
		t.Fatalf("stored %+v", acks.rows)
	}
}

func TestAppArmorSetMode_EndsTheAcknowledgement(t *testing.T) {
	acks := &fakeAppArmorAcks{rows: []models.AppArmorModeAck{{Profile: "jabali-fpm-app", Mode: "complain"}}}
	r := newAppArmorRouter(&aaAgent{modes: map[string]string{"jabali-fpm-app": "complain"}}, acks)

	w := aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/mode", `{"mode":"enforce"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(acks.deleted, []string{"jabali-fpm-app"}) {
		t.Fatalf("deleted = %v", acks.deleted)
	}
}

func TestAppArmorUnacknowledge(t *testing.T) {
	acks := &fakeAppArmorAcks{}
	r := newAppArmorRouter(&aaAgent{}, acks)
	if w := aaDo(r, http.MethodDelete, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge", ""); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !reflect.DeepEqual(acks.deleted, []string{"jabali-fpm-app"}) {
		t.Fatalf("deleted = %v", acks.deleted)
	}
}

func TestAppArmorStatus_ListsTheAcknowledgementsThatStillHold(t *testing.T) {
	acks := &fakeAppArmorAcks{rows: []models.AppArmorModeAck{
		{Profile: "jabali-fpm-app", Mode: "complain"},
		{Profile: "jabali-sendmail", Mode: "complain"}, // now enforcing
	}}
	r := newAppArmorRouter(&aaAgent{modes: map[string]string{"jabali-fpm-app": "complain", "jabali-sendmail": "enforce"}}, acks)

	w := aaDo(r, http.MethodGet, "/v1/admin/security/apparmor/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		Profiles     []map[string]string `json:"profiles"`
		Acknowledged []string            `json:"acknowledged"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body.Acknowledged, []string{"jabali-fpm-app"}) {
		t.Fatalf("acknowledged = %v", body.Acknowledged)
	}
	if len(body.Profiles) != 2 {
		t.Fatalf("the agent's profiles must pass through, got %v", body.Profiles)
	}
	if !reflect.DeepEqual(acks.deleted, []string{"jabali-sendmail"}) {
		t.Fatalf("deleted = %v", acks.deleted)
	}
}

// The audit log names the profile for each change; the request log alone
// shows only the route.
func TestAppArmorRoutes_AuditNamesTheProfile(t *testing.T) {
	rec := &aaAudit{}
	r := newAppArmorRouterAudited(&aaAgent{modes: map[string]string{"jabali-fpm-app": "complain"}}, &fakeAppArmorAcks{}, rec)

	aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge", "")
	aaDo(r, http.MethodDelete, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge", "")
	aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-fpm-app/mode", `{"mode":"enforce"}`)
	aaDo(r, http.MethodPost, "/v1/admin/security/apparmor/profiles/jabali-panel/acknowledge", "") // refused: not recorded

	type ev struct{ action, target, actor string }
	var got []ev
	for _, e := range rec.events {
		actor := ""
		if e.ActorUserID != nil {
			actor = *e.ActorUserID
		}
		got = append(got, ev{e.Action, e.TargetID, actor})
	}
	want := []ev{
		{"security.apparmor.acknowledge", "jabali-fpm-app", "01HZADMIN0000000000000000A"},
		{"security.apparmor.unacknowledge", "jabali-fpm-app", "01HZADMIN0000000000000000A"},
		{"security.apparmor.set_mode", "jabali-fpm-app", "01HZADMIN0000000000000000A"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit events = %v\nwant %v", got, want)
	}
}
