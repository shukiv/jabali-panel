// Package tui is the Bubble Tea installer front-end (M353 / GH #353). It walks
// the operator through a deploy profile, an optional-module checklist with live
// dependency resolution, a confirm screen, then runs install.sh with
// JABALI_MODULES=<keys> and streams its output into a scrolling progress pane.
package tui

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"git.jabali-panel.com/shukivaknin/jabali2/installer/internal/modules"
)

type screen int

const (
	screenWelcome screen = iota
	screenProfile
	screenModules
	screenConfig
	screenConfirm
	screenInstalling
	screenResult
)

// Layout bounds. The TUI has to be legible in an 80x24 default terminal, not
// only in a maximised one, so every width below is derived from the actual
// terminal size and clamped rather than hardcoded.
const (
	logTailMax  = 16 // streamed log lines when there is room
	logTailMin  = 4
	progressMax = 60 // progress bar never grows past this on a wide screen
	progressMin = 12
	// appStyle's Padding(1, 2) costs two columns on each side.
	appHPad = 4
	// contentMin keeps arithmetic sane on absurdly narrow terminals; below
	// this the output is going to be cramped whatever we do.
	contentMin = 24
)

// contentWidth is the usable width inside the app padding, or 0 before the
// first WindowSizeMsg (callers then fall back to their natural size).
func (m Model) contentWidth() int {
	if m.width <= 0 {
		return 0
	}
	if w := m.width - appHPad; w > contentMin {
		return w
	}
	return contentMin
}

// logRows is how many streamed lines the log pane shows: as many as the
// terminal has room for, between logTailMin and logTailMax. The pane is a
// FIXED height once chosen so it can't shrink mid-install and leave ghost
// lines behind.
func (m Model) logRows() int {
	if m.height <= 0 {
		return logTailMax
	}
	// Everything else on the installing screen — banner, title, progress bar,
	// phase line, the log box's own border, help line, stats footer and the
	// blank lines between them — costs exactly 15 rows. Measured, not
	// estimated: the first guess here was 14, which overflowed every terminal
	// by one row and truncated the stats footer. TestScreensFitTheTerminal
	// pins it, with the log pane forced visible (it defaults to hidden, so a
	// test that does not set logHidden never exercises this at all).
	switch n := m.height - 15; {
	case n > logTailMax:
		return logTailMax
	case n < logTailMin:
		return logTailMin
	default:
		return n
	}
}

// tightRows reports whether the terminal is too short for a layout that wants
// `want` rows. Screens use it to drop per-item description lines and show the
// description for the highlighted entry only — the modules list wanted 27 rows
// and the config screen 29, neither of which fits a default 80x24 window. The
// container silently truncates the excess, so without this the help line at
// the bottom just vanishes.
func (m Model) tightRows(want int) bool {
	return m.height > 0 && want > m.height
}

// fitLine trims a line to the content width, allowing for `indent` columns of
// leading space.
func (m Model) fitLine(s string, indent int) string {
	if w := m.contentWidth(); w > 0 {
		return truncate(s, w-indent)
	}
	return s
}

// wrap reflows prose to the content width instead of relying on hardcoded
// newlines, which overflowed narrow terminals and left ragged gaps on wide ones.
func (m Model) wrap(s string) string {
	if w := m.contentWidth(); w > 0 {
		return lipgloss.NewStyle().Width(w).Render(s)
	}
	return s
}

// logLineWidth is how wide a streamed log line may be before it is trimmed.
// boxStyle costs two border columns and two padding columns. This was a
// hardcoded 76, which overflowed every terminal narrower than 80 and wrapped
// each log line into two.
func (m Model) logLineWidth() int {
	if w := m.contentWidth(); w > 0 {
		return fitWidth(w-4, 20, 120)
	}
	return 76
}

// Dark theme (GH #353 tester feedback: the white fill was too bright and the
// streamed-log pane didn't stand out). Solid black also blends with the
// luminance-inverted logo art, whose untouched cells show the app background.
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	onStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	offStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	errStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Foreground(lipgloss.Color("252"))
	bgDark      = lipgloss.Color("#000000")
	fgLight     = lipgloss.Color("#e6e6e6")
	appStyle    = lipgloss.NewStyle().Padding(1, 2).Background(bgDark).Foreground(fgLight)
	logoStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	tagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
)

// themeSGR is the app's foreground+background as a raw escape sequence, or ""
// when the terminal has no colour. Derived by rendering with the real style so
// it follows whatever colour profile lipgloss negotiated.
func themeSGR() string {
	const marker = "\x00"
	out := lipgloss.NewStyle().Background(bgDark).Foreground(fgLight).Render(marker)
	if i := strings.Index(out, marker); i > 0 {
		return out[:i]
	}
	return ""
}

