package commands

// CrowdSec 1.8 AppSec bot detection (per-server, default OFF).
//
// The upstream crowdsecurity/appsec-bot-challenge collection scores each
// request's fingerprint and, above a threshold, returns a `challenge`
// remediation — a self-contained JS / proof-of-work interstitial the
// crowdsec-nginx-bouncer (>= 1.2.2) serves. This handler composes those
// configs into the AppSec acquisition alongside jabali-appsec, plus a
// jabali-bot-exempt config that shields the things that CANNOT solve a
// challenge (ACME HTTP-01, panel login/API, DB tools, WebDAV, webmail).
//
// Two hard-won operational facts (validated on the test box):
//   - Composing bot-challenge config on a CrowdSec engine < 1.8 FATALs the
//     process on start → AppSec AND all scenario enforcement go down
//     (fail-open). So the ON path version-gates (engine >= 1.8, bouncer
//     >= 1.2.x) and validates with `crowdsec -t` BEFORE touching the
//     running service, rolling the files back if validation fails.
//   - A change to the acquisition (adding/removing datasource configs) is
//     NOT picked up by `systemctl reload` (SIGHUP re-reads rules, not
//     acquisitions) — it needs a `restart`. Geoblock (which only changes
//     appsec-config CONTENT) still uses reload; bot detection restarts.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

const (
	// botExemptConfigPath is the jabali-owned challenge-exemption config,
	// composed into the acquis only while bot detection is on.
	botExemptConfigPath = "/etc/crowdsec/appsec-configs/jabali-bot-exempt.yaml"
	// appsecAcquisPath is the AppSec acquisition (the :7422 datasource).
	// install.sh seeds it; this handler rewrites it to add/remove the
	// bot-challenge composition.
	appsecAcquisPath = "/etc/crowdsec/acquis.d/jabali-appsec.yaml"
)

// csBotDetectionModes is the closed enum shared by the UI, panel-api and
// agent. "balanced" rejects at fingerprint score >= 75, "permissive" >= 100.
var csBotDetectionModes = map[string]struct{}{
	"off":        {},
	"balanced":   {},
	"permissive": {},
}

type csBotDetectionResponse struct {
	Mode string `json:"mode"`
}

// botCollectionsForMode returns the hub collections to install for an ON
// mode: the threshold collection plus the good-bot and path exemptions
// (verified search engines / AI / social / monitoring, and crawler files /
// static assets / API / feeds / webhooks).
func botCollectionsForMode(mode string) []string {
	threshold := "crowdsecurity/appsec-bot-challenge" // balanced
	if mode == "permissive" {
		threshold = "crowdsecurity/appsec-bot-challenge-permissive"
	}
	return []string{
		threshold,
		"crowdsecurity/appsec-bot-challenge-good-bots",
		"crowdsecurity/appsec-bot-challenge-exclude-paths",
	}
}

// readBotDetectionMode reports the applied bot-detection mode. It prefers the
// `# jabali-bot-detection:` marker in jabali-appsec.yaml, but falls back to
// SNIFFING THE ACQUIS when the marker is missing or off — because the acquis
// is the applied truth: a stale panel binary running the old `render-config`
// rewrites jabali-appsec.yaml without the marker (the header ages out at the
// deploy-panel-api-too boundary) while install.sh's plural-guard keeps the
// composed acquis. Without the fallback the agent's readback would then lie
// "off" while crowdsec is actively challenging. The threshold is recovered
// from the scoring config the acquis lists.
func readBotDetectionMode() string {
	if body, err := os.ReadFile(appsecRulePath); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# jabali-bot-detection:") {
				m := strings.TrimSpace(strings.TrimPrefix(line, "# jabali-bot-detection:"))
				if _, ok := csBotDetectionModes[m]; ok && m != "off" {
					return m
				}
				break // header present but off/unknown → sniff the acquis
			}
		}
	}
	return sniffBotModeFromAcquis()
}

// sniffBotModeFromAcquis derives the mode from the AppSec acquisition: a
// plural `appsec_configs:` list means bot detection is on, and the presence
// of the permissive scoring config distinguishes the threshold. Singular /
// absent acquis → off.
func sniffBotModeFromAcquis() string {
	body, err := os.ReadFile(appsecAcquisPath)
	if err != nil {
		return "off"
	}
	return botModeFromAcquisBody(string(body))
}

