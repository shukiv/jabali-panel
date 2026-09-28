package api

import (
	"fmt"
	"regexp"
	"sort"
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

// TestOpenAPISpecHasNoNullValues catches the YAML flow-mapping comma trap. In
// `{ description: Admin user, or a failed teardown }` the unquoted comma ends
// the description, and "or a failed teardown" becomes a key with a null value.
// The served spec then carries a cut-off description plus a junk field, which
// OpenAPI validators and client generators reject. The spec has no legitimate
// null anywhere, so every null is this trap: quote the string.
func TestOpenAPISpecHasNoNullValues(t *testing.T) {
	var doc any
	if err := yaml.Unmarshal(openAPIYAML, &doc); err != nil {
		t.Fatalf("parse embedded openapi.yaml: %v", err)
	}
	var found []string
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if val == nil {
					found = append(found, at+" -> "+k)
					continue
				}
				walk(val, at+"/"+k)
			}
		case map[any]any:
			for k, val := range x {
				key := fmt.Sprint(k)
				if val == nil {
					found = append(found, at+" -> "+key)
					continue
				}
				walk(val, at+"/"+key)
			}
		case []any:
			for i, val := range x {
				walk(val, fmt.Sprintf("%s/%d", at, i))
			}
		}
	}
	walk(doc, "")
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("null value at %s: an unquoted comma split a flow-mapping string; quote the whole string", f)
	}
}

var openAPIPathParamRe = regexp.MustCompile(`\{([^}/]+)\}`)

// TestOpenAPIPathParamsDeclared requires every {name} in a path to be declared
// as an `in: path` parameter, on the path item or on the operation. An
// undeclared one makes the spec invalid, and a generated client cannot build
// the URL.
func TestOpenAPIPathParamsDeclared(t *testing.T) {
	type param struct {
		In   string `yaml:"in"`
		Name string `yaml:"name"`
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openAPIYAML, &doc); err != nil {
		t.Fatalf("parse embedded openapi.yaml: %v", err)
	}
	methods := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true, "options": true}
	var missing []string
	for path, item := range doc.Paths {
		want := openAPIPathParamRe.FindAllStringSubmatch(path, -1)
		if len(want) == 0 {
			continue
		}
		var shared []param
		if n, ok := item["parameters"]; ok {
			if err := n.Decode(&shared); err != nil {
				t.Fatalf("%s: decode path-level parameters: %v", path, err)
			}
		}
		for method, node := range item {
			if !methods[method] {
				continue
			}
			var op struct {
				Parameters []param `yaml:"parameters"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s: decode operation: %v", method, path, err)
			}
			declared := map[string]bool{}
			for _, p := range append(append([]param{}, shared...), op.Parameters...) {
				if p.In == "path" {
					declared[p.Name] = true
				}
			}
			for _, m := range want {
				if !declared[m[1]] {
					missing = append(missing, fmt.Sprintf("%s %s: {%s}", method, path, m[1]))
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("path parameter not declared: %s", m)
	}
}
