package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func pinDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := nspawnPinDir
	nspawnPinDir = dir
	t.Cleanup(func() { nspawnPinDir = prev })
	return dir
}

// A panel username may contain `_`. The handler validated it with the image
// rule ([a-z0-9-]+) and refused such users, so they never got a pin.
func TestWriteNspawnPin_AcceptsAnUnderscoreUsername(t *testing.T) {
	dir := pinDir(t)
	raw, _ := json.Marshal(sshUserWriteNspawnPinParams{Username: "sr_a_891", Image: "debian-13"})
	if _, err := sshUserWriteNspawnPinHandler(context.Background(), raw); err != nil {
		t.Fatalf("pin for sr_a_891: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sr_a_891", "nspawn-image"))
	if err != nil || string(got) != "debian-13\n" {
		t.Fatalf("pin = %q, %v", got, err)
	}
}

// Names that are not account names, above all anything that could leave the
// pin directory, are refused before any file is touched.
func TestWriteNspawnPin_RefusesNamesThatAreNotAccounts(t *testing.T) {
	dir := pinDir(t)
	for _, name := range []string{"", "../etc", "a/b", "Alice", ".hidden", "a.b", "1abc"} {
		raw, _ := json.Marshal(sshUserWriteNspawnPinParams{Username: name, Image: "debian-13"})
		if _, err := sshUserWriteNspawnPinHandler(context.Background(), raw); err == nil {
			t.Fatalf("%q accepted", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("pin dir not empty: %v", entries)
	}
}

// An empty image removes the pin.
func TestWriteNspawnPin_EmptyImageRemovesThePin(t *testing.T) {
	dir := pinDir(t)
	raw, _ := json.Marshal(sshUserWriteNspawnPinParams{Username: "sr_a_891", Image: "debian-13"})
	if _, err := sshUserWriteNspawnPinHandler(context.Background(), raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, _ = json.Marshal(sshUserWriteNspawnPinParams{Username: "sr_a_891", Image: ""})
	if _, err := sshUserWriteNspawnPinHandler(context.Background(), raw); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sr_a_891", "nspawn-image")); !os.IsNotExist(err) {
		t.Fatalf("pin still present: %v", err)
	}
}