// botModeFromAcquisBody is the pure core of sniffBotModeFromAcquis: a plural
// `appsec_configs:` list ⇒ on, and the permissive scoring config ⇒ the
// permissive threshold. Singular / absent ⇒ off.
func botModeFromAcquisBody(s string) string {
	if !strings.Contains(s, "appsec_configs:") {
		return "off"
	}
	if strings.Contains(s, "scoring-permissive") {
		return "permissive"
	}
	return "balanced"
}

// readGeoblockState parses the geoblock mode + countries back out of the
// jabali-appsec.yaml header so a bot-detection change re-renders the config
// without wiping the operator's geoblock selection (and vice-versa).
func readGeoblockState() (string, []string) {
	mode := "off"
	var countries []string
	body, err := os.ReadFile(appsecRulePath)
	if err != nil {
		return mode, countries
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# jabali-mode:"):
			mode = strings.TrimSpace(strings.TrimPrefix(line, "# jabali-mode:"))
		case strings.HasPrefix(line, "# jabali-countries:"):
			csv := strings.TrimSpace(strings.TrimPrefix(line, "# jabali-countries:"))
			if csv != "" {
				countries = strings.Split(csv, ",")
			}
		}
	}
	if _, ok := csAppSecGeoblockModes[mode]; !ok {
		mode = "off"
	}
	return mode, countries
}

// renderJabaliAppsec is the single call site for appseccfg.Render used by
// both the geoblock and bot-detection handlers, so neither resets the
// other's header marker. Webmail allowlist comes from the panel reconciler's
// state file (missing → no webmail filter, the safe default).
func renderJabaliAppsec(geoMode string, countries []string, botMode string) string {
	webmailHosts, _ := appseccfg.LoadWebmailHosts(appseccfg.WebmailHostsPath)
	return appseccfg.Render(appseccfg.Opts{
		Mode:      geoMode,
		Countries: countries,
		Inband: []string{
			"crowdsecurity/base-config",
			"crowdsecurity/vpatch-*",
			"crowdsecurity/generic-*",
		},
		AdminAllowlist: true,
		PanelHost:      appsecPanelHost(),
		WebmailHosts:   webmailHosts,
		BotDetection:   botMode,
	})
}

func csBotDetectionGetHandler(_ context.Context, _ json.RawMessage) (any, error) {
	return csBotDetectionResponse{Mode: readBotDetectionMode()}, nil
}

func csBotDetectionSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if _, ok := csBotDetectionModes[p.Mode]; !ok {
		return nil, csInvalidArg("mode must be off|balanced|permissive")
	}
	geoMode, countries := readGeoblockState()
	if p.Mode == "off" {
		return botDetectionApplyOff(ctx, geoMode, countries)
	}
	if err := requireBotDetectionSupport(ctx); err != nil {
		return nil, err
	}
	return botDetectionApplyOn(ctx, p.Mode, geoMode, countries)
}

// botDetectionApplyOn installs the collections, composes the acquis, and
// restarts — validating with `crowdsec -t` before the restart and rolling
// the files back on failure so a bad config can never take AppSec down.
func botDetectionApplyOn(ctx context.Context, mode, geoMode string, countries []string) (any, error) {
	if err := installBotCollections(ctx, mode); err != nil {
		return nil, err
	}
	leaves, err := enumerateBotChallengeConfigs(ctx)
	if err != nil {
		return nil, err
	}
	leaves = filterThreshold(leaves, mode)
	if len(leaves) == 0 {
		return nil, csInternal("bot-challenge configs", fmt.Errorf("no appsec-bot-challenge configs found after install — hub fetch may have failed"))
	}

	if err := os.MkdirAll("/etc/crowdsec/appsec-configs", 0o755); err != nil {
		return nil, csInternal("mkdir appsec-configs", err)
	}
	// Snapshot the files we are about to overwrite, for rollback.
	prevAcquis, hadAcquis := readFileOK(appsecAcquisPath)
	prevAppsec, hadAppsec := readFileOK(appsecRulePath)
	_, hadExempt := readFileOK(botExemptConfigPath)

	if err := writeAppsecFile(botExemptConfigPath, appseccfg.RenderBotExempt(botExemptOpts())); err != nil {
		return nil, err
	}
	if err := writeAppsecFile(appsecRulePath, renderJabaliAppsec(geoMode, countries, mode)); err != nil {
		return nil, err
	}
	acquis := appseccfg.RenderAcquis(appseccfg.Opts{BotDetection: mode, BotConfigs: leaves})
	if err := writeAppsecFile(appsecAcquisPath, acquis); err != nil {
		return nil, err
	}

	if verr := validateCrowdsecConfig(ctx); verr != nil {
		// Roll the files back to their pre-change state; leave the running
		// crowdsec untouched (we never restarted), so AppSec keeps serving.
		rollbackAppsecFiles(prevAcquis, hadAcquis, prevAppsec, hadAppsec, hadExempt)
		return nil, csInvalidArg(fmt.Sprintf("bot detection config failed validation, no change applied: %v", verr))
	}
	if err := restartCrowdsec(ctx); err != nil {
		return nil, err
	}
	return csBotDetectionResponse{Mode: mode}, nil
}

// botDetectionApplyOff returns the acquis to the singular jabali-appsec form
// and drops the exemption config. The bot-challenge collections are left
// installed (harmless while unreferenced — configs load only via an acquis —
// and a re-enable then skips the hub download).
func botDetectionApplyOff(ctx context.Context, geoMode string, countries []string) (any, error) {
	prevAcquis, hadAcquis := readFileOK(appsecAcquisPath)
	prevAppsec, hadAppsec := readFileOK(appsecRulePath)

	if err := writeAppsecFile(appsecRulePath, renderJabaliAppsec(geoMode, countries, "off")); err != nil {
		return nil, err
	}
	if err := writeAppsecFile(appsecAcquisPath, appseccfg.RenderAcquis(appseccfg.Opts{BotDetection: "off"})); err != nil {
		return nil, err
	}
	if err := os.Remove(botExemptConfigPath); err != nil && !os.IsNotExist(err) {
		return nil, csInternal("remove bot-exempt config", err)
	}
	if verr := validateCrowdsecConfig(ctx); verr != nil {
		rollbackAppsecFiles(prevAcquis, hadAcquis, prevAppsec, hadAppsec, false)
		return nil, csInternal("crowdsec -t after disabling bot detection", verr)
	}
	if err := restartCrowdsec(ctx); err != nil {
		return nil, err
	}
	return csBotDetectionResponse{Mode: "off"}, nil
}

// botExemptOpts feeds RenderBotExempt the panel host + webmail allowlist that
// jabali-appsec uses (so the built-in exemptions match the on_match allowlist)
// PLUS the operator's per-domain opt-out list (both from the panel reconciler's
// state files; missing → the safe default of no exemption).
func botExemptOpts() appseccfg.Opts {
	webmailHosts, _ := appseccfg.LoadWebmailHosts(appseccfg.WebmailHostsPath)
	exemptHosts, _ := appseccfg.LoadBotExemptHosts(appseccfg.BotExemptHostsPath)
	return appseccfg.Opts{
		PanelHost:      appsecPanelHost(),
		WebmailHosts:   webmailHosts,
		BotExemptHosts: exemptHosts,
	}
}

// csBotDetectionRefreshExemptHandler re-renders jabali-bot-exempt.yaml from the
// current per-domain opt-out state file and reloads crowdsec on a real change.
// The panel reconciler calls this EVERY pass while bot detection is on (a
// per-tick idempotent loop, not a change-triggered one — a single failed call
// must never leave the exemptions stale: write-on-diff makes the steady state a
// cheap render+memcmp, and convergence is guaranteed).
//
// A change here is to an appsec-config's CONTENT, not the acquisition's
// datasource list, so SIGHUP (reload) is enough — same as geoblock. Bot
// detection off ⇒ the exempt config isn't composed into the acquis, so there is
// nothing to reload; the next botdetection.set renders it fresh.
func csBotDetectionRefreshExemptHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	mode := readBotDetectionMode()
	if mode == "off" {
		return map[string]any{"mode": "off", "reloaded": false}, nil
	}
	body := appseccfg.RenderBotExempt(botExemptOpts())
	if existing, err := os.ReadFile(botExemptConfigPath); err == nil && string(existing) == body {
		return map[string]any{"mode": mode, "reloaded": false}, nil
	}
	if err := writeAppsecFile(botExemptConfigPath, body); err != nil {
		return nil, err
	}
	if err := reloadCrowdsec(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"mode": mode, "reloaded": true}, nil
}

func installBotCollections(ctx context.Context, mode string) error {
	args := append([]string{"collections", "install"}, botCollectionsForMode(mode)...)
	if out, err := execCommandContext(ctx, "cscli", args...).CombinedOutput(); err != nil {
		return csInternal("cscli collections install (bot-challenge)",
			fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out))))
	}
	return nil
}

