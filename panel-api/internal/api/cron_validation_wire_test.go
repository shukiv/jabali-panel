package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/cronops"
)

// TestMapCronopsErr_SurfacesValidationCode pins the validation_failed wire
// contract the Cron UI depends on (GH #1686 item 5): a cronops name/schedule/
// command error must be translated into { error, field, code, detail } with the
// structured cronvalidate code and the CLEAN validator detail — never the opaque
// "cronops: invalid <x>: <code>: <detail>" wrapper. The UI's code→message map
// (components/cron/cronErrorHeadline) keys on `code`; if this handler stops
// emitting it, the map silently dies and users see raw errors again.
//
// The fixture wraps the ValidationError exactly as cronops does (multi-%w), so
// this guards the API adapter's errors.As extraction. The cronops-side guard
// that the wrap itself stays %w (not %v) lives in cronops_test.go.
func TestMapCronopsErr_SurfacesValidationCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		err        error
		wantField  string
		wantCode   string
		wantDetail string
	}{
		{
			name:       "command",
			err:        fmt.Errorf("%w: %w", cronops.ErrCommandInvalid, &cronvalidate.ValidationError{Code: cronvalidate.ErrCodeBinaryNotAllowed, Detail: `first token must be "wp", got "ls"`}),
			wantField:  "command",
			wantCode:   cronvalidate.ErrCodeBinaryNotAllowed,
			wantDetail: `first token must be "wp", got "ls"`,
		},
		{
			name:       "schedule",
			err:        fmt.Errorf("%w: %w", cronops.ErrScheduleInvalid, &cronvalidate.ValidationError{Code: cronvalidate.ErrCodeBadScheduleSyntax, Detail: "expected exactly 5 fields"}),
			wantField:  "schedule",
			wantCode:   cronvalidate.ErrCodeBadScheduleSyntax,
			wantDetail: "expected exactly 5 fields",
		},
		{
			name:       "name",
			err:        fmt.Errorf("%w: %w", cronops.ErrNameInvalid, &cronvalidate.ValidationError{Code: cronvalidate.ErrCodeInvalidName, Detail: "cron name contains invalid control characters"}),
			wantField:  "name",
			wantCode:   cronvalidate.ErrCodeInvalidName,
			wantDetail: "cron name contains invalid control characters",
		},
	}

	h := &cronHandler{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/cron", nil)

			h.mapCronopsErr(c, tc.err)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d want 400", w.Code)
			}
			var body struct {
				Error  string `json:"error"`
				Field  string `json:"field"`
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
			}
			if body.Error != "validation_failed" {
				t.Fatalf("error: got %q want validation_failed", body.Error)
			}
			if body.Field != tc.wantField {
				t.Fatalf("field: got %q want %q", body.Field, tc.wantField)
			}
			if body.Code != tc.wantCode {
				t.Fatalf("code: got %q want %q (the UI map keys on this)", body.Code, tc.wantCode)
			}
			if body.Detail != tc.wantDetail {
				t.Fatalf("detail: got %q want clean %q", body.Detail, tc.wantDetail)
			}
		})
	}
}