// reassertTheme re-establishes the app colours immediately after every SGR
// reset in the rendered frame.
//
// This exists because of how lipgloss composes: the theme is set by ONE outer
// style carrying Background(bgDark), but every nested style it renders inside
// that container terminates with ESC[0m — which clears the background along
// with the colour it was actually trying to end. Everything after the first
// nested style on a line then paints on the TERMINAL's default background. On
// a light terminal that is a white slab straight through the UI: the title and
// the phase text on the progress screen, the focused field's label and the
// whole PHP-version row on the config screen. The tell was that UNFOCUSED rows
// looked right — they use a plain "  " cursor with no nested style, so nothing
// reset the background.
//
// Fixing it at each render site would mean giving ~15 styles an explicit
// background and remembering to do the same for every style added later, which
// is exactly the mistake that produced this. Doing it once on the finished
// frame is provably complete: after any reset, the theme is active again.
// TestScreensPaintTheirOwnBackground enforces the property.
func reassertTheme(frame string) string {
	base := themeSGR()
	if base == "" {
		return frame // monochrome terminal: nothing to reassert
	}
	// Trailing reset so the theme doesn't leak into whatever the terminal
	// draws after the frame.
	return strings.ReplaceAll(frame, ansiReset, ansiReset+base) + ansiReset
}

const ansiReset = "\x1b[0m"

// renderContent is the frame BEFORE it is placed in the full-terminal
// container. Split out so tests can measure what the container would clamp:
// Width()/Height() on the container silently wrap and truncate, so a layout
// that overflows looks perfectly sized once rendered — the bottom of the UI
// just quietly goes missing. Measuring here is the only way to see it.
func (m Model) renderContent(body string) string {
	return appStyle.Render(bannerView(m) + body)
}

// jabaliLogo is the brand logo shown on the welcome screen: a chafa render of
// jabali_logo_dark.png as truecolor half-block art, embedded at build time so
// there's no ANSI-escaping in source.
//
// Regenerate with installer/internal/tui/gen-logo.sh — never by hand and never
// with an ad-hoc chafa invocation. The art must be SELF-CONTAINED: every cell
// carries an explicit foreground and background, so it cannot inherit a colour
// and cannot be affected by a reset. chafa emits ESC[0m for transparent
// regions, and a reset cancels the lipgloss background this TUI sets, which is
// how the previous art ended up as a bright slab on a dark terminal.
// TestLogoIsSelfContainedDarkArt enforces the property.
//
//go:embed logo.ansi
var jabaliLogo string

// bannerView renders the logo + tagline, prepended to every screen.
func bannerView(m Model) string {
	tag := tagStyle.Render("JABALI PANEL · Linux Web Hosting Control Panel")
	// Full logo only on the welcome screen; elsewhere a compact one-line brand
	// so tall screens (modules, config, installing) don't push the art off the
	// top of the terminal — the "hog is up" cutoff.
	// The art is 17 rows; on a short terminal it pushes the prompt and help
	// line past the bottom edge, where the container silently truncates them.
	if m.screen == screenWelcome && !m.tightRows(lipgloss.Height(jabaliLogo)+11) {
		return strings.TrimRight(jabaliLogo, "\n") + "\n" + tag + "\n\n"
	}
	return tag + "\n\n"
}

// Model is the installer state machine.
type Model struct {
	screen   screen
	cursor   int
	profiles []modules.Profile
	mods     []modules.Module
	selected map[string]bool

	installSh  string
	dryRun     bool
	events     chan tea.Msg
	spinner    spinner.Model
	progress   progress.Model
	stepsDone  float64 // completed phase weight (apt phase counts as aptWeight)
	stepsTot   float64 // total phase weight (estimate + aptWeight)
	creep      float64 // intra-phase progress (0..1); apt uses parsed %, others a timer
	inApt      bool    // current phase is the long "apt install system packages" step
	started    bool    // at least one phase marker seen (so we know a prev phase to close)
	prevSample sysSample
	stats      sysStats
	logHidden  bool // hide the streaming log box (toggle with \'l\')

	logLines   []string
	phase      string
	installed  bool
	installErr error
	summary    []string // captured "JABALI PANEL — installed" block (URL/user/pass)
	capSummary bool

	// Config screen (T3): host/admin/NS/PHP inputs + focus + validation error.
	fields    []configField
	focus     int
	configErr string

	Confirmed bool
	Aborted   bool
	ExitCode  int
	width     int // terminal size (WindowSizeMsg) for full-screen centering
	height    int
}

