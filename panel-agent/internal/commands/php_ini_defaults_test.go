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

// The PHP script body must be a well-formed single-quoted list of exactly the
// directives the panel exposes — a drift here would read the wrong ini keys.
func TestPHPIniDefaults_ScriptListsAllDirectives(t *testing.T) {
	body := joinQuoted(phpIniDefaultDirectives)
	for _, d := range phpIniDefaultDirectives {
		if !strings.Contains(body, d) {
			t.Errorf("directive %q missing from the ini-read script list", d)
		}
	}
	if strings.Contains(body, `"`) {
		t.Error("directive list must use single quotes only (PHP -r string)")
	}
}
