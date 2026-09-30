package api

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// GH #1938: an admin create failed with "internal" and nothing in the
// journal. An error the helper cannot map is logged; a mapped one is not.
func TestUserOpsRESTError_LogsOnlyUnmappedErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		err    error
		status int
		logged bool
	}{
		{"unmapped", fmt.Errorf("%w: Error 1048: Column 'username' cannot be null", userops.ErrInternal), http.StatusInternalServerError, true},
		{"username taken", fmt.Errorf("%w: ops", userops.ErrUsernameTaken), http.StatusConflict, false},
		{"invalid username", userops.ErrInvalidUsername, http.StatusBadRequest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)

			userOpsRESTError(c, log, tc.err)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := strings.Contains(buf.String(), "cannot be null"); got != tc.logged {
				t.Fatalf("logged = %v, want %v: %q", got, tc.logged, buf.String())
			}
			if !tc.logged && buf.Len() != 0 {
				t.Fatalf("a mapped error must not be logged: %q", buf.String())
			}
		})
	}
	// A nil logger never panics.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	userOpsRESTError(c, nil, errors.New("boom"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("nil logger: status = %d", w.Code)
	}
}
