package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// GH #1796: an over-max page_size must clamp DOWN to the endpoint max, not
// silently reset to the (small) default. A dropdown that over-requests
// (page_size=500 vs a max of 200) was getting the default page (20) back, so
// only the first 20 rows showed. page<1/invalid still falls back to default.
func TestParseListOptions_PageSizeClamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const def, max = 20, 200

	cases := []struct {
		name     string
		query    string
		wantPage int
		wantSize int
		wantOff  int
	}{
		{"over-max clamps to max", "page_size=500", 1, max, 0},
		{"at-max passes through", "page_size=200", 1, 200, 0},
		{"under-max passes through", "page_size=50", 1, 50, 0},
		{"zero falls back to default", "page_size=0", 1, def, 0},
		{"negative falls back to default", "page_size=-1", 1, def, 0},
		{"absent uses default", "", 1, def, 0},
		{"non-numeric falls back to default", "page_size=abc", 1, def, 0},
		{"over-max offset uses clamped size", "page=2&page_size=500", 2, max, max},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/?"+tc.query, nil)
			page, size, opts := parseListOptions(c, def, max)
			if page != tc.wantPage || size != tc.wantSize {
				t.Fatalf("page=%d size=%d, want page=%d size=%d", page, size, tc.wantPage, tc.wantSize)
			}
			if opts.Limit != tc.wantSize {
				t.Errorf("opts.Limit=%d, want %d", opts.Limit, tc.wantSize)
			}
			if opts.Offset != tc.wantOff {
				t.Errorf("opts.Offset=%d, want %d", opts.Offset, tc.wantOff)
			}
		})
	}
}
