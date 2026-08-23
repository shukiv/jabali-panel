package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-367 (criterion 6): belt-and-suspenders per-command coverage that EVERY
// mutating admin File Manager command refuses an admin_root write outside the
// safe data roots (read_only) or inside the hard deny-list (path_denied). All
// commands route their destination through scope.WriteScope() (JAB-358); this
// pins that wiring per command, not just on files.write. Rejection is at the
// lexical string gate, so these run unprivileged in CI with no filesystem.
func TestAdminFileManager_MutatingCommandsRejectOutOfAllowList(t *testing.T) {
	const (
		// Outside the safe write roots (/home, /var/www, /srv, /tmp, /var/tmp)
		// but NOT in the deny-list → the WriteScope refuses it as read_only.
		sysPath = "/etc/cron.d/jab367-pwn"
		// Inside the hard deny-list (jabali control-plane). For a MUTATION this
		// still surfaces as read_only, not path_denied: WriteScope checks
		// mutable-root membership first, and every deny-list prefix is also
		// outside the write roots, so the read-only gate fires first. (path_denied
		// is the distinct code for READS of a denied path, where the scope is "/".)
		// Either way a deny-list path can never be written — which is the point.
		denyPath = "/etc/jabali/config.toml"
		// A valid upload scratch file (tmpUploadPrefix, no slash after) so the
		// ingest handler reaches its DESTINATION WriteScope check rather than
		// rejecting on tmp_path first.
		okTmp = "/var/lib/jabali-uploads/jabali-upload-jab367test"
		// Lexically valid write-root path — used where a command needs a
		// separate (readable) source so we isolate the DESTINATION rejection.
		okZip = "/tmp/jab367-ok.zip"
	)

	type tc struct {
		name    string
		handler func(context.Context, json.RawMessage) (any, error)
		params  any
		wantSub string // "read_only" | "path_denied"
	}
	adm := func(extra map[string]any) map[string]any {
		m := map[string]any{"user_id": "admin", "username": "admin", "admin_root": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	cases := []tc{
		// read_only: a system path outside the safe roots, per mutating command.
		{"write→read_only", filesWriteHandler, adm(map[string]any{"path": sysPath, "content": "x"}), "read_only"},
		{"mkdir→read_only", filesMkdirHandler, adm(map[string]any{"path": sysPath}), "read_only"},
		{"delete→read_only", filesDeleteHandler, adm(map[string]any{"path": sysPath}), "read_only"},
		{"chmod→read_only", filesChmodHandler, adm(map[string]any{"path": sysPath, "mode": "0644"}), "read_only"},
		{"move→read_only", filesMoveHandler, adm(map[string]any{"old_path": sysPath, "new_path": sysPath + ".moved"}), "read_only"},
		{"rename→read_only", filesRenameHandler, adm(map[string]any{"old_path": sysPath, "new_path": "/etc/cron.d/jab367-renamed"}), "read_only"},
		{"copy→read_only", filesCopyHandler, adm(map[string]any{"src_path": sysPath, "dst_path": sysPath + ".copy"}), "read_only"},
		{"ingest→read_only", filesIngestHandler, adm(map[string]any{"tmp_path": okTmp, "dest_path": sysPath}), "read_only"},
		// extract: archive read is fine (valid write-root path); the DESTINATION
		// is the mutation and must be refused.
		{"extract dest→read_only", filesExtractHandler, adm(map[string]any{"path": okZip, "dest": sysPath}), "read_only"},

		// A deny-list control-plane path can never be written by any command
		// (surfaces as read_only for a mutation — see denyPath comment above).
		{"write deny-list refused", filesWriteHandler, adm(map[string]any{"path": denyPath, "content": "x"}), "read_only"},
		{"delete deny-list refused", filesDeleteHandler, adm(map[string]any{"path": denyPath}), "read_only"},
		{"chmod deny-list refused", filesChmodHandler, adm(map[string]any{"path": denyPath, "mode": "0777"}), "read_only"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params, err := json.Marshal(c.params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			_, callErr := c.handler(context.Background(), params)
			if callErr == nil {
				t.Fatalf("%s: expected the mutation to be refused, got nil error", c.name)
			}
			var ae *agentwire.AgentError
			if !isAgentError(callErr, &ae) {
				t.Fatalf("%s: want an *agentwire.AgentError, got %T: %v", c.name, callErr, callErr)
			}
			if ae.Code != agentwire.CodeInvalidArgument {
				t.Errorf("%s: want code %q, got %q", c.name, agentwire.CodeInvalidArgument, ae.Code)
			}
			if !strings.Contains(callErr.Error(), c.wantSub) {
				t.Errorf("%s: want error containing %q, got %q", c.name, c.wantSub, callErr.Error())
			}
		})
	}
}
