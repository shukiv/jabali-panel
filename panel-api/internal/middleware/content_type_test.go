package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireContentType())
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	cases := []struct {
		name, method, ct, body string
		want                   int
	}{
		{"json ok", "POST", "application/json", `{}`, 200},
		{"multipart ok", "POST", "multipart/form-data; boundary=x", "x", 200},
		{"octet-stream ok (chunked upload, GH#211)", "POST", "application/octet-stream", "\x00\x01", 200},
		{"text rejected", "POST", "text/plain", "hi", http.StatusUnsupportedMediaType},
		{"empty body passes", "POST", "", "", 200},
		{"GET passes", "GET", "", "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/x", strings.NewReader(tc.body))
			if tc.ct != "" {
				req.Header.Set("Content-Type", tc.ct)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

// The Outlook autodiscover POST (GH #1039) arrives as text/xml, which the guard
// would otherwise 415. It must be exempt on its well-known path, case-insensitive.
func TestRequireContentType_AutodiscoverExempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireContentType())
	r.POST("/autodiscover/autodiscover.xml", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/Autodiscover/Autodiscover.xml", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/autodiscover/autodiscover.xml", "/Autodiscover/Autodiscover.xml"} {
		req := httptest.NewRequest("POST", path, strings.NewReader("<Autodiscover/>"))
		req.Header.Set("Content-Type", "text/xml")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s text/xml: got %d, want 200 (exempt)", path, w.Code)
		}
	}
	// Guard still bites text/xml on a non-exempt path.
	req := httptest.NewRequest("POST", "/x", strings.NewReader("<x/>"))
	req.Header.Set("Content-Type", "text/xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("non-exempt text/xml: got %d, want 415", w.Code)
	}
}
