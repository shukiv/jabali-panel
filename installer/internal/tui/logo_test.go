package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)
var bgRe = regexp.MustCompile(`48;2;(\d{1,3});(\d{1,3});(\d{1,3})`)

type logoCells struct {
	glyphs      int // cells painted by a block character
	lightSpace  int // blank cells with an explicit LIGHT background
	darkSpace   int // blank cells with an explicit dark background
	inherited   int // blank cells with NO background in effect
	totalNonSGR int
}

// scanLogo walks the embedded art and classifies every cell by how it will
// actually paint, tracking the background colour the way a terminal does.
func scanLogo(art string) logoCells {
	var c logoCells
	for _, line := range strings.Split(strings.TrimRight(art, "\n"), "\n") {
		var bg []int
		for i := 0; i < len(line); {
			if m := sgrRe.FindStringIndex(line[i:]); m != nil && m[0] == 0 {
				params := sgrRe.FindStringSubmatch(line[i:])[1]
				if params == "0" || params == "" {
					bg = nil // reset drops the background entirely
				}
				if bm := bgRe.FindStringSubmatch(params); bm != nil {
					bg = []int{atoi(bm[1]), atoi(bm[2]), atoi(bm[3])}
				}
				i += m[1]
				continue
			}
			r, size := decodeRune(line[i:])
			i += size
			c.totalNonSGR++
			if r != ' ' {
				c.glyphs++
				continue
			}
			switch {
			case bg == nil:
				c.inherited++
			case luminance(bg) > 128:
				c.lightSpace++
			default:
				c.darkSpace++
			}
		}
	}
	return c
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func luminance(c []int) float64 {
	return 0.2126*float64(c[0]) + 0.7152*float64(c[1]) + 0.0722*float64(c[2])
}

func decodeRune(s string) (rune, int) {
	for i, r := range s {
		if i == 0 {
			return r, len(string(r))
		}
	}
	return 0, 1
}

// TestLogoIsSelfContainedDarkArt pins the property that broke the welcome
// screen: the art must not depend on the background it is drawn onto.
//
// The old logo was produced with `chafa --bg white jabali_logo.png` — the
// light-background asset, rendered assuming a white terminal. On the dark
// theme it displayed as a bright slab with a barely-visible bull. Measured on
// that file: 288 blank cells carried an explicit LIGHT background and 438 had
// no background at all, inheriting whatever was active. The inherited ones are
// the subtler half: chafa emits ESC[0m for transparent regions, and a reset
// cancels the lipgloss background this TUI sets, so those cells fall back to
// the terminal's default rather than the app theme.
//
// Flattening the source onto black before rendering removes the alpha channel,
// so every cell carries an explicit foreground AND background. Regenerate with
// gen-logo.sh; do not hand-edit logo.ansi.
func TestLogoIsSelfContainedDarkArt(t *testing.T) {
	t.Parallel()

	c := scanLogo(jabaliLogo)

	if c.totalNonSGR == 0 {
		t.Fatal("parsed no cells out of logo.ansi — the art or this parser drifted, " +
			"so the test is checking nothing")
	}
	if c.lightSpace > 0 {
		t.Errorf("%d blank cells have an explicit LIGHT background. On the dark theme "+
			"these paint as a bright slab behind the logo. Regenerate with "+
			"installer/internal/tui/gen-logo.sh (it uses jabali_logo_dark.png, not "+
			"jabali_logo.png, and flattens onto black first).", c.lightSpace)
	}
	if c.inherited > 0 {
		t.Errorf("%d blank cells have no background in effect, so they inherit. chafa "+
			"emits ESC[0m for transparent regions and a reset cancels the lipgloss "+
			"background, leaving those cells at the terminal default instead of the "+
			"app theme. Flatten the source onto black before rendering — see "+
			"installer/internal/tui/gen-logo.sh.", c.inherited)
	}
	if c.glyphs < 100 {
		t.Errorf("only %d painted cells — the art looks empty, which is what rendering "+
			"the black-on-transparent jabali_logo.png onto black produces", c.glyphs)
	}
}

// The welcome screen is the one place the full art is drawn; every other
// screen gets the one-line brand so tall screens don't push it off the top.
func TestBannerShowsFullLogoOnlyOnWelcome(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)

	welcome := bannerView(Model{screen: screenWelcome})
	if !strings.Contains(welcome, strings.TrimRight(jabaliLogo, "\n")) {
		t.Error("welcome screen must show the full logo")
	}
	for _, s := range []screen{screenProfile, screenModules, screenConfig, screenInstalling} {
		if got := bannerView(Model{screen: s}); strings.Contains(got, "\x1b[48;2;") {
			t.Errorf("screen %v renders block art in its banner; only the welcome "+
				"screen should, or tall screens scroll the art away", s)
		}
	}
}

// GH #353 follow-up ("can't read bottom instructions"): a 16-colour terminal —
// VPS web/serial consoles report exactly that — must never receive the
// truecolor logo art (renders as garbage blocks) nor the ANSI-256 greys the
// help/off/label styles use on capable terminals (nearest 16-colour match is
// bright black: unreadable on any background). Under the ANSI profile the
// greys degrade to plain white and the banner falls back to the text brand.
func TestSixteenColourConsoleStaysLegible(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.TrueColor) })

	welcome := bannerView(Model{screen: screenWelcome})
	if strings.Contains(welcome, "\x1b[38;2;") || strings.Contains(welcome, "\x1b[48;2;") {
		t.Error("welcome banner leaks truecolor sequences on a 16-colour terminal")
	}

	for name, rendered := range map[string]string{
		"help":  helpStyle.Render("enter: continue   q: quit"),
		"off":   offStyle.Render("Mail suite"),
		"label": fieldLabelStyle.Render("URL"),
		"tag":   tagStyle.Render("JABALI PANEL"),
		"box":   boxStyle.Render("log line"),
	} {
		if strings.Contains(rendered, "[38;5;") {
			t.Errorf("%s style emits an ANSI-256 grey on a 16-colour terminal: %q", name, rendered)
		}
		if strings.Contains(rendered, "[90m") || strings.Contains(rendered, "[30m") {
			t.Errorf("%s style degrades to (bright) black — invisible: %q", name, rendered)
		}
		if !strings.Contains(rendered, "[37m") {
			t.Errorf("%s style must degrade to white (37) on a 16-colour terminal: %q", name, rendered)
		}
	}
}
