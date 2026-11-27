package tui

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// installWaitDelay bounds the wait for install.sh's output to close once it
// has exited. A var so tests can shorten it.
var installWaitDelay = 3 * time.Second

// Streaming messages from the install goroutine.
type logLineMsg string
type installDoneMsg struct{ err error }

// startInstall spawns install.sh (with the selected JABALI_MODULES) and streams
// its combined stdout+stderr line-by-line onto `events`, then a final
// installDoneMsg. Returned as a tea.Cmd so it runs off the UI goroutine.
func startInstall(installSh string, extraEnv []string, dryRun bool, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go func() {
			args := []string{installSh}
			if dryRun {
				args = append(args, "--dry-run")
			}
			cmd := exec.Command("bash", args...)
			cmd.Env = append(os.Environ(), extraEnv...)
			pr, pw := io.Pipe()
			cmd.Stdout, cmd.Stderr = pw, pw
			// A process install.sh started can keep its output open after
			// install.sh exits. Waiting for that EOF left the screen on its
			// last line forever, the error unseen (GH #2590). Bound it.
			cmd.WaitDelay = installWaitDelay
			if err := cmd.Start(); err != nil {
				events <- installDoneMsg{err: err}
				return
			}
			waitErr := make(chan error, 1)
			go func() {
				err := cmd.Wait()
				pw.Close()
				waitErr <- err
			}()
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for sc.Scan() {
				events <- logLineMsg(sc.Text())
			}
			// Keep reading past an over-long line: a stalled reader blocks
			// install.sh on its next write.
			_, _ = io.Copy(io.Discard, pr)
			err := <-waitErr
			if errors.Is(err, exec.ErrWaitDelay) {
				err = nil // install.sh exited 0; only a leftover child held the output
			}
			events <- installDoneMsg{err: err}
		}()
		// Immediately hand back the first waiter so Update starts draining.
		return waitForEvent(events)()
	}
}

// waitForEvent blocks on the events channel and returns the next message; the
// model re-arms it after every non-terminal event so streaming continues.
func waitForEvent(events chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

// phaseFromLine extracts a human phase label from install.sh's own markers so
// the header can show "current step" without us re-listing every step. install.sh
// logs `[i] <msg>` (start) and `[✓] <msg>` (ok); we surface the latest `[i]`.
func phaseFromLine(line string) (string, bool) {
	s := stripANSI(line)
	if i := strings.Index(s, "[i] "); i >= 0 {
		return strings.TrimSpace(s[i+4:]), true
	}
	return "", false
}

// stripANSI removes the SGR color escapes install.sh emits, so the parsed phase
// + the log pane render cleanly inside the box.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// aptProgress parses apt's machine-readable progress lines (emitted when
// install.sh runs the long apt step with -o APT::Status-Fd=1 under the TUI):
//
//	dlstatus:<id>:<pct>:<desc>   during download (pct 0..100)
//	pmstatus:<pkg>:<pct>:<desc>  during unpack/configure (pct 0..100)
//
// Returns the intra-phase creep (0..1) — download mapped to the first half,
// install to the second — and ok=true so the caller drives the bar with the
// real percentage and hides the line from the log. Non-status lines → ok=false.
func aptProgress(line string) (float64, bool) {
	var half float64
	switch {
	case strings.HasPrefix(line, "dlstatus:"):
		half = 0.0
	case strings.HasPrefix(line, "pmstatus:"):
		half = 0.5
	default:
		return 0, false
	}
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 3 {
		return 0, true // a status line, just no usable pct → hide it anyway
	}
	pct, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil {
		return 0, true
	}
	c := half + (pct/100.0)*0.5
	if c > 0.98 {
		c = 0.98
	}
	return c, true
}
