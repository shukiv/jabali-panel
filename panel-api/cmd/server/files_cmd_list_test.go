package main

import (
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/filesops"
)

// TestRenderFilesList_MalformedReply_FailsClosed is the JAB-340 AC2 regression
// guard for the CLI file manager. The `files list` command used to decode the
// agent reply with `_ = json.Unmarshal(...)`, so a malformed body printed an
// empty listing and exited 0 — an agent failure looked like an empty directory.
// The command now renders through renderFilesList, which decodes via filesops
// and fails closed. This test pins that: revert the decode to a swallow and it
// breaks.
func TestRenderFilesList_MalformedReply_FailsClosed(t *testing.T) {
	if err := renderFilesList(json.RawMessage(`{"entries": [ this is not json`), false); err == nil {
		t.Fatal("malformed files.list reply must return an error, not print an empty listing as success")
	}
}

// TestRenderFilesList_WellFormedReply_Succeeds keeps the guard honest: a valid
// reply still renders without error, so the fail-closed check above is catching
// malformed input rather than rejecting everything.
func TestRenderFilesList_WellFormedReply_Succeeds(t *testing.T) {
	raw, err := json.Marshal(filesops.ListResult{
		Path:    "/home/alice",
		Entries: []filesops.ListEntry{{Name: "notes.txt", Size: 12, Mode: "-rw-r--r--"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := renderFilesList(raw, false); err != nil {
		t.Fatalf("well-formed files.list reply must render without error: %v", err)
	}
}
