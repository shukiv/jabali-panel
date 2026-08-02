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
