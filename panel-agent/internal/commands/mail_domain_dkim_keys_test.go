package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dkimKeyFiles are every key file a mail domain can have: the keys, the
// copies a rotation keeps, and a rotation's staged key.
var dkimKeyFiles = []string{".key", ".rsa.key", ".key.old", ".rsa.key.old", ".key.new", ".rsa.key.new"}

// writeKeyFiles creates each name in dir with its own name as content.
func writeKeyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func domainKeyFiles(domain string) []string {
	var out []string
	for _, s := range dkimKeyFiles {
		out = append(out, domain+s)
	}
	return out
}

// listDir returns dir's file names, sorted.
func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// Neighbours whose names share a prefix or suffix with example.com: deleting
// or renaming example.com must not touch their keys.
var neighbourKeys = []string{"sub.example.com.key", "example.com.au.key", "other.com.key", "other.com.rsa.key"}

// Deleting a domain removes its key files: left behind, the next owner of
// the same name would sign with the old owner's key (enable reuses a key
// file it finds).
func TestMailDomainPurge_RemoveDomainRemovesItsKeyFiles(t *testing.T) {
	dir := wireDKIMDir(t)
	writeKeyFiles(t, dir, domainKeyFiles("example.com")...)
	writeKeyFiles(t, dir, neighbourKeys...)
	f := newMGFake()
	f.dkim["sig1"] = "dom1"
	wireJMAP(t, f.server(t))

	out, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com","remove_domain":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); got != "example.com.au.key other.com.key other.com.rsa.key sub.example.com.key" {
		t.Fatalf("key dir = %q", got)
	}
	if got := out.(mailDomainPurgeResult).DKIMKeysRemoved; got != len(dkimKeyFiles) {
		t.Errorf("dkim_keys_removed = %d, want %d", got, len(dkimKeyFiles))
	}
}

// A domain the mail server never registered can still have key files.
func TestMailDomainPurge_RemoveDomainNotInRegistryRemovesItsKeyFiles(t *testing.T) {
	dir := wireDKIMDir(t)
	writeKeyFiles(t, dir, "gone.com.key", "gone.com.rsa.key", "other.com.key")
	f := newMGFake()
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"gone.com","remove_domain":true}`)); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); got != "other.com.key" {
		t.Fatalf("key dir = %q", got)
	}
}

// A mail-only purge keeps the keys: re-enabling mail signs with them again
// (ADR-0043; domain.email_disable keeps them too).
func TestMailDomainPurge_MailOnlyKeepsKeyFiles(t *testing.T) {
	dir := wireDKIMDir(t)
	writeKeyFiles(t, dir, "example.com.key", "example.com.rsa.key")
	f := newMGFake()
	f.dkim["sig1"] = "dom1"
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com"}`)); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); got != "example.com.key example.com.rsa.key" {
		t.Fatalf("key dir = %q", got)
	}
}

// A signature the mail server would not destroy still signs with its key:
// the files stay, so the panel can still publish and rotate it.
func TestMailDomainPurge_KeyFilesStayWhileASignatureRemains(t *testing.T) {
	dir := wireDKIMDir(t)
	writeKeyFiles(t, dir, "example.com.key", "example.com.rsa.key")
	f := newMGFake()
	f.dkim["sig1"] = "dom1"
	f.keepDkim = map[string]bool{"sig1": true}
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com","remove_domain":true}`)); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); got != "example.com.key example.com.rsa.key" {
		t.Fatalf("key dir = %q", got)
	}
}

// A rename keeps the mail server's signature (and its key) on the renamed
// domain, so the key files follow the name: under the old name, Rotate DKIM
// found no key and a mail re-enable made a new, unsigned one.
func TestMailDomainRename_MovesKeyFiles(t *testing.T) {
	for _, status := range []string{"renamed", "already", "not_in_registry"} {
		t.Run(status, func(t *testing.T) {
			dir := wireDKIMDir(t)
			writeKeyFiles(t, dir, "old.com.key", "old.com.rsa.key", "old.com.key.old", "old.com.rsa.key.old", "sub.old.com.key")
			// Stale keys under the new name (a domain of that name deleted
			// before key files were removed) are replaced, or removed when
			// the renamed domain has no such file.
			writeKeyFiles(t, dir, "new.com.key", "new.com.rsa.key.new")
			f := &fakeStalwart{domains: map[string]string{"old.com": "o1"}, catchall: map[string]string{"o1": ""}}
			switch status {
			case "already":
				f.domains = map[string]string{"new.com": "o1"}
			case "not_in_registry":
				f.domains = map[string]string{}
			}

			res := callRename(t, f, "old.com", "new.com", false)
			if res.Status != status {
				t.Fatalf("status = %q", res.Status)
			}
			if got := listDir(t, dir); got != "new.com.key new.com.key.old new.com.rsa.key new.com.rsa.key.old sub.old.com.key" {
				t.Fatalf("key dir = %q", got)
			}
			if got := readFile(t, filepath.Join(dir, "new.com.key")); got != "old.com.key" {
				t.Errorf("new.com.key holds %q, want the renamed domain's key", got)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings = %v", res.Warnings)
			}
		})
	}
}

