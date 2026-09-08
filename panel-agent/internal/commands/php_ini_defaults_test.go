package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1543: php.ini_defaults reports the box php.ini baseline per PHP version so
// the panel can label the per-domain PHP dropdowns with the real inherited
// value. These pin the input guards (the php exec itself needs a real box, so
// it's covered by the live smoke, not here).

func TestPHPIniDefaults_RejectsBadVersion(t *testing.T) {
	for _, v := range []string{"", "8", "8.x", "../8.3", "8.3; rm -rf", "8.3\n"} {
		params, _ := json.Marshal(phpIniDefaultsParams{PHPVersion: v})
		_, err := phpIniDefaultsHandler(context.Background(), params)
		ae, ok := err.(*agentwire.AgentError)
		if !ok || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("version %q: want CodeInvalidArgument, got %v", v, err)
		}
	}
}

func TestPHPIniDefaults_RejectsBadParams(t *testing.T) {
	_, err := phpIniDefaultsHandler(context.Background(), json.RawMessage(`not json`))
	if ae, ok := err.(*agentwire.AgentError); !ok || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("want CodeInvalidArgument on unparseable params, got %v", err)
	}
}

// The PHP -r program must be well-formed and read exactly the directives the
// panel exposes. A hand-glued quote list once dropped the final closing quote,
// yielding a malformed foreach that PHP rejected (exit 255) while this test —
// which only checked the names were present — stayed green. So assert the real
// property instead: the directive list embedded for PHP round-trips back to the
// exact allowlist, and the single-quoted PHP string wrapping it is balanced.
func TestPHPIniDefaults_ScriptIsWellFormed(t *testing.T) {
	script := phpIniReadScript()

	// The list is handed to PHP as a JSON array literal inside a single-quoted
	// string: json_decode('[...]',true). Pull that literal back out and decode
	// it — it must equal the allowlist exactly (order and contents).
	start := strings.Index(script, "json_decode('")
	if start < 0 {
		t.Fatalf("script does not pass the directive list via json_decode: %q", script)
	}
	start += len("json_decode('")
	end := strings.Index(script[start:], "'")
	if end < 0 {
		t.Fatalf("unterminated JSON literal in script: %q", script)
	}
	literal := script[start : start+end]

	var got []string
	if err := json.Unmarshal([]byte(literal), &got); err != nil {
		t.Fatalf("embedded directive literal is not valid JSON (%v): %q", err, literal)
	}
	if len(got) != len(phpIniDefaultDirectives) {
		t.Fatalf("script reads %d directives, want %d: %v", len(got), len(phpIniDefaultDirectives), got)
	}
	for i, d := range phpIniDefaultDirectives {
		if got[i] != d {
			t.Errorf("directive[%d] = %q, want %q", i, got[i], d)
		}
	}

	// Every single quote in the PHP -r program must be paired — an odd count is
	// exactly the malformed-list regression this guards.
	if q := strings.Count(script, "'"); q%2 != 0 {
		t.Errorf("PHP -r program has unbalanced single quotes (%d): %q", q, script)
	}
}
