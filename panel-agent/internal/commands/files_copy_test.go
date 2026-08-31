package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1392: files.copy.start must reject bad input SYNCHRONOUSLY (immediate
// 400), before registering a job — a user error should never become a job that
// instantly fails and burns a per-user slot.
func TestFilesCopyStart_ValidatesBeforeJob(t *testing.T) {
	resetFileJobs()
	cases := []struct {
		name   string
		params string
	}{
		{"missing username", `{"src_path":"/home/u/a","dst_path":"/home/u/b"}`},
		{"missing src", `{"username":"u","dst_path":"/home/u/b"}`},
		{"missing dst", `{"username":"u","src_path":"/home/u/a"}`},
		{"bad json", `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := filesCopyStartHandler(context.Background(), json.RawMessage(c.params))
			if err == nil {
				t.Fatalf("want error, got out=%v", out)
			}
			ae, ok := err.(*agentwire.AgentError)
			if !ok || ae.Code != agentwire.CodeInvalidArgument {
				t.Fatalf("want InvalidArgument AgentError, got %T %v", err, err)
			}
		})
	}
	// None of the rejected requests may have registered a job.
	fileJobs.mu.Lock()
	n := len(fileJobs.m)
	fileJobs.mu.Unlock()
	if n != 0 {
		t.Fatalf("validation failures must not create jobs, got %d", n)
	}
}
