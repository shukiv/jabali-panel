package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const appsecBase = "/api/v1/admin/security/crowdsec/appsec"

func appsecExclRouter(t *testing.T, mock agent.AgentInterface, store *wafExclStore, admin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: admin})
		c.Next()
	})
	RegisterSecurityAppSecExclusionRoutes(v1, SecurityAppSecExclusionConfig{
		Agent: mock, Exclusions: store, HostModes: wafModes{},
	})
	return r
}

func appsecDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func applyCalls(m *agent.MockClient) int {
	n := 0
	for _, c := range m.Calls() {
		if c.Command == appseccfg.OperatorApplyVerb {
			n++
		}
	}
	return n
}

func okApply(m *agent.MockClient) *agent.MockClient {
	return m.On(appseccfg.OperatorApplyVerb, appseccfg.OperatorApplyResult{Changed: true, Reloaded: true})
}

var sampleEvents = appsecops.EventsResponse{
	Events: []appsecops.Event{
		{Timestamp: "2026-09-27T10:00:00Z", SourceIP: "1.1.1.1", TargetHost: "shop.example.com", TargetURI: "/cart", RuleIDs: []string{"901340", "942100", "949110"}},
		{Timestamp: "2026-09-27T10:02:00Z", SourceIP: "2.2.2.2", TargetHost: "shop.example.com", TargetURI: "/cart", RuleIDs: []string{"901340", "942100", "949110"}},
	},
	InlineBlocks:  []appsecops.InlineBlock{{Timestamp: "t", SourceIP: "9.9.9.9", Scores: "lfi: 5"}},
	AlertsScanned: 2,
}

func TestAppSecEvents_GroupsAndPassesLimit(t *testing.T) {
	m := agent.NewMockClient().On(appsecops.EventsVerb, sampleEvents)
	r := appsecExclRouter(t, m, &wafExclStore{}, true)

	rec := appsecDo(r, http.MethodGet, appsecBase+"/events?limit=10", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Patterns      []appsecops.Pattern          `json:"patterns"`
		InlineBlocks  []appsecops.InlineBlockGroup `json:"inline_blocks"`
		AlertsScanned int                          `json:"alerts_scanned"`
		EventsCount   int                          `json:"events_count"`
		Limit         int                          `json:"limit"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Patterns, 1)
	assert.Equal(t, 2, body.Patterns[0].Count)
	assert.Equal(t, 2, body.Patterns[0].DistinctIPs)
	assert.Equal(t, []string{"942100"}, body.Patterns[0].Detections)
	assert.Len(t, body.Patterns[0].Infra, 2)
	assert.Equal(t, 2, body.AlertsScanned)
	assert.Equal(t, 2, body.EventsCount)
	assert.Equal(t, 10, body.Limit)
	require.Len(t, body.InlineBlocks, 1)

	calls := m.Calls()
	require.Len(t, calls, 1)
	assert.JSONEq(t, `{"limit":10}`, string(calls[0].Params))
}

func TestAppSecEvents_DefaultLimit(t *testing.T) {
	m := agent.NewMockClient().On(appsecops.EventsVerb, appsecops.EventsResponse{})
	r := appsecExclRouter(t, m, &wafExclStore{}, true)
	rec := appsecDo(r, http.MethodGet, appsecBase+"/events", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"limit":25}`, string(m.Calls()[0].Params))
	assert.Contains(t, rec.Body.String(), `"patterns":[]`)
	assert.Contains(t, rec.Body.String(), `"inline_blocks":[]`)
}

func TestAppSecEvents_RejectsLimitOutOfRange(t *testing.T) {
	for _, q := range []string{"0", "51", "200", "-1", "x"} {
		m := agent.NewMockClient().On(appsecops.EventsVerb, appsecops.EventsResponse{})
		r := appsecExclRouter(t, m, &wafExclStore{}, true)
		rec := appsecDo(r, http.MethodGet, appsecBase+"/events?limit="+q, "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, "limit=%s", q)
		assert.Empty(t, m.Calls(), "limit=%s reached the agent", q)
	}
}

func TestAppSecEvents_AgentTimeoutIs504(t *testing.T) {
	m := agent.NewMockClient().OnError(appsecops.EventsVerb,
		&agent.AgentError{Code: agent.CodeDeadlineExceeded, Message: "deadline"})
	r := appsecExclRouter(t, m, &wafExclStore{}, true)
	rec := appsecDo(r, http.MethodGet, appsecBase+"/events", "")
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
}

// slowAgent answers only when the caller gives up, the way agent.Client does
// when the request's deadline passes: a wrapped socket timeout, not an
// AgentError.
type slowAgent struct{}

func (slowAgent) Call(ctx context.Context, _ string, _ any) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("agent: read: %w", os.ErrDeadlineExceeded)
}