// New builds the model. installSh is the path to install.sh; dryRun passes
// --dry-run so a run previews the plan without changing the system.
func New(installSh string, dryRun bool) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	pr := progress.New(progress.WithDefaultGradient(), progress.WithWidth(46))
	return Model{
		progress:  pr,
		screen:    screenWelcome,
		profiles:  modules.Profiles,
		mods:      modules.Registry,
		selected:  map[string]bool{},
		installSh: installSh,
		dryRun:    dryRun,
		events:    make(chan tea.Msg, 256),
		spinner:   sp,
		logHidden: true, // log hidden by default; press 'l' to reveal
		fields:    newConfigFields(defaultHostname()),
	}
}

func (m Model) Init() tea.Cmd { return nil }

// defaultHostname pre-fills the hostname field: JABALI_HOSTNAME if set, else the
// machine's FQDN (hostname -f), else the short hostname. The operator can still
// edit it; admin/NS fields derive from whatever ends up here.
func defaultHostname() string {
	if v := strings.TrimSpace(os.Getenv("JABALI_HOSTNAME")); v != "" {
		return v
	}
	if out, err := exec.Command("hostname", "-f").Output(); err == nil {
		if h := strings.TrimSpace(string(out)); h != "" && h != "localhost" {
			return h
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		return h
	}
	return ""
}

func (m Model) SelectedKeys() []string {
	var out []string
	for _, mod := range m.mods {
		if m.selected[mod.Key] {
			out = append(out, mod.Key)
		}
	}
	return out
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		if m.screen != screenInstalling || m.installed {
			return m, nil
		}
		// Creep across the current phase toward (but never reaching) the next
		// boundary, so the bar advances during long/silent steps like apt.
		if m.creep < 0.9 {
			m.creep += 0.05
		}
		cur := sampleSys(time.Time(msg))
		m.stats = computeStats(m.prevSample, cur)
		m.prevSample = cur
		return m, tea.Batch(tickCmd(), m.progress.SetPercent(m.overallPct()))
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		return m, cmd
	case logLineMsg:
		line := string(msg)
		// apt machine-progress lines drive the bar's intra-phase creep with the
		// real download/install percentage; they're hidden from the log box.
		if c, ok := aptProgress(line); ok {
			if c > m.creep {
				m.creep = c
			}
			return m, tea.Batch(waitForEvent(m.events), m.progress.SetPercent(m.overallPct()))
		}
		m.logLines = append(m.logLines, line)
		if len(m.logLines) > 400 {
			m.logLines = m.logLines[len(m.logLines)-400:]
		}
		m.captureSummary(stripANSI(line))
		if p, ok := phaseFromLine(line); ok {
			// Close the previous phase, adding its weight (the long apt phase is
			// worth aptWeight; everything else is worth 1).
			if m.started {
				w := 1.0
				if m.inApt {
					w = aptWeight
				}
				m.stepsDone += w
			}
			m.started = true
			m.phase = p
			m.inApt = strings.Contains(p, "apt install system packages")
			m.creep = 0
		}
		return m, tea.Batch(waitForEvent(m.events), m.progress.SetPercent(m.overallPct()))
	case installDoneMsg:
		m.installed = true
		m.installErr = msg.err
		_ = m.progress.SetPercent(1.0)
		if msg.err != nil {
			m.ExitCode = 1
		}
		m.screen = screenResult
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// The progress bar was a fixed 46 columns, which overflowed and wrapped
		// on a default 80x24 terminal once the percentage and padding were added.
		m.progress.Width = fitWidth(m.contentWidth()-8, progressMin, progressMax)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		if m.screen == screenConfig && len(m.fields) > 0 {
			cmd := updateFocusedField(m.fields, m.focus, msg)
			return m, cmd
		}
	}
	return m, nil
}

// focusField focuses field index i (blurring the rest) and returns the blink cmd.
func (m *Model) focusField(i int) tea.Cmd {
	for j := range m.fields {
		if j == i {
			m.fields[j].input.Focus()
		} else {
			m.fields[j].input.Blur()
		}
	}
	return textinput.Blink
}