// enumerateBotChallengeConfigs lists the enabled appsec-CONFIG names that
// belong to the bot-challenge set. The leaf set is version-dependent, so we
// discover it from cscli rather than hardcode it (a collection NAME in the
// acquis would FATAL the engine — only leaf config names are valid).
func enumerateBotChallengeConfigs(ctx context.Context) ([]string, error) {
	out, err := execCommandContext(ctx, "cscli", "appsec-configs", "list", "-o", "json").Output()
	if err != nil {
		return nil, csInternal("cscli appsec-configs list", err)
	}
	var parsed struct {
		Configs []struct {
			Name string `json:"name"`
		} `json:"appsec-configs"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, csInternal("parse appsec-configs json", err)
	}
	var names []string
	for _, c := range parsed.Configs {
		if strings.Contains(c.Name, "bot-challenge") {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// filterThreshold drops the OTHER threshold's scoring config so switching
// balanced<->permissive can't leave both loaded (double-scoring).
func filterThreshold(leaves []string, mode string) []string {
	drop := "scoring-permissive"
	if mode == "permissive" {
		drop = "scoring-balanced"
	}
	out := make([]string, 0, len(leaves))
	for _, n := range leaves {
		if strings.Contains(n, drop) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// requireBotDetectionSupport version-gates: the challenge funcs
// (SendChallenge/ExemptFromChallenge) exist only in CrowdSec engine >= 1.8,
// and the bouncer must be >= 1.2.x to serve the challenge (challenge.lua).
func requireBotDetectionSupport(ctx context.Context) error {
	ev := crowdsecEngineVersion(ctx)
	if !versionAtLeast(ev, 1, 8) {
		return csInvalidArg(fmt.Sprintf(
			"bot detection needs CrowdSec engine >= 1.8 (have %q) — upgrade crowdsec first",
			ev))
	}
	bv := bouncerVersion(ctx)
	if !versionAtLeast(bv, 1, 2) {
		return csInvalidArg(fmt.Sprintf(
			"bot detection needs crowdsec-nginx-bouncer >= 1.2.2 (have %q) — upgrade the bouncer first",
			bv))
	}
	return nil
}

func crowdsecEngineVersion(ctx context.Context) string {
	out, err := execCommandContext(ctx, "cscli", "version").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "version:") {
			return strings.TrimSpace(line[len("version:"):])
		}
	}
	return ""
}

func bouncerVersion(ctx context.Context) string {
	out, err := execCommandContext(ctx, "dpkg-query", "-W", "-f=${Version}", "crowdsec-nginx-bouncer").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// versionAtLeast compares a version string ("v1.8.0-debian-...", "1.2.2") to
// wantMajor.wantMinor. Unparseable → false (fail-closed: the gate refuses).
func versionAtLeast(v string, wantMajor, wantMinor int) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	maj, e1 := strconv.Atoi(parts[0])
	min, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return false
	}
	if maj != wantMajor {
		return maj > wantMajor
	}
	return min >= wantMinor
}

func validateCrowdsecConfig(ctx context.Context) error {
	if out, err := execCommandContext(ctx, "crowdsec", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restartCrowdsec is used (not reloadCrowdsec) whenever the acquisition
// changes — SIGHUP does not bind new/changed datasources.
func restartCrowdsec(ctx context.Context) error {
	if out, err := execCommandContext(ctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err != nil {
		return csInternal("systemctl restart crowdsec", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out))))
	}
	return nil
}

func readFileOK(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

// writeAppsecFile wraps the shared writeFileAtomic with a csInternal error so
// a failure surfaces cleanly over the agent wire.
func writeAppsecFile(path, body string) error {
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return csInternal("write "+path, err)
	}
	return nil
}

// rollbackAppsecFiles restores the acquis + jabali-appsec to their pre-change
// bytes and removes the exemption config if it did not exist before. Called
// only on a `crowdsec -t` failure, before any restart — so the still-running
// crowdsec keeps its known-good in-memory config while the files on disk are
// returned to a state that will start cleanly.
func rollbackAppsecFiles(prevAcquis []byte, hadAcquis bool, prevAppsec []byte, hadAppsec, hadExempt bool) {
	if hadAcquis {
		_ = writeAppsecFile(appsecAcquisPath, string(prevAcquis))
	}
	if hadAppsec {
		_ = writeAppsecFile(appsecRulePath, string(prevAppsec))
	}
	if !hadExempt {
		_ = os.Remove(botExemptConfigPath)
	}
}
