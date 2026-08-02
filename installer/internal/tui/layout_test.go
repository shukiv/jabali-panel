package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ansiSeq matches any CSI escape sequence, not just SGR — the frame also
// carries cursor-visibility codes like ESC[?25l which are not colour changes.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// bgAfter walks one SGR parameter list and reports the background state after
// it, given the state before.
//
// Parameters must be consumed as UNITS: 38;2;r;g;b and 48;2;r;g;b are five
// parameters each, and their r/g/b components are ordinary numbers. Splitting
// on ";" and looking for "0" reads the zeros inside 48;2;0;0;0 (black) as
// resets — which is exactly the mistake this helper exists to avoid.
func bgAfter(params string, had bool) (hasBG, reset bool) {
	if params == "" {
		return false, true // ESC[m is ESC[0m
	}
	f := strings.Split(params, ";")
	hasBG = had
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "0":
			hasBG, reset = false, true
		case "38", "48":
			isBG := f[i] == "48"
			if i+1 < len(f) && f[i+1] == "2" {
				i += 4 // ;2;r;g;b
			} else if i+1 < len(f) && f[i+1] == "5" {
				i += 2 // ;5;n
			}
			if isBG {
				hasBG = true
			}
		case "49":
			hasBG = false
		default:
			if len(f[i]) == 2 && f[i][0] == '4' && f[i][1] >= '0' && f[i][1] <= '7' {
				hasBG = true
			}
		}
	}
	return hasBG, reset
}

// termSize is a terminal geometry worth rendering correctly.
var termSizes = []struct {
	name          string
	width, height int
}{
	{"80x24 default", 80, 24},
	{"100x30 windowed", 100, 30},
	{"200x50 maximised", 200, 50},
	{"60x20 cramped", 60, 20},
}

func allScreens() []struct {
	name  string
	setup func(*Model)
} {
	return []struct {
		name  string
		setup func(*Model)
	}{
		{"welcome", func(m *Model) { m.screen = screenWelcome }},
		{"profile", func(m *Model) { m.screen = screenProfile }},
		{"modules", func(m *Model) { m.screen = screenModules }},
		{"config", func(m *Model) { m.screen = screenConfig }},
		{"confirm", func(m *Model) { m.screen = screenConfirm }},
		{"installing", func(m *Model) {
			m.screen = screenInstalling
			m.logHidden = false // exercise the log pane; it defaults to hidden
			m.phase = "apt install crowdsec (upstream, try 1/5) with a deliberately long phase name"
			m.logLines = []string{
				"Get:1 http://deb.debian.org/debian trixie InRelease [genuinely quite a long apt line]",
				"Unpacking libfoo:amd64 (1.2.3-4) over (1.2.3-3) …",
			}
		}},
		{"installing log hidden", func(m *Model) {
			m.screen = screenInstalling
			m.logHidden = true
			m.phase = "apt install system packages"
		}},
		{"result", func(m *Model) { m.screen = screenResult }},
	}
}

// render builds a model at the given size the way bubbletea does — a
// WindowSizeMsg first, so size-derived widths are recomputed — and returns the
// frame.
func render(t *testing.T, w, h int, setup func(*Model)) string {
	t.Helper()
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := New("/tmp/install.sh", true)
	updated, _ := m.Update(windowSize(w, h))
	m = updated.(Model)
	setup(&m)
	return m.View()
}

// unbackgroundedRuns returns visible text that paints on the TERMINAL's default
// background instead of the app theme, walking the frame the way a terminal
// does: SGR is cumulative and ESC[0m clears everything.
func unbackgroundedRuns(frame string) []string {
	var bad []string
	for _, line := range strings.Split(frame, "\n") {
		hasBG := false
		var run strings.Builder
		flush := func() {
			if s := strings.TrimSpace(run.String()); s != "" {
				bad = append(bad, s)
			}
			run.Reset()
		}
		for i := 0; i < len(line); {
			if loc := ansiSeq.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
				seq := line[i : i+loc[1]]
				if strings.HasSuffix(seq, "m") { // SGR; everything else is not colour
					next, reset := bgAfter(seq[2:len(seq)-1], hasBG)
					if reset {
						flush()
					}
					hasBG = next
				}
				i += loc[1]
				continue
			}
			r, size := firstRune(line[i:])
			i += size
			if !hasBG {
				run.WriteRune(r)
			}
		}
		flush()
	}
	return bad
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