// handleConfigKey drives the config screen: tab/↑↓ move between the visible
// fields, typing edits the focused input, enter validates and advances.
func (m Model) handleConfigKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := visibleFields(m.fields, m.selected["dns"])
	// position of m.focus within the visible slice
	pos := 0
	for i, idx := range vis {
		if idx == m.focus {
			pos = i
		}
	}
	// PHP multi-select field: left/right move the version cursor, space toggles.
	if m.fields[m.focus].phpSelect {
		switch key.String() {
		case "left", "h":
			if m.fields[m.focus].phpCursor > 0 {
				m.fields[m.focus].phpCursor--
			}
			return m, nil
		case "right", "l":
			if m.fields[m.focus].phpCursor < len(m.fields[m.focus].phpVers)-1 {
				m.fields[m.focus].phpCursor++
			}
			return m, nil
		case " ", "space", "x":
			f := &m.fields[m.focus]
			v := f.phpVers[f.phpCursor]
			f.phpChecked[v] = !f.phpChecked[v]
			return m, nil
		}
	}
	switch key.String() {
	case "tab", "down":
		pos = (pos + 1) % len(vis)
		m.focus = vis[pos]
		return m, m.focusField(m.focus)
	case "shift+tab", "up":
		pos = (pos - 1 + len(vis)) % len(vis)
		m.focus = vis[pos]
		return m, m.focusField(m.focus)
	case "enter":
		if e := validateConfig(m.fields, m.selected["dns"]); e != "" {
			m.configErr = e
			return m, nil
		}
		m.configErr = ""
		m.screen = screenConfirm
		return m, nil
	case "esc":
		m.screen = screenModules
		return m, nil
	default:
		cmd := updateFocusedField(m.fields, m.focus, key)
		if m.fields[m.focus].env == "JABALI_HOSTNAME" {
			// Hostname edited → refresh the untouched derived fields.
			applyDerived(m.fields, m.fields[m.focus].input.Value())
		} else if m.fields[m.focus].derive != nil {
			// Operator typed into a derivable field → stop auto-deriving it.
			m.fields[m.focus].touched = true
		}
		return m, cmd
	}
}

func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		m.Aborted = true
		m.ExitCode = 130
		return m, tea.Quit
	case "q":
		// On the config screen 'q' is a valid input character (hostnames,
		// emails); only treat it as quit on the selection screens.
		if m.screen == screenConfig {
			break
		}
		if m.screen == screenResult {
			return m, tea.Quit
		}
		if m.screen != screenInstalling {
			m.Aborted = true
			m.ExitCode = 130
			return m, tea.Quit
		}
	}
	switch m.screen {
	case screenWelcome:
		if key.String() == "enter" {
			m.screen = screenProfile
			m.cursor = 0
		}
	case screenProfile:
		switch key.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.profiles)-1 {
				m.cursor++
			}
		case "enter":
			p := m.profiles[m.cursor]
			m.selected = map[string]bool{}
			for _, k := range p.Modules {
				m.selected[k] = true
			}
			m.selected = modules.Resolve(m.selected, "")
			m.screen = screenModules
			m.cursor = 0
		}
	case screenModules:
		switch key.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.mods)-1 {
				m.cursor++
			}
		case " ", "space", "x":
			mod := m.mods[m.cursor]
			if m.selected[mod.Key] {
				delete(m.selected, mod.Key)
				m.selected = modules.Resolve(m.selected, "")
			} else {
				m.selected[mod.Key] = true
				m.selected = modules.Resolve(m.selected, mod.Key)
			}
		case "enter":
			m.screen = screenConfig
			m.focus = 0
			m.configErr = ""
			return m, m.focusField(0)
		}
	case screenConfig:
		return m.handleConfigKey(key)
	case screenConfirm:
		switch key.String() {
		case "enter", "y":
			m.Confirmed = true
			m.screen = screenInstalling
			m.stepsTot = float64(estimateSteps(m.SelectedKeys())) + aptWeight
			env := append(configEnv(m.fields, m.selected["dns"]),
				"JABALI_MODULES="+strings.Join(m.SelectedKeys(), ","),
				"JABALI_NONINTERACTIVE=1") // TUI owns the terminal; install.sh must not open /dev/tty

			return m, tea.Batch(m.spinner.Tick, tickCmd(), startInstall(m.installSh, env, m.dryRun, m.events))
		case "b", "backspace", "left":
			m.screen = screenModules
		}
	case screenInstalling:
		if key.String() == "l" {
			m.logHidden = !m.logHidden
		}
	case screenResult:
		if key.String() == "enter" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// body renders the screen-specific portion of the frame, without the banner,
// the app padding or the full-terminal container. Split out from View so tests
// can measure the layout before the container clamps it.
func (m Model) body() string {
	var b strings.Builder
	switch m.screen {
	case screenWelcome:
		b.WriteString(titleStyle.Render("Jabali Panel installer"))
		b.WriteString("\n\n" + m.wrap("Modular install: pick a deploy profile, then fine-tune the "+
			"optional modules. Core services (web, database, panel) are always installed.") + "\n\n")
		b.WriteString(helpStyle.Render("enter: continue   q: quit"))
	case screenProfile:
		b.WriteString(titleStyle.Render("Deploy profile"))
		b.WriteString("\n\n")
		tight := m.tightRows(len(m.profiles)*2 + 9)
		for i, p := range m.profiles {
			cur := "  "
			if i == m.cursor {
				cur = cursorStyle.Render("> ")
			}
			if tight {
				// One row per entry; the description for the highlighted entry
				// only, below the list. Two rows each does not fit 24 rows.
				b.WriteString(fmt.Sprintf("%s%s\n", cur, p.Title))
				continue
			}
			b.WriteString(fmt.Sprintf("%s%s\n    %s\n", cur, p.Title,
				helpStyle.Render(m.fitLine(p.Desc, 4))))
		}
		if tight && m.cursor < len(m.profiles) {
			b.WriteString("\n" + helpStyle.Render(m.fitLine(m.profiles[m.cursor].Desc, 2)) + "\n")
		}
		b.WriteString("\n" + helpStyle.Render("↑/↓: move   enter: select   q: quit"))
	case screenModules:
		b.WriteString(titleStyle.Render("Optional modules"))
		b.WriteString("\n\n")
		tightMods := m.tightRows(len(m.mods)*2 + 9)
		for i, mod := range m.mods {
			cur := "  "
			if i == m.cursor {
				cur = cursorStyle.Render("> ")
			}
			box := offStyle.Render("[ ]")
			if m.selected[mod.Key] {
				box = onStyle.Render("[x]")
			}
			if tightMods {
				b.WriteString(fmt.Sprintf("%s%s %s\n", cur, box, mod.Title))
				continue
			}
			b.WriteString(fmt.Sprintf("%s%s %s\n      %s\n", cur, box, mod.Title,
				helpStyle.Render(m.fitLine(mod.Desc, 6))))
		}
		if tightMods && m.cursor < len(m.mods) {
			b.WriteString("\n" + helpStyle.Render(m.fitLine(m.mods[m.cursor].Desc, 2)) + "\n")
		}
		b.WriteString("\n" + helpStyle.Render(m.wrap("↑/↓: move   space: toggle   enter: continue   q: quit")))
		if !tightMods {
			b.WriteString("\n" + helpStyle.Render("(dependencies auto-select; conflicts auto-clear)"))
		}
	case screenConfig:
		b.WriteString(titleStyle.Render("Configuration"))
		b.WriteString("\n\n")
		tightCfg := m.tightRows(len(visibleFields(m.fields, m.selected["dns"]))*2 + 12)
		for _, i := range visibleFields(m.fields, m.selected["dns"]) {
			f := m.fields[i]
			cur := "  "
			if i == m.focus {
				cur = cursorStyle.Render("> ")
			}
			req := ""
			if f.required {
				req = helpStyle.Render(" *")
			}
			if f.phpSelect {
				b.WriteString(fmt.Sprintf("%s%s\n%s\n", cur, f.label,
					phpChips(f, i == m.focus, m.contentWidth())))
			} else if tightCfg {
				// Label and value share a row; two rows per field does not fit
				// a short terminal once the PHP chips and help text are added.
				b.WriteString(fmt.Sprintf("%s%s%s %s\n", cur, f.label, req, f.input.View()))
			} else {
				b.WriteString(fmt.Sprintf("%s%s%s\n    %s\n", cur, f.label, req, f.input.View()))
			}
		}
		if !m.selected["dns"] {
			b.WriteString("\n" + helpStyle.Render(m.wrap("(DNS module off — nameservers skipped; "+
				"configure them in Server Settings if you enable DNS later)")) + "\n")
		}
		if m.configErr != "" {
			b.WriteString("\n" + errStyle.Render(m.configErr) + "\n")
		}
		b.WriteString("\n" + helpStyle.Render(m.wrap("tab/↑↓: move fields   ←→+space: toggle PHP   "+
			"enter: continue   esc: back")))
	case screenConfirm:
		b.WriteString(titleStyle.Render("Confirm"))
		b.WriteString("\n\n" + m.wrap("Core: web (nginx + PHP-FPM), database (MariaDB), panel + auth") +
			"\n\nOptional modules to install:\n")
		keys := m.SelectedKeys()
		if len(keys) == 0 {
			b.WriteString(helpStyle.Render("  (none — minimal install)") + "\n")
		}
		for _, k := range keys {
			b.WriteString(onStyle.Render("  + "+k) + "\n")
		}
		b.WriteString("\n" + helpStyle.Render("JABALI_MODULES=") + onStyle.Render(strings.Join(keys, ",")) + "\n")
		// The configuration echo repeats the screen the operator just left, so
		// it is the first thing to drop when there are not enough rows. Decide
		// by MEASURING the assembled frame rather than estimating its height —
		// the estimate has to account for prose that rewraps with the width,
		// which is how it kept being wrong by a row.
		var echo strings.Builder
		echo.WriteString("\nConfiguration:\n")
		for _, i := range visibleFields(m.fields, m.selected["dns"]) {
			f := m.fields[i]
			v := strings.TrimSpace(f.input.Value())
			if v == "" {
				continue
			}
			echo.WriteString(helpStyle.Render("  "+f.label+": ") + m.fitLine(v, 4) + "\n")
		}
		var tail strings.Builder
		if m.dryRun {
			tail.WriteString("\n" + helpStyle.Render(m.wrap("(dry run — will preview the plan, not install)")) + "\n")
		}
		tail.WriteString("\n" + helpStyle.Render("enter/y: install   b: back   q: quit"))
		if m.height <= 0 || lipgloss.Height(m.renderContent(b.String()+echo.String()+tail.String())) <= m.height {
			b.WriteString(echo.String())
		}
		b.WriteString(tail.String())
	case screenInstalling:
		b.WriteString(fmt.Sprintf("%s %s\n", m.spinner.View(), titleStyle.Render("Installing…")))
		b.WriteString("\n" + m.progress.View() + "\n")
		if m.phase != "" {
			// Truncate rather than wrap. A long apt phase used to fold onto a
			// second line and shove everything below it down, so the layout
			// twitched every time the phase changed.
			b.WriteString("\n" + helpStyle.Render("current: ") +
				truncate(m.phase, m.contentWidth()-len("current: ")) + "\n")
		}
		if m.logHidden {
			b.WriteString("\n" + helpStyle.Render("l: show log   ctrl+c: abort") + "\n")
		} else {
			b.WriteString("\n" + boxStyle.Render(m.tailLog()) + "\n")
			b.WriteString(helpStyle.Render("l: hide log   installing… please wait (ctrl+c aborts)"))
		}
		b.WriteString("\n\n" + statsFooter(m.stats, m.contentWidth()))
	case screenResult:
		if m.installErr != nil {
			b.WriteString(errStyle.Render("Install failed"))
			b.WriteString("\n\n" + m.installErr.Error() + "\n")
			b.WriteString("\n" + boxStyle.Render(m.tailLog()) + "\n")
		} else {
			b.WriteString(onStyle.Render("✓ Install complete"))
			if len(m.summary) > 0 {
				b.WriteString("\n\n" + renderSummaryCard(m.summary, m.width) + "\n")
			} else {
				b.WriteString("\n\n" + helpStyle.Render("Log in at the panel URL printed above.") + "\n")
			}
		}
		b.WriteString("\n" + helpStyle.Render("enter/q: exit"))
	}
	return b.String()
}

