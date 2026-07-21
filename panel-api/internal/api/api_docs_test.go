package api

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPISpecParsesStrictly guards the GET /_meta/openapi.json endpoint,
// which does a FULL yaml.v3 decode of the embedded spec (`var doc any`) and so
// rejects a duplicate mapping key ANYWHERE — returning 500 spec_parse_failed to
// the user (GH #580: "mapping key \"summary\" already defined").
//
// TestOpenAPICoverage decodes only shallowly (paths -> verb -> yaml.Node), so a
// duplicate key INSIDE an operation slips past it — which is exactly how #580
// shipped. This test parses the spec the same way the endpoint does, so any
// duplicate key (or other parse error) fails CI instead of a user's browser.
func TestOpenAPISpecParsesStrictly(t *testing.T) {
	var doc any
	if err := yaml.Unmarshal(openAPIYAML, &doc); err != nil {
		t.Fatalf("embedded openapi.yaml does not parse — GET /_meta/openapi.json would return 500: %v", err)
	}
}
