package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// An internal reference: a JAB- ticket, an ADR number, a milestone (M43,
// M33.2), a GitHub issue or PR number, or a wave name. The bare "#1234" form
// must not follow "&" (an HTML entity), a word character, "/" or "#".
var docsInternalCodeRe = regexp.MustCompile(`\bJAB-\d+|\bADRs? ?-?\d{3,4}\b|\bM\d+(?:\.\d+)*[a-z]?\b|\b(?:GH|PR) ?#\d+|(?:^|[^&\w/#])#\d{2,5}\b|\bWave \d`)

// Product names that look like a milestone code.
var docsInternalCodeAllowed = map[string]bool{"M365": true}

// Pages in docs/site that are notes for the website team, not published docs.
var docsInternalPages = map[string]bool{"README.md": true, "INTEGRATION.md": true}

// TestDocsSiteHasNoInternalCodes keeps internal codes out of the published
// docs. jabali-panel.com merges its pages from docs/site and removes every
// such code by hand, so a code added here comes back on the website with the
// next merge. Say what the feature does and where it is instead. The CLI
// reference and the notification event catalog are generated from the Cobra
// help text and the event catalog, so their codes are fixed in the Go source.
func TestDocsSiteHasNoInternalCodes(t *testing.T) {
	problems, err := findDocsInternalCodes(docsSiteDir)
	if err != nil {
		t.Fatalf("walk %s: %v", docsSiteDir, err)
	}
	if len(problems) > 0 {
		t.Errorf("%d internal code(s) in the published docs:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

// findDocsInternalCodes returns "file:line: code" for every internal code in
// the published markdown under dir.
func findDocsInternalCodes(dir string) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") || docsInternalPages[filepath.Base(path)] {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		rel, _ := filepath.Rel(dir, path)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			for _, m := range docsInternalCodeRe.FindAllString(sc.Text(), -1) {
				code := strings.TrimLeft(m, " \t([{*_,;:.-\"'`")
				if docsInternalCodeAllowed[code] {
					continue
				}
				problems = append(problems, rel+":"+strconv.Itoa(lineNo)+": "+code)
			}
		}
		return sc.Err()
	})
	return problems, err
}
