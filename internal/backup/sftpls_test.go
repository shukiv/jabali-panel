package backup

import (
	"reflect"
	"testing"
)

// TestBuildSSHMkdirArgs_Pin freezes the exact argv buildSSHMkdirArgs produces
// for every auth mode BEFORE the shared-base refactor (JAB-405 Part 2a extracts
// buildSSHConnArgs, shared with the new ls builder). If the extraction changes
// any argv element — most importantly the `--` separator that stops ssh from
// treating the remote command as more of its own options — this pin goes red.
func TestBuildSSHMkdirArgs_Pin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   SFTPInputs
		want []string
	}{
		{
			name: "password auth",
			in:   SFTPInputs{User: "u", Host: "h", Path: "/p", Auth: "password"},
			want: []string{
				"sshpass", "-e", "ssh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "PreferredAuthentications=password",
				"-o", "PubkeyAuthentication=no",
				"u@h", "--", "mkdir", "-p", "/p",
			},
		},
		{
			name: "key auth with key path",
			in:   SFTPInputs{User: "u", Host: "h", Path: "/p", Auth: "key", KeyPath: "/k"},
			want: []string{
				"ssh", "-i", "/k", "-o", "IdentitiesOnly=yes",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "BatchMode=yes",
				"u@h", "--", "mkdir", "-p", "/p",
			},
		},
		{
			name: "default auth (no key, no password)",
			in:   SFTPInputs{User: "u", Host: "h", Path: "/p"},
			want: []string{
				"ssh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "BatchMode=yes",
				"u@h", "--", "mkdir", "-p", "/p",
			},
		},
		{
			name: "non-22 port on key auth",
			in:   SFTPInputs{User: "u", Host: "h", Path: "/p", Auth: "key", KeyPath: "/k", Port: 2222},
			want: []string{
				"ssh", "-i", "/k", "-o", "IdentitiesOnly=yes",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "BatchMode=yes",
				"-p", "2222",
				"u@h", "--", "mkdir", "-p", "/p",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSSHMkdirArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argv mismatch\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

// TestBuildSSHListArgs pins the ls argv: the shared connection prefix, then a
// `--` separator (so ssh stops parsing its own options), then `ls -1` and the
// remote path as ONE argv element — a path with spaces or metacharacters can
// never split into extra arguments. Falsify by dropping the `--`, or by joining
// the remote command into a single string.
func TestBuildSSHListArgs(t *testing.T) {
	t.Parallel()

	got := buildSSHListArgs(
		SFTPInputs{User: "u", Host: "h", Auth: "key", KeyPath: "/k", Port: 2222},
		"/home/puzzle/jabali/keys",
	)
	want := []string{
		"ssh", "-i", "/k", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"-p", "2222",
		"u@h", "--", "ls", "-1", "/home/puzzle/jabali/keys",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ls argv mismatch\n got: %#v\nwant: %#v", got, want)
	}

	// A path with a space stays a single argv element (argv exec, no shell).
	spaced := buildSSHListArgs(SFTPInputs{User: "u", Host: "h"}, "/a b/keys")
	if last := spaced[len(spaced)-1]; last != "/a b/keys" {
		t.Errorf("remote path split into extra args: last argv element = %q", last)
	}
}
