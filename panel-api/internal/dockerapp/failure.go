package dockerapp

import "strings"

// FailureSummary shortens an agent install error to the one line an app's
// status keeps. The agent's first line is only "docker compose up -d failed:
// exit status 1"; docker compose's own last lines follow it (composeFailMessage
// in the agent), and the first of those that names an error, such as a refused
// image pull, is the reason. It is kept after the first line.
func FailureSummary(msg string) string {
	first, rest, ok := strings.Cut(msg, "\n")
	if !ok {
		return msg
	}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		l := strings.ToLower(line)
		if !strings.Contains(l, "error") && !strings.Contains(l, "denied") {
			continue
		}
		if r := []rune(line); len(r) > 300 {
			line = string(r[:300]) + "…"
		}
		return first + ": " + line
	}
	return first
}