func TestAppSecEvents_OwnDeadlineIs504(t *testing.T) {
	orig := appsecEventsTimeout
	appsecEventsTimeout = 20 * time.Millisecond
	t.Cleanup(func() { appsecEventsTimeout = orig })

	r := appsecExclRouter(t, slowAgent{}, &wafExclStore{}, true)
	rec := appsecDo(r, http.MethodGet, appsecBase+"/events?limit=50", "")
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"agent_timeout"`)
}

func TestAppSecExclusions_AdminOnly(t *testing.T) {
	m := okApply(agent.NewMockClient())
	store := &wafExclStore{}
	r := appsecExclRouter(t, m, store, false)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, appsecBase + "/events", ""},
		{http.MethodGet, appsecBase + "/exclusions", ""},
		{http.MethodPost, appsecBase + "/exclusions", `{"host":"a.example.com","uri_prefix":"/x/","rule_id":"942100"}`},
		{http.MethodDelete, appsecBase + "/exclusions/01HX", ""},
	} {
		rec := appsecDo(r, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", tc.method, tc.path)
	}
	assert.Empty(t, m.Calls())
	assert.Empty(t, store.rows)
}

func TestAppSecExclusions_ListMarksManagedRows(t *testing.T) {
	flarum, err := appsecops.FlarumExclusion("inst1", "forum.example.com", false, "")
	require.NoError(t, err)
	store := &wafExclStore{rows: []models.CRSRuleExclusion{
		{ID: "a", Host: flarum.Host, URIPrefix: flarum.URIPrefix, RuleID: flarum.RuleID, Note: flarum.Note},
		{ID: "b", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100", Note: "by hand"},
	}}
	r := appsecExclRouter(t, agent.NewMockClient(), store, true)

	rec := appsecDo(r, http.MethodGet, appsecBase+"/exclusions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data []struct {
			ID               string `json:"id"`
			Host             string `json:"host"`
			URIPrefix        string `json:"uri_prefix"`
			ManagedInstallID string `json:"managed_install_id"`
		} `json:"data"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 2, body.Total)
	assert.Equal(t, "inst1", body.Data[0].ManagedInstallID)
	assert.Equal(t, "/x/", body.Data[1].URIPrefix)
	assert.Empty(t, body.Data[1].ManagedInstallID)
}