// TestScreensPaintTheirOwnBackground pins the defect that made the installer
// look broken on a light terminal.
//
// The theme is one outer lipgloss style carrying Background(bgDark), and
// everything drawn inside was assumed to inherit it. It does not: every nested
// style lipgloss renders terminates with ESC[0m, which clears the background
// along with the colour it meant to end, so all text after the first nested
// style on a line paints on the terminal's default background — a white slab
// through the UI. Observed on "Installing…", the phase text, the focused
// field's label and the whole PHP-version row. The tell was that UNFOCUSED
// rows were fine: they use a plain "  " cursor with no nested style, so
// nothing reset the background.
func TestScreensPaintTheirOwnBackground(t *testing.T) {
	for _, sz := range termSizes {
		for _, sc := range allScreens() {
			t.Run(sz.name+"/"+sc.name, func(t *testing.T) {
				bad := unbackgroundedRuns(render(t, sz.width, sz.height, sc.setup))
				if len(bad) > 0 {
					if len(bad) > 5 {
						bad = bad[:5]
					}
					t.Errorf("text paints on the terminal's default background rather "+
						"than the theme — on a light terminal these are white blocks "+
						"through the UI: %q", bad)
				}
			})
		}
	}
}

// TestScreensFitTheTerminal is the responsiveness half.
//
// Measured on the content BEFORE the full-terminal container, because the
// container hides the damage: Width()/Height() wrap long lines and truncate
// excess rows, so an overflowing layout still renders as an exactly-sized
// frame — the bottom of the UI just silently goes missing. Asserting on the
// finished frame therefore proves nothing, which is how a 46-column progress
// bar, 76-column log lines and a 16-row log pane survived in a UI that has to
// work in an 80x24 window.
func TestScreensFitTheTerminal(t *testing.T) {
	for _, sz := range termSizes {
		for _, sc := range allScreens() {
			t.Run(sz.name+"/"+sc.name, func(t *testing.T) {
				lipgloss.SetColorProfile(termenv.TrueColor)
				m := New("/tmp/install.sh", true)
				u, _ := m.Update(windowSize(sz.width, sz.height))
				m = u.(Model)
				sc.setup(&m)

				content := m.renderContent(m.body())
				if h := lipgloss.Height(content); h > sz.height {
					t.Errorf("content is %d rows in a %d-row terminal — the container "+
						"truncates the excess, so the bottom of the UI (help line, "+
						"stats footer) silently disappears", h, sz.height)
				}
				for i, line := range strings.Split(content, "\n") {
					if w := lipgloss.Width(line); w > sz.width {
						t.Errorf("line %d is %d cells in a %d-column terminal; it wraps "+
							"and shoves everything below it down: %q",
							i, w, sz.width, truncate(stripANSI(line), 50))
					}
				}
			})
		}
	}
}

// The log pane must keep a FIXED height for a given terminal size, or lines
// appearing shift everything below them on every tick.
func TestLogPaneHeightIsStableAndFits(t *testing.T) {
	for _, sz := range termSizes {
		t.Run(sz.name, func(t *testing.T) {
			lipgloss.SetColorProfile(termenv.TrueColor)
			m := New("/tmp/install.sh", true)
			updated, _ := m.Update(windowSize(sz.width, sz.height))
			m = updated.(Model)

			empty := strings.Count(m.tailLog(), "\n")
			for i := 0; i < 50; i++ {
				m.logLines = append(m.logLines, "line of streamed apt output number x")
			}
			full := strings.Count(m.tailLog(), "\n")
			if empty != full {
				t.Errorf("log pane is %d rows when empty and %d when full; it must not "+
					"resize mid-install", empty+1, full+1)
			}
			if rows := m.logRows(); rows < logTailMin || rows > logTailMax {
				t.Errorf("logRows()=%d outside [%d,%d]", rows, logTailMin, logTailMax)
			}
			// It has to leave room for the rest of the screen.
			if got := m.logRows(); sz.height > 0 && got > sz.height {
				t.Errorf("log pane wants %d rows in a %d-row terminal", got, sz.height)
			}
		})
	}
}

// truncate is used on operator-visible strings that come from apt, which is
// UTF-8. Byte-slicing them (the previous implementation) can cut a rune in
// half and emit mojibake.
func TestTruncateIsRuneSafe(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"truncate me here", 8, "truncat…"},
		{"héllo wörld", 6, "héllo…"},
		{"日本語のテキスト", 6, "日本…"}, // wide runes count as 2 cells
		{"x", 1, "x"},
		{"xyz", 1, "…"},
		{"anything", 0, "anything"},
	} {
		if got := truncate(tc.in, tc.width); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
		if got := truncate(tc.in, tc.width); !utf8Valid(got) {
			t.Errorf("truncate(%q, %d) produced invalid UTF-8: %q", tc.in, tc.width, got)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// The stats strip must shed segments rather than wrap on a narrow terminal.
func TestStatsFooterFitsWidth(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	st := sysStats{cpuPct: 38, memPct: 23, netBps: 6100, ioBps: 0, haveNet: true, haveIO: true}
	for _, w := range []int{120, 76, 56, 40, 30} {
		if got := lipgloss.Width(statsFooter(st, w)); got > w {
			t.Errorf("stats footer is %d cells wide with a %d-cell budget — it will wrap", got, w)
		}
	}
}

// windowSize builds the resize message bubbletea delivers on start and on
// every terminal resize.
func windowSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }
