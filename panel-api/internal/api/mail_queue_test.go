package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

func newMailQueueRouter(mock agent.AgentInterface, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(isAdmin))
	api.RegisterAdminMailQueueRoutes(v1, mock)
	return r
}

func TestMailQueue_RBAC(t *testing.T) {
	r := newMailQueueRouter(agent.NewMockClient(), false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/queue", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMailQueue_ListEnvelopeAndDomainFilter(t *testing.T) {
	mock := agent.NewMockClient().On("mail.queue.list", map[string]any{
		"data": []map[string]any{
			{"id": "m1", "from": "b@send.com", "recipients": []string{"x@d1.com"}, "size": 100},
			{"id": "m2", "from": "b@send.com", "recipients": []string{"y@d2.com"}, "size": 200},
			{"id": "m3", "from": "z@d1.com", "recipients": []string{"q@other.com"}, "size": 300},
		},
		"total": 3,
	})
	r := newMailQueueRouter(mock, true)

	// no filter → canonical envelope, all 3
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/queue", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data     []map[string]any `json:"data"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Len(t, env.Data, 3)
	assert.Equal(t, 3, env.Total)
	assert.Equal(t, 1, env.Page)
	assert.Greater(t, env.PageSize, 0)

	// ?domain=d1.com → m1 (rcpt @d1.com) + m3 (from @d1.com)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/queue?domain=d1.com", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, 2, env.Total, "domain filter d1.com → m1+m3")

	// pagination
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/queue?page=2&page_size=2", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Len(t, env.Data, 1, "page2 size2 of 3 → 1")
	assert.Equal(t, 2, env.Page)
	assert.Equal(t, 2, env.PageSize)
}

// A page far past the end is an empty page, including pages so large that
// page*size does not fit in an int.
func TestMailQueue_PagePastTheEndIsEmpty(t *testing.T) {
	mock := agent.NewMockClient().On("mail.queue.list", map[string]any{
		"data": []map[string]any{
			{"id": "m1", "from": "b@send.com", "recipients": []string{"x@d1.com"}, "size": 100},
			{"id": "m2", "from": "b@send.com", "recipients": []string{"y@d2.com"}, "size": 200},
			{"id": "m3", "from": "z@d1.com", "recipients": []string{"q@other.com"}, "size": 300},
		},
		"total": 3,
	})
	r := newMailQueueRouter(mock, true)
	for _, q := range []string{
		"page=3&page_size=2",
		"page=4611686018427387905&page_size=2",
		"page=9223372036854775807&page_size=2",
		"page=9223372036854775807",
		"page=46116860184273880&page_size=200",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/queue?"+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, q)
		var env struct {
			Data  []map[string]any `json:"data"`
			Total int              `json:"total"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), q)
		assert.Empty(t, env.Data, q)
		assert.Equal(t, 3, env.Total, q)
	}
}

func TestMailQueue_RetryDelete(t *testing.T) {
	mock := agent.NewMockClient().
		On("mail.queue.retry", map[string]any{"id": "m1", "rescheduled": true}).
		On("mail.queue.delete", map[string]any{"id": "m1", "deleted": true})
	r := newMailQueueRouter(mock, true)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/mail/queue/m1/retry", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mail/queue/m1", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}
