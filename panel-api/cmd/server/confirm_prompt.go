package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// A destructive command whose confirmation isn't answered yes changes nothing.
// It used to print "Cancelled." and exit 0, so a script, or a run without a
// terminal where the question reads no answer at all, couldn't tell it from a
// delete that went through. It now fails: the error says nothing was changed
// and names the flag that skips the question, and the command exits 1.

// confirmOrAbort prints prompt and reads one answer from in. y or yes (any
// case) confirms; anything else, including no answer, returns notConfirmed.
func confirmOrAbort(cmd *cobra.Command, in io.Reader, prompt, skipFlag string) error {
	fmt.Print(prompt + " [y/N]: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	if line == "" {
		fmt.Println() // no answer was typed: end the prompt line
	}
	return notConfirmed(cmd, skipFlag)
}

// notConfirmed is the error for a confirmation not answered yes. The command
// line was fine, so the usage text isn't printed with it.
func notConfirmed(cmd *cobra.Command, skipFlag string) error {
	cmd.SilenceUsage = true
	return fmt.Errorf("not confirmed, nothing was changed (pass %s to skip the question)", skipFlag)
}