// View composes the full frame: banner + body, placed on a terminal-sized
// themed canvas, with the theme re-established after every reset.
func (m Model) View() string {
	content := m.renderContent(m.body())
	if m.width <= 0 || m.height <= 0 {
		return reassertTheme(content) // no size yet (pre first WindowSizeMsg)
	}
	// Fill the ENTIRE terminal with the theme background and place the content
	// block on it — a solid Width×Height container is reliable where
	// Place+WhitespaceBackground left the terminal's own background showing
	// through.
	//
	// Centred horizontally: left-aligned, the block sat in the left third of a
	// wide terminal with a large dead area beside it. Top-aligned vertically:
	// centring both axes left the content floating in the middle of a default
	// 80x24 window with dead bands above and below, and made the whole UI jump
	// whenever its height changed (the log pane appearing, a phase line
	// wrapping). An installer that streams progress should grow downward from
	// a fixed top edge.
	return reassertTheme(lipgloss.NewStyle().
		Width(m.width).Height(m.height).
		Background(bgDark).Foreground(fgLight).
		Align(lipgloss.Center, lipgloss.Top).
		Render(content))
}

// tailLog returns the last logTail streamed lines, ANSI-stripped, for the box.
func (m Model) tailLog() string {
	rows := m.logRows()
	lines := m.logLines
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	out := make([]string, rows) // fixed height → box never shrinks (no ghost lines)
	base := rows - len(lines)
	for i := range out {
		if i < base {
			out[i] = ""
			continue
		}
		out[i] = truncate(stripANSI(lines[i-base]), m.logLineWidth())
	}
	if len(lines) == 0 {
		out[0] = helpStyle.Render("(waiting for output…)")
	}
	return strings.Join(out, "\n")
}

// estimateSteps guesses the number of [i] phase markers install.sh will emit,
// so the progress bar advances smoothly. Rough on purpose — capped at 0.97 in
// Update and snapped to 1.0 on completion, so an over/under estimate only
// changes the fill speed, never correctness. Heavier modules weigh more.
func estimateSteps(keys []string) int {
	total := 55 // core: base packages, php, nginx, mariadb, panel, agent, certs…
	weight := map[string]int{"dns": 10, "mail": 28, "security": 30, "docker": 8,
		"docker_apps": 6, "python_apps": 8, "postgres": 8, "quota": 5, "api": 2}
	for _, k := range keys {
		if w, ok := weight[k]; ok {
			total += w
		}
	}
	return total
}

