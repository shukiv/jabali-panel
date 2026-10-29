package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// renderCLIReference walks the live Cobra command tree and emits a canonical
// Markdown reference (Gitea #536). The committed reference is regenerated from
// this and guarded by a drift test, so the docs can never silently diverge from
// the actual `jabali` binary's commands, flags, and defaults.
//
// Deterministic: subcommands sorted by name, flags sorted by name — so the
// golden file is stable across runs.
func renderCLIReference(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("# Jabali CLI reference\n\n")
	b.WriteString("> Generated from the Cobra command tree — do not edit by hand.\n")
	b.WriteString("> Regenerate with `go test ./panel-api/cmd/server -run TestCLIReferenceGolden -update`.\n\n")
	writeCLICommand(&b, root)
	return b.String()
}

func writeCLICommand(b *strings.Builder, cmd *cobra.Command) {
	if cmd.Hidden {
		return
	}
	depth := strings.Count(cmd.CommandPath(), " ")
	level := depth + 2 // root = ##
	if level > 6 {
		level = 6
	}
	fmt.Fprintf(b, "%s `%s`\n\n", strings.Repeat("#", level), cmd.CommandPath())
	if s := strings.TrimSpace(cmd.Short); s != "" {
		fmt.Fprintf(b, "%s\n\n", s)
	}
	if cmd.Runnable() {
		fmt.Fprintf(b, "```\n%s\n```\n\n", strings.TrimSpace(cmd.UseLine()))
	}
	writeCLIFlags(b, "Flags", cmd.LocalFlags())

	for _, c := range visibleSubcommands(cmd) {
		writeCLICommand(b, c)
	}
}

// visibleSubcommands returns cmd's subcommands without hidden ones and
// Cobra's own help/completion, name-sorted.
func visibleSubcommands(cmd *cobra.Command) []*cobra.Command {
	subs := make([]*cobra.Command, 0, len(cmd.Commands()))
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		subs = append(subs, c)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
	return subs
}

// cliSummaryWidth is the column the README's command list wraps at.
const cliSummaryWidth = 80

// renderCLICommandSummary emits the README's command list: one entry per
// top-level command with its subcommands, wrapped at cliSummaryWidth. A
// command without subcommands shows its short description instead. A drift
// test keeps the README block equal to this, so a new command is listed there
// as well as in the full reference.
func renderCLICommandSummary(root *cobra.Command) string {
	tops := visibleSubcommands(root)
	nameWidth := 0
	for _, c := range tops {
		if n := len(c.Name()); n > nameWidth {
			nameWidth = n
		}
	}
	col := len("jabali ") + nameWidth + 2

	var b strings.Builder
	for _, c := range tops {
		line := fmt.Sprintf("%-*s", col, "jabali "+c.Name())
		subs := visibleSubcommands(c)
		if len(subs) == 0 {
			fmt.Fprintf(&b, "%s# %s\n", line, strings.TrimSpace(c.Short))
			continue
		}
		for i, s := range subs {
			word := s.Name()
			if i < len(subs)-1 {
				word += "|"
			}
			if len(line)+len(word) > cliSummaryWidth && len(line) > col {
				b.WriteString(line + "\n")
				line = strings.Repeat(" ", col)
			}
			line += word
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func writeCLIFlags(b *strings.Builder, heading string, fs *pflag.FlagSet) {
	var flags []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		flags = append(flags, f)
	})
	if len(flags) == 0 {
		return
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	fmt.Fprintf(b, "**%s:**\n\n", heading)
	for _, f := range flags {
		def := ""
		if f.DefValue != "" && f.DefValue != "false" {
			def = fmt.Sprintf(" (default `%s`)", f.DefValue)
		}
		fmt.Fprintf(b, "- `--%s` — %s%s\n", f.Name, strings.TrimSpace(f.Usage), def)
	}
	b.WriteString("\n")
}
