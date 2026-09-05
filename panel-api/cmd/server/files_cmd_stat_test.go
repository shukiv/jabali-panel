package main

import (
	"os"
	"strings"
	"testing"
)

// JAB-340 / AC2: the CLI `files stat` command used to print the raw agent reply
// straight to stdout and exit 0, so a malformed or empty stat body looked like a
// success. It must now validate the reply through the shared fail-closed decoder
// before printing. Source-pin the wiring: DecodeStat(raw) is called, and it runs
// BEFORE the raw bytes are written, so a decode failure short-circuits the print.
func TestFilesStat_DecodesReplyBeforePrinting(t *testing.T) {
	src, err := os.ReadFile("files_cmd.go")
	if err != nil {
		t.Fatalf("read files_cmd.go: %v", err)
	}
	s := string(src)

	decodeIdx := strings.Index(s, "filesops.DecodeStat(raw)")
	if decodeIdx < 0 {
		t.Fatal("files stat must validate the agent reply via filesops.DecodeStat(raw) before printing (JAB-340 AC2)")
	}
	printIdx := strings.Index(s, "os.Stdout.Write(raw)")
	if printIdx < 0 {
		t.Fatal("expected stat to print the raw reply on success")
	}
	if decodeIdx > printIdx {
		t.Fatal("filesops.DecodeStat(raw) must run BEFORE os.Stdout.Write(raw); a malformed reply must not be printed as an exit-0 success")
	}
}
