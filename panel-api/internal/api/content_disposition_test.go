package api

import (
	"mime"
	"strings"
	"testing"
)

// rawFallback extracts the legacy `filename="..."` token straight from the
// header string (before mime.ParseMediaType folds filename* over it), so we can
// assert the fallback itself can't break out of its quotes.
func rawFallback(t *testing.T, header string) string {
	t.Helper()
	const marker = `filename="`
	i := strings.Index(header, marker)
	if i < 0 {
		t.Fatalf("no legacy filename= param in %q", header)
	}
	rest := header[i+len(marker):]
	j := strings.IndexByte(rest, '"') // our fallback strips all inner quotes, so the next quote closes it
	if j < 0 {
		t.Fatalf("unterminated filename= param in %q", header)
	}
	return rest[:j]
}

func TestContentDisposition_ParsesToTrueName(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		wantName string // the true name the browser should save
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"breakout attempt", `evil";filename="safe.txt`, `evil";filename="safe.txt`},
		{"semicolon", "a;b;c.txt", "a;b;c.txt"},
		{"backslash", `a\b.txt`, `a\b.txt`},
		{"utf8", "naïve—файл.txt", "naïve—файл.txt"},
		{"leading path stripped", "../../etc/passwd", "passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := contentDisposition("attachment", tc.filename)

			// A breakout would either fail to parse or yield a filename other
			// than the true name (a stray injected param wins/duplicates).
			dtype, params, err := mime.ParseMediaType(h)
			if err != nil {
				t.Fatalf("header %q does not parse: %v", h, err)
			}
			if dtype != "attachment" {
				t.Errorf("disposition type = %q, want attachment", dtype)
			}
			// mime folds the decoded filename* into params["filename"].
			if got := params["filename"]; got != tc.wantName {
				t.Errorf("decoded filename = %q, want %q", got, tc.wantName)
			}

			// The raw legacy fallback must be free of breakout vectors.
			fb := rawFallback(t, h)
			if strings.ContainsAny(fb, "\"\\") {
				t.Errorf("legacy fallback %q contains a quote/backslash", fb)
			}
			for _, r := range fb {
				if r < 0x20 || r == 0x7f {
					t.Errorf("legacy fallback %q contains a control char", fb)
				}
			}
		})
	}
}

func TestContentDisposition_ControlCharsStripped(t *testing.T) {
	h := contentDisposition("attachment", "a\r\n\tb\x00.txt")
	_, params, err := mime.ParseMediaType(h)
	if err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	// filename* preserves the true bytes (percent-encoded on the wire).
	if params["filename"] != "a\r\n\tb\x00.txt" {
		t.Errorf("decoded filename = %q, lost the true bytes", params["filename"])
	}
	// The ASCII fallback replaced every control char with '_'.
	if strings.ContainsAny(rawFallback(t, h), "\r\n\t\x00") {
		t.Errorf("fallback still has control chars: %q", rawFallback(t, h))
	}
}

func TestContentDisposition_InlineType(t *testing.T) {
	h := contentDisposition("inline", "pic.png")
	if !strings.HasPrefix(h, "inline; ") {
		t.Errorf("want inline disposition, got %q", h)
	}
}

func TestContentDisposition_EmptyFallsBackToDownload(t *testing.T) {
	h := contentDisposition("attachment", "/")
	if _, _, err := mime.ParseMediaType(h); err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	if !strings.Contains(h, `filename="download"`) {
		t.Errorf("want download fallback, got %q", h)
	}
}

func TestRFC5987Encode(t *testing.T) {
	cases := map[string]string{
		"abc":     "abc",
		"a b":     "a%20b",
		`a"b`:     "a%22b",
		"a;b":     "a%3Bb",
		"café":    "caf%C3%A9", // UTF-8 bytes, per-byte percent-encoding
		"a-b_c.d": "a-b_c.d",   // attr-chars pass through
	}
	for in, want := range cases {
		if got := rfc5987Encode(in); got != want {
			t.Errorf("rfc5987Encode(%q) = %q, want %q", in, got, want)
		}
	}
}