// truncate shortens s to at most width display CELLS, marking the cut with an
// ellipsis. s must be plain text (no ANSI) — both callers strip it first.
//
// Counts runes and display width, not bytes. The previous byte-slicing version
// could cut a multibyte rune in half and emit mojibake, which matters here
// because apt's output is UTF-8 and the strings this trims come straight from
// it.
func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var out []rune
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

// phpChips renders the PHP version multi-select as [x]/[ ] chips, highlighting
// the cursor position when the field is focused.
// phpChips lays the version chips out in as many rows as the width needs. As a
// single row they ran 73 cells, which overflowed anything narrower than ~78.
func phpChips(f configField, focused bool, width int) string {
	parts := make([]string, len(f.phpVers))
	for i, v := range f.phpVers {
		box := "[ ]"
		st := offStyle
		if f.phpChecked[v] {
			box = "[x]"
			st = onStyle
		}
		chip := st.Render(box + " " + v)
		if focused && i == f.phpCursor {
			chip = cursorStyle.Render("‹") + chip + cursorStyle.Render("›")
		} else {
			chip = " " + chip + " "
		}
		parts[i] = chip
	}
	const indent = "    "
	if width <= 0 {
		return indent + strings.Join(parts, " ")
	}
	var rows []string
	var row string
	for _, chip := range parts {
		candidate := row
		if candidate != "" {
			candidate += " "
		}
		candidate += chip
		if row != "" && lipgloss.Width(indent+candidate) > width {
			rows = append(rows, indent+row)
			row = chip
			continue
		}
		row = candidate
	}
	if row != "" {
		rows = append(rows, indent+row)
	}
	return strings.Join(rows, "\n")
}

// aptWeight is how many phase-units the long "apt install system packages" step
// is worth. It takes minutes (vs seconds for other phases) and is driven by
// apt's real dl/pm percentage, so it must own a large slice of the bar or it
// looks frozen. ~45 puts it around a third of a full install.
const aptWeight = 45.0

// tickMsg drives the intra-phase progress creep + the system-stats footer on a
// timer; it carries the tick time so stats rates have a delta.
type tickMsg time.Time

// tickCmd fires a tickMsg roughly twice a second while installing.
func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// overallPct combines completed phases with the current-phase creep, capped
// below 100% until the install actually finishes.
func (m Model) overallPct() float64 {
	if m.stepsTot <= 0 {
		return 0
	}
	w := 1.0
	if m.inApt {
		w = aptWeight
	}
	c := m.creep
	if c > 1.0 {
		c = 1.0
	}
	pct := (m.stepsDone + c*w) / m.stepsTot
	if pct > 0.97 {
		pct = 0.97
	}
	return pct
}

// captureSummary collects install.sh's final "JABALI PANEL — installed" block
// (URL / Username / Password + login advisory) from the streamed output so the
// result screen can show it even though the live log box is hidden. The block
// is bordered by ===== lines around a "JABALI PANEL … installed" banner.
func (m *Model) captureSummary(s string) {
	sc := strings.TrimSpace(s)
	if strings.Contains(sc, "JABALI PANEL") && strings.Contains(sc, "installed") {
		m.capSummary = true
		m.summary = nil
		return
	}
	if !m.capSummary {
		return
	}
	isBorder := sc != "" && strings.Trim(sc, "=") == ""
	if isBorder {
		if len(m.summary) > 0 { // closing border → block done
			m.capSummary = false
		}
		return
	}
	if sc != "" {
		m.summary = append(m.summary, sc)
	}
}

var (
	fieldLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Width(11)
	urlValueStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	credValueStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	cardStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("42")).Padding(0, 2)
	cardTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
)