func TestAppSecExclusions_ListEmptyIsArray(t *testing.T) {
	r := appsecExclRouter(t, agent.NewMockClient(), &wafExclStore{}, true)
	rec := appsecDo(r, http.MethodGet, appsecBase+"/exclusions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"data":[]`)
}

func TestAppSecExclusions_AddStoresAndApplies(t *testing.T) {
	m := okApply(agent.NewMockClient())
	store := &wafExclStore{}
	r := appsecExclRouter(t, m, store, true)

	rec := appsecDo(r, http.MethodPost, appsecBase+"/exclusions",
		`{"host":"Blog.Example.com","uri_prefix":"/wp-json/x/","rule_id":"942100","note":"webhook"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Len(t, store.rows, 1)
	assert.Equal(t, "blog.example.com", store.rows[0].Host)
	assert.Equal(t, 1, applyCalls(m))

	var body struct {
		Exclusion struct {
			ID string `json:"id"`
		} `json:"exclusion"`
		Apply appseccfg.OperatorApplyResult `json:"apply"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, store.rows[0].ID, body.Exclusion.ID)
	assert.True(t, body.Apply.Reloaded)
}

func TestAppSecExclusions_AddRefusesInvalid(t *testing.T) {
	for name, payload := range map[string]string{
		"no path":         `{"host":"blog.example.com","rule_id":"942100"}`,
		"anomaly blocker": `{"host":"blog.example.com","uri_prefix":"/x/","rule_id":"949110"}`,
		"seclang quote":   `{"host":"blog.example.com","uri_prefix":"/x\"/","rule_id":"942100"}`,
		"bad host":        `{"host":"blog example.com","uri_prefix":"/x/","rule_id":"942100"}`,
	} {
		m := okApply(agent.NewMockClient())
		store := &wafExclStore{}
		r := appsecExclRouter(t, m, store, true)
		rec := appsecDo(r, http.MethodPost, appsecBase+"/exclusions", payload)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		assert.Contains(t, rec.Body.String(), `"invalid_exclusion"`, name)
		assert.Empty(t, store.rows, name)
		assert.Empty(t, m.Calls(), name)
	}
}

func TestAppSecExclusions_AddMalformedJSON(t *testing.T) {
	m := okApply(agent.NewMockClient())
	r := appsecExclRouter(t, m, &wafExclStore{}, true)
	rec := appsecDo(r, http.MethodPost, appsecBase+"/exclusions", `{"host":`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, m.Calls())
}

func TestAppSecExclusions_AddDuplicateIs409(t *testing.T) {
	m := okApply(agent.NewMockClient())
	store := &wafExclStore{rows: []models.CRSRuleExclusion{{ID: "a", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100"}}}
	r := appsecExclRouter(t, m, store, true)
	rec := appsecDo(r, http.MethodPost, appsecBase+"/exclusions", `{"host":"blog.example.com","uri_prefix":"/x/","rule_id":"942100"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Len(t, store.rows, 1)
	assert.Empty(t, m.Calls())
}

// The WAF cannot be updated: the operator gets an error that says whether the
// change was undone, and the list does not keep the exclusion.
func TestAppSecExclusions_AddApplyFailureLeavesNoRow(t *testing.T) {
	m := agent.NewMockClient().OnError(appseccfg.OperatorApplyVerb,
		&agent.AgentError{Code: agent.CodeInternal, Message: "crowdsec reload and restart failed"})
	store := &wafExclStore{}
	r := appsecExclRouter(t, m, store, true)

	rec := appsecDo(r, http.MethodPost, appsecBase+"/exclusions", `{"host":"blog.example.com","uri_prefix":"/x/","rule_id":"942100"}`)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Empty(t, store.rows)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["rolled_back"])
	assert.Contains(t, body["detail"], "crowdsec reload and restart failed")
	assert.Contains(t, body["detail"], "render-config", "the second apply failed too, so the operator must be told how to fix the file")
}

func TestAppSecExclusions_RemoveApplies(t *testing.T) {
	m := okApply(agent.NewMockClient())
	store := &wafExclStore{rows: []models.CRSRuleExclusion{{ID: "01HX", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100"}}}
	r := appsecExclRouter(t, m, store, true)
	rec := appsecDo(r, http.MethodDelete, appsecBase+"/exclusions/01HX", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, store.rows)
	assert.Equal(t, 1, applyCalls(m))
}

func TestAppSecExclusions_RemoveUnknownIs404(t *testing.T) {
	for _, id := range []string{"nope", strings.Repeat("A", 27)} {
		m := okApply(agent.NewMockClient())
		store := &wafExclStore{rows: []models.CRSRuleExclusion{{ID: "01HX", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100"}}}
		r := appsecExclRouter(t, m, store, true)
		rec := appsecDo(r, http.MethodDelete, appsecBase+"/exclusions/"+id, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, id)
		assert.Len(t, store.rows, 1, id)
		assert.Empty(t, m.Calls(), id)
	}
}

func TestAppSecExclusions_RemoveApplyFailureKeepsRow(t *testing.T) {
	m := agent.NewMockClient().OnError(appseccfg.OperatorApplyVerb,
		&agent.AgentError{Code: agent.CodeUnavailable, Message: "agent down"})
	row := models.CRSRuleExclusion{ID: "01HX", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100", Note: "n"}
	store := &wafExclStore{rows: []models.CRSRuleExclusion{row}}
	r := appsecExclRouter(t, m, store, true)

	rec := appsecDo(r, http.MethodDelete, appsecBase+"/exclusions/01HX", "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Len(t, store.rows, 1)
	assert.Equal(t, row, store.rows[0])
	assert.Contains(t, rec.Body.String(), `"rolled_back":true`)
}