// A retried rename (the panel's own retry after its DB update failed) finds
// the domain already renamed on the mail server and its keys already under
// the new name: they stay. A move cut short moved the Ed25519 key first; the
// retry moves the rest and removes nothing.
func TestMailDomainRename_RetryKeepsMovedKeyFiles(t *testing.T) {
	for name, tc := range map[string]struct{ before, want string }{
		"finished": {
			"new.com.key new.com.key.old new.com.rsa.key",
			"new.com.key new.com.key.old new.com.rsa.key",
		},
		"cut short": {
			"new.com.key old.com.key.old old.com.rsa.key",
			"new.com.key new.com.key.old new.com.rsa.key",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := wireDKIMDir(t)
			writeKeyFiles(t, dir, strings.Fields(tc.before)...)
			f := &fakeStalwart{domains: map[string]string{"new.com": "o1"}, catchall: map[string]string{"o1": ""}}

			res := callRename(t, f, "old.com", "new.com", false)
			if res.Status != "already" {
				t.Fatalf("status = %q", res.Status)
			}
			if got := listDir(t, dir); got != tc.want {
				t.Fatalf("key dir = %q, want %q", got, tc.want)
			}
			if got := readFile(t, filepath.Join(dir, "new.com.key")); got != "new.com.key" {
				t.Errorf("new.com.key holds %q, want the key it had", got)
			}
		})
	}
}

// A dry run and a refused rename move nothing.
func TestMailDomainRename_DryRunAndConflictKeepKeyFiles(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		dir := wireDKIMDir(t)
		writeKeyFiles(t, dir, "old.com.key")
		f := &fakeStalwart{domains: map[string]string{"old.com": "o1"}, catchall: map[string]string{"o1": ""}}
		if res := callRename(t, f, "old.com", "new.com", true); res.Status != "ok" {
			t.Fatalf("status = %q", res.Status)
		}
		if got := listDir(t, dir); got != "old.com.key" {
			t.Fatalf("key dir = %q", got)
		}
	})
	// The not_in_registry and already answers come before the dry-run
	// check, so they must not move files on a dry run either.
	t.Run("dry run, not in registry", func(t *testing.T) {
		dir := wireDKIMDir(t)
		writeKeyFiles(t, dir, "old.com.key", "new.com.rsa.key")
		f := &fakeStalwart{domains: map[string]string{}}
		if res := callRename(t, f, "old.com", "new.com", true); res.Status != "not_in_registry" {
			t.Fatalf("status = %q", res.Status)
		}
		if got := listDir(t, dir); got != "new.com.rsa.key old.com.key" {
			t.Fatalf("key dir = %q", got)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		dir := wireDKIMDir(t)
		writeKeyFiles(t, dir, "old.com.key", "new.com.key")
		f := &fakeStalwart{
			domains:  map[string]string{"old.com": "o1", "new.com": "n1"},
			catchall: map[string]string{"o1": "", "n1": ""},
			accounts: map[string]string{"a1": "n1"},
		}
		if res := callRename(t, f, "old.com", "new.com", false); res.Status != "conflict" {
			t.Fatalf("status = %q", res.Status)
		}
		if got := listDir(t, dir); got != "new.com.key old.com.key" {
			t.Fatalf("key dir = %q", got)
		}
	})
}

// The domain name becomes part of a path: a name that is not a plain domain
// name removes nothing.
func TestMailDomainPurge_RemoveDomainSkipsKeyFilesForABadName(t *testing.T) {
	dir := wireDKIMDir(t)
	outside := filepath.Join(filepath.Dir(dir), "escape.key")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newMGFake()
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"../escape","remove_domain":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the key directory was removed: %v", err)
	}
}