// renderSummaryCard turns the captured install summary lines into a styled
// "Panel access" card: URL/Username/Password as aligned coloured fields, and
// the "> ..." advisory lines as a dim note block below.
func renderSummaryCard(lines []string, width int) string {
	var fields []string
	var notes []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "URL:"):
			fields = append(fields, fieldLabelStyle.Render("URL")+urlValueStyle.Render(strings.TrimSpace(strings.TrimPrefix(t, "URL:"))))
		case strings.HasPrefix(t, "Username:"):
			fields = append(fields, fieldLabelStyle.Render("Username")+credValueStyle.Render(strings.TrimSpace(strings.TrimPrefix(t, "Username:"))))
		case strings.HasPrefix(t, "Password:"):
			fields = append(fields, fieldLabelStyle.Render("Password")+credValueStyle.Render(strings.TrimSpace(strings.TrimPrefix(t, "Password:"))))
		case strings.HasPrefix(t, ">"):
			notes = append(notes, "• "+strings.TrimSpace(strings.TrimPrefix(t, ">")))
		default:
			// continuation of a previous note (wrapped advisory line)
			if len(notes) > 0 && t != "" {
				notes[len(notes)-1] += " " + t
			}
		}
	}
	var body strings.Builder
	body.WriteString(cardTitleStyle.Render("Panel access") + "\n\n")
	body.WriteString(strings.Join(fields, "\n"))
	if len(notes) > 0 {
		// Word-wrap the advisory notes to the viewport width at WORD boundaries.
		// Relying on the terminal to soft-wrap long lines (the old behaviour) broke
		// them mid-word ("co\nuld not reach") and mid-URL; lipgloss .Width wraps on
		// spaces instead. Subtract appStyle's Padding(1,2) so wrapped lines don't
		// run under the right padding.
		noteW := width - 4
		if noteW < 32 {
			noteW = 76
		}
		body.WriteString("\n\n" + helpStyle.Width(noteW).Render(strings.Join(notes, "\n")))
	}
	// No border: the bordered box scattered its pipes when content overflowed. Plain
	// left-aligned text with explicit note wrapping (above) reads cleanly.
	return body.String()
}

var (
	barOKStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	barWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	barHotStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	statLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
)

// statBar renders a compact [████░░░░] bar coloured by load, plus the percent.
func statBar(label string, pct float64, width int) string {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	st := barOKStyle
	if pct >= 90 {
		st = barHotStyle
	} else if pct >= 70 {
		st = barWarnStyle
	}
	bar := st.Render(strings.Repeat("█", filled)) + offStyle.Render(strings.Repeat("░", width-filled))
	return statLabel.Render(label+" ") + bar + statLabel.Render(fmt.Sprintf(" %3.0f%%", pct))
}

// humanRate formats bytes/sec as B/K/M/G per second.
func humanRate(bps float64) string {
	units := []string{"B", "K", "M", "G"}
	i := 0
	for bps >= 1024 && i < len(units)-1 {
		bps /= 1024
		i++
	}
	// Fixed-width field (%6.1f → max "1023.9") so the footer's total width never
	// changes tick-to-tick. A variable width made the centered block re-center
	// every frame, jittering the whole UI left↔right.
	return fmt.Sprintf("%6.1f%s/s", bps, units[i])
}

// statsFooter is the live system-metrics line shown while installing.
// statsFooter renders the CPU/MEM/NET/DISK strip, dropping segments and
// shrinking the bars until it fits. It used to be a fixed layout that wrapped
// onto a second line on a default-width terminal — "DISK 0.0B/s" folding under
// the rest, which then pushed the frame taller than the window.
//
// width <= 0 means "no size yet"; render the full strip at its natural size.
func statsFooter(s sysStats, width int) string {
	build := func(barW int, net, io bool) string {
		var parts []string
		if barW > 0 {
			parts = append(parts,
				statBar("CPU", s.cpuPct, barW),
				statBar("MEM", s.memPct, barW))
		} else {
			// No room for bars at all: percentages only.
			parts = append(parts,
				statLabel.Render("CPU ")+fmt.Sprintf("%.0f%%", s.cpuPct),
				statLabel.Render("MEM ")+fmt.Sprintf("%.0f%%", s.memPct))
		}
		if net {
			parts = append(parts, statLabel.Render("NET ")+humanRate(s.netBps))
		}
		if io {
			parts = append(parts, statLabel.Render("DISK ")+humanRate(s.ioBps))
		}
		return strings.Join(parts, statLabel.Render("   "))
	}
	// Widest first; shed the least important thing at each step — optional
	// segments before bar width, bars entirely only as a last resort.
	type variant struct {
		barW    int
		net, io bool
	}
	for _, v := range []variant{
		{10, s.haveNet, s.haveIO},
		{10, s.haveNet, false},
		{10, false, false},
		{6, false, false},
		{0, false, false},
	} {
		out := build(v.barW, v.net, v.io)
		if width <= 0 || lipgloss.Width(out) <= width {
			return out
		}
	}
	return build(0, false, false)
}

// fitWidth clamps n into [lo, hi].
func fitWidth(n, lo, hi int) int {
	switch {
	case n < lo:
		return lo
	case n > hi:
		return hi
	default:
		return n
	}
}

// SummaryLines returns the captured "JABALI PANEL — installed" block (URL /
// Username / Password + advisory) so main can re-print it after the alt-screen
// is torn down on exit.
func (m Model) SummaryLines() []string { return m.summary }
