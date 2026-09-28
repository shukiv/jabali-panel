package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const docsSiteDir = "../../../docs/site"

var (
	// A fenced-block line that runs the CLI: optional prompt or sudo, then "jabali ".
	docsCLILineRe = regexp.MustCompile(`^\s*(?:\$\s+|#\s+)?(?:sudo\s+)?(jabali\s.*)$`)
	// An inline code span that starts with the CLI name.
	docsCLISpanRe  = regexp.MustCompile("`(jabali\\s[^`]+)`")
	docsCLIWordRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	docsCLIFlagRe  = regexp.MustCompile(`^--?([A-Za-z0-9][A-Za-z0-9-]*)`)
	docsCLIStopTok = map[string]bool{"|": true, "||": true, "&&": true, ";": true, ">": true, ">>": true, "2>": true, "<": true, "#": true}
)

// TestDocsCLICommandsExist resolves every `jabali …` command the published docs
// tell an operator to run against the live Cobra tree. A doc that names a
// subcommand or flag the binary does not have fails here, instead of failing
// on a real box with "unknown command" in the middle of a recovery.
func TestDocsCLICommandsExist(t *testing.T) {
	root := newRootCmd()
	var problems []string
	err := filepath.WalkDir(docsSiteDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") || filepath.Base(path) == "cli-reference.md" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		rel, _ := filepath.Rel(docsSiteDir, path)
		sc := bufio.NewScanner(f)
		inFence, lineNo := false, 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			var cmds []string
			if inFence {
				if m := docsCLILineRe.FindStringSubmatch(line); m != nil {
					cmds = append(cmds, m[1])
				}
			} else {
				for _, m := range docsCLISpanRe.FindAllStringSubmatch(line, -1) {
					cmds = append(cmds, m[1])
				}
			}
			for _, c := range cmds {
				if msg := checkDocsCLICommand(root, c); msg != "" {
					problems = append(problems, rel+":"+strconv.Itoa(lineNo)+": "+msg)
				}
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatalf("walk %s: %v", docsSiteDir, err)
	}
	if len(problems) > 0 {
		t.Errorf("%d doc command(s) the jabali binary does not have (see docs/site/platform/cli-reference.md):\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

// checkDocsCLICommand returns "" when the command line resolves, or a message
// naming the first subcommand or flag that does not exist.
func checkDocsCLICommand(root *cobra.Command, line string) string {
	toks := splitDocsCLIWords(line)[1:] // drop "jabali"
	cmd := root
	i := 0
	for ; i < len(toks); i++ {
		tok := toks[i]
		if !docsCLIWordRe.MatchString(tok) {
			break
		}
		sub := findDocsSubcommand(cmd, tok)
		if sub == nil {
			// A command with subcommands takes positional words only when
			// it declares an Args validator (the root never does), so an
			// unknown word under any other parent is a missing subcommand.
			if cmd.HasSubCommands() && (cmd == root || cmd.Args == nil) {
				return "`" + line + "`: `" + cmd.CommandPath() + "` has no subcommand `" + tok + "`"
			}
			break
		}
		cmd = sub
	}
	cmd.InitDefaultHelpFlag()
	for ; i < len(toks); i++ {
		tok := strings.TrimLeft(toks[i], "[(")
		if docsCLIStopTok[tok] {
			break
		}
		m := docsCLIFlagRe.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		name := m[1]
		if strings.HasPrefix(tok, "--") {
			if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil && !(name == "version" && cmd == root) {
				return "`" + line + "`: `" + cmd.CommandPath() + "` has no flag `--" + name + "`"
			}
			continue
		}
		if len(name) == 1 && cmd.Flags().ShorthandLookup(name) == nil && cmd.InheritedFlags().ShorthandLookup(name) == nil {
			return "`" + line + "`: `" + cmd.CommandPath() + "` has no flag `-" + name + "`"
		}
	}
	return ""
}

func findDocsSubcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}

// splitDocsCLIWords splits a command line on spaces, keeping a quoted value
// as one word so the flags inside `--command "wp … --url=…"` are not read as
// flags of the jabali command.
func splitDocsCLIWords(line string) []string {
	var words []string
	var cur strings.Builder
	var quote rune
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}
