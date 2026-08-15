package commands

import (
	"strings"
	"testing"
)

// GH #746: a systemd-run --property flag placed AFTER /usr/local/bin/jabali is
// handed to `jabali migrate import` and dies with "unknown flag: --property",
// which broke every auto-create import (the only path that sets it). The
// EnvironmentFile property must sit before the command.
func TestImportSystemdRunArgs_PropertyBeforeCommand_GH746(t *testing.T) {
	args := importSystemdRunArgs(
		"01KYNJ4TMHTYQH133SKARBZ4R2", "demolab",
		[]string{"--property=EnvironmentFile=/run/jabali-migrate-x.env"},
		[]string{"--target-email=a@b.test"},
	)

	idxProp, idxJabali := -1, -1
	for i, a := range args {
		switch a {
		case "--property=EnvironmentFile=/run/jabali-migrate-x.env":
			idxProp = i
		case "/usr/local/bin/jabali":
			idxJabali = i
		}
	}
	if idxProp == -1 {
		t.Fatalf("--property not present in args: %v", args)
	}
	if idxJabali == -1 {
		t.Fatalf("jabali binary not present in args: %v", args)
	}
	if idxProp > idxJabali {
		t.Errorf("--property (idx %d) must come BEFORE /usr/local/bin/jabali (idx %d); args=%v",
			idxProp, idxJabali, args)
	}
}

func TestImportSystemdRunArgs_NoPropertyWhenNoPassword_GH746(t *testing.T) {
	args := importSystemdRunArgs("01KYNJ4TMHTYQH133SKARBZ4R2", "demolab", nil, nil)
	// GH746's actual invariant: every --property precedes the command —
	// one placed after /usr/local/bin/jabali lands in jabali's argv and
	// dies with "unknown flag". (Since JAB-242 the baseline resource
	// props are ALWAYS present, so "zero properties" is no longer the
	// no-password shape.)
	cmdAt := -1
	for i, a := range args {
		if a == "/usr/local/bin/jabali" {
			cmdAt = i
			break
		}
	}
	if cmdAt < 0 {
		t.Fatalf("command not found in args=%v", args)
	}
	for _, a := range args[cmdAt:] {
		if strings.HasPrefix(a, "--property") {
			t.Errorf("--property after the command lands in jabali argv (GH746); args=%v", args)
		}
	}
	// JAB-242: baseline resource controls are always present, before the command.
	joined := strings.Join(args[:cmdAt], " ")
	for _, want := range []string{"MemoryMax=", "TasksMax=", "CPUWeight=", "IOWeight="} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing resource prop %s before command; args=%v", want, args)
		}
	}
	// The command tail is still well-formed.
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "/usr/local/bin/jabali migrate import --job-id=01KYNJ4TMHTYQH133SKARBZ4R2 --target-user=demolab") {
		t.Errorf("malformed import command: %v", args)
	}
}
