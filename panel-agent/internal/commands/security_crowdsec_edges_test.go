package commands

import (
	"strings"
	"testing"
	"time"
)

func TestParseCrowdsecDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantArg string
		wantErr bool
	}{
		{"4h", 4 * time.Hour, "4h", false},
		{"30m", 30 * time.Minute, "30m", false},
		{"168h", 168 * time.Hour, "168h", false},
		// CrowdSec-native day forms Go's ParseDuration rejects.
		{"7d", 7 * 24 * time.Hour, "168h0m0s", false},
		{"7d12h", 7*24*time.Hour + 12*time.Hour, "180h0m0s", false},
		{"1d30m", 24*time.Hour + 30*time.Minute, "24h30m0s", false},
		{"forever", 0, "", true},
		{"", 0, "", true},
		{"0d", 0, "", true},
		{"-1d", 0, "", true},
		{"7dxyz", 0, "", true},
	}
	for _, tc := range cases {
		d, arg, err := parseCrowdsecDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %v / %q", tc.in, d, arg)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if d != tc.want || arg != tc.wantArg {
			t.Errorf("%q: got (%v, %q), want (%v, %q)", tc.in, d, arg, tc.want, tc.wantArg)
		}
	}
}

func TestCscliDeletedCount(t *testing.T) {
	cases := []struct {
		out    string
		want   int
		wantOK bool
	}{
		{"1 decision(s) deleted", 1, true},
		{"0 decision(s) deleted", 0, true},
		{"12 decisions deleted", 12, true},
		{"WARN some noise\n3 decision(s) deleted\n", 3, true},
		// Format drift → ok=false so callers assume success (never fail a
		// real deletion on parse drift).
		{"deleted", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		n, ok := cscliDeletedCount(tc.out)
		if n != tc.want || ok != tc.wantOK {
			t.Errorf("%q: got (%d,%v), want (%d,%v)", tc.out, n, ok, tc.want, tc.wantOK)
		}
	}
}

// The day-form duration must pass decisions.add validation (it previously
// rejected CrowdSec's own "7d" syntax); the handler then fails later at the
// cscli exec stage in a test environment, which is fine — the validation
// message must NOT be about the duration.
func TestCSDecisionsAddAcceptsDayDuration(t *testing.T) {
	_, err := csDecisionsAddHandler(t.Context(), []byte(`{"scope":"ip","value":"203.0.113.9","duration":"7d","reason":"test entry"}`))
	if err != nil && strings.Contains(err.Error(), "duration") {
		t.Fatalf("day duration rejected by validation: %v", err)
	}
}
