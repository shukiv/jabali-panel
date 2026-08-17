package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// QA 2026-06-22 regressions:
//   - duplicate sibling Use names silently shadow a command tree (the `docker`
//     dup that hid the engine subcommands).
//   - grouping commands (subcommands, no Run) printed help + exit 0 on an
//     unknown subcommand; rejectUnknownSubcommands now makes them error.
func TestCLITree_NoDuplicateSiblingNames(t *testing.T) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		seen := map[string]bool{}
		for _, sub := range c.Commands() {
			name := strings.Fields(sub.Use)[0]
			if seen[name] {
				t.Errorf("duplicate sibling command %q under %q — one tree is shadowed", name, path)
			}
			seen[name] = true
			walk(sub, path+" "+name)
		}
	}
	walk(newRootCmd(), "jabali")
}

// JAB-272: these DB/Kratos-touching subcommands MUST bootstrap their shared
// state via a PreRunE. They don't inline-init (unlike the `*Direct` helper
// commands such as `user delete` / `domain list`), so a missing PreRunE leaves
// sharedDB/sharedCfg nil — `user suspend` and `session revoke-user` SIGSEGV on
// the nil *gorm.DB, and `session list/revoke` fail with "config not loaded".
// Pin the exact commands that regressed rather than a blanket rule (which would
// false-positive on the legitimately inline-initialising commands).
func TestCLITree_StatefulCommandsHavePreRunE(t *testing.T) {
	paths := [][]string{
		{"user", "suspend"},
		{"user", "unsuspend"},
		{"session", "list"},
		{"session", "revoke"},
		{"session", "revoke-user"},
	}
	root := newRootCmd()
	for _, p := range paths {
		cmd, _, err := root.Find(p)
		if err != nil {
			t.Errorf("command %q not found in tree: %v", strings.Join(p, " "), err)
			continue
		}
		// Find returns the nearest match; confirm it resolved to the leaf and
		// not a parent (a typo'd path would stop at the group).
		if got := strings.Fields(cmd.Use)[0]; got != p[len(p)-1] {
			t.Errorf("command %q resolved to %q, not the intended leaf", strings.Join(p, " "), got)
			continue
		}
		if cmd.PreRunE == nil {
			t.Errorf("command %q has no PreRunE — it will run on nil shared state (JAB-272)", strings.Join(p, " "))
		}
	}
}

// Every grouping command (has subcommands, no Run/RunE author-set) must be made
// runnable by rejectUnknownSubcommands so an unknown subcommand errors instead
// of help+exit0. After building the root, no group should be left non-runnable.
func TestCLITree_GroupingCommandsRejectUnknown(t *testing.T) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if c.HasSubCommands() && !c.Runnable() {
			t.Errorf("grouping command %q is not runnable — unknown subcommands will help+exit0", path)
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+strings.Fields(sub.Use)[0])
		}
	}
	walk(newRootCmd(), "jabali")
}
