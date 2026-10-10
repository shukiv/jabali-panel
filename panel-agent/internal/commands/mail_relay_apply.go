package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/smarthost"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/mailrelay"
)

// mail.relay.apply (GH #2056, ADR 0174) — where the sites' PHP mail() goes.
//
//	mode "smarthost": write the relay config (0640 root:jabali-mailrelay),
//	  start jabali-mailrelay, wait for its socket, then point the shim at it.
//	mode "local": point the shim back at the local mail server first, then
//	  stop the relay and delete its config, the smarthost password with it.
//
// The order matters both ways: the shim never points at a relay that isn't
// listening, and the password never outlives smarthost mode. The panel sends
// the full sender list every time, so a missed call heals on the next one.

const (
	mailRelayUnit  = "jabali-mailrelay.service"
	mailRelayGroup = "jabali-mailrelay"
)

// Test seams.
var (
	mailRelayConfigPath  = mailrelay.ConfigPath
	mailRelayModePath    = mailrelay.ModePath
	mailRelaySocketPath  = mailrelay.SocketPath
	mailRelayLookupUser  = user.Lookup
	mailRelayLookupGroup = user.LookupGroup
	mailRelaySocketWait  = 10 * time.Second
)

var mailRelayMu sync.Mutex

type mailRelaySender struct {
	Username string   `json:"username"`
	Domains  []string `json:"domains"`
	Default  string   `json:"default"`
}

type mailRelayApplyParams struct {
	Mode      string               `json:"mode"`
	Smarthost *mailrelay.Smarthost `json:"smarthost,omitempty"`
	Senders   []mailRelaySender    `json:"senders,omitempty"`
}

type mailRelayApplyResponse struct {
	Ok      bool   `json:"ok"`
	Mode    string `json:"mode"`
	Changed bool   `json:"changed"`
	Senders int    `json:"senders"`
	// Skipped names the senders left out and why (no such system user, no
	// valid domain).
	Skipped []string `json:"skipped,omitempty"`
}

func mailRelayApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailRelayApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	mailRelayMu.Lock()
	defer mailRelayMu.Unlock()
	switch p.Mode {
	case mailrelay.ModeLocal:
		return mailRelayApplyLocal(ctx)
	case mailrelay.ModeSmarthost:
		return mailRelayApplySmarthost(ctx, p)
	default:
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: `mode must be "local" or "smarthost"`}
	}
}

func mailRelayApplyLocal(ctx context.Context) (any, error) {
	changed, err := mailRelayWriteMode(mailrelay.ModeLocal)
	if err != nil {
		return nil, err
	}
	if out, err := runSystemctl(ctx, "disable", "--now", mailRelayUnit); err != nil && !mailRelayUnitMissing(out) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "stop the mail relay: " + systemctlDetail(out)}
	}
	switch err := os.Remove(mailRelayConfigPath); {
	case err == nil:
		changed = true
	case !os.IsNotExist(err):
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove relay config: %v", err)}
	}
	return mailRelayApplyResponse{Ok: true, Mode: mailrelay.ModeLocal, Changed: changed}, nil
}

// mailRelayUnitMissing: a box that never installed the relay has nothing to
// stop.
func mailRelayUnitMissing(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "does not exist") || strings.Contains(s, "not found") || strings.Contains(s, "not loaded")
}

func mailRelayApplySmarthost(ctx context.Context, p mailRelayApplyParams) (any, error) {
	if p.Smarthost == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "smarthost required"}
	}
	sh := *p.Smarthost
	if err := smarthost.Validate(sh.SmarthostConfig()); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if sh.Helo != "" {
		if err := smarthost.Validate(smarthost.Config{Host: sh.Helo, Port: sh.Port, TLS: sh.TLS}); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid helo name"}
		}
	}
	gid, err := mailRelayAccountGID()
	if err != nil {
		return nil, err
	}

	cfg := mailrelay.Config{Smarthost: sh, Senders: map[string]mailrelay.Sender{}}
	var skipped []string
	for _, s := range p.Senders {
		uid, sender, why := mailRelayResolveSender(s)
		if why != "" {
			skipped = append(skipped, s.Username+": "+why)
			continue
		}
		cfg.Senders[uid] = sender
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("marshal relay config: %v", err)}
	}

	dir := filepath.Dir(mailRelayConfigPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v", dir, err)}
	}
	if err := sendmailChown(dir, 0, gid); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown %s: %v", dir, err)}
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod %s: %v", dir, err)}
	}
	changed := false
	if existing, rerr := os.ReadFile(mailRelayConfigPath); rerr != nil || string(existing) != string(data) {
		if err := mailRelayWriteConfig(data, gid); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write relay config: %v", err)}
		}
		changed = true
	}

	if out, err := runSystemctl(ctx, "enable", "--now", mailRelayUnit); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "start the mail relay: " + systemctlDetail(out)}
	}
	if err := mailRelayWaitSocket(ctx); err != nil {
		return nil, err
	}
	modeChanged, err := mailRelayWriteMode(mailrelay.ModeSmarthost)
	if err != nil {
		return nil, err
	}
	return mailRelayApplyResponse{
		Ok:      true,
		Mode:    mailrelay.ModeSmarthost,
		Changed: changed || modeChanged,
		Senders: len(cfg.Senders),
		Skipped: skipped,
	}, nil
}

// mailRelayAccountGID is the relay group's gid, once the relay account is
// known to be the dedicated system user: a system uid that isn't root, whose
// primary group is the relay group. The config holds the smarthost password,
// and that group may read it.
func mailRelayAccountGID() (int, error) {
	missing := &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "the jabali-mailrelay user is missing; run jabali update"}
	u, err := mailRelayLookupUser(mailRelayGroup)
	if err != nil {
		return 0, missing
	}
	grp, err := mailRelayLookupGroup(mailRelayGroup)
	if err != nil {
		return 0, missing
	}
	uid, uerr := strconv.Atoi(u.Uid)
	gid, gerr := strconv.Atoi(grp.Gid)
	if uerr != nil || gerr != nil || uid <= 0 || uid >= mailRelayFirstLoginUID || u.Gid != grp.Gid {
		return 0, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "the jabali-mailrelay account isn't the dedicated system user; run jabali update"}
	}
	return gid, nil
}

// mailRelayFirstLoginUID is where login accounts start (UID_MIN). Site users
// are at or above it; system accounts below it never send website mail.
const mailRelayFirstLoginUID = 1000

// mailRelayResolveSender checks one sender and finds its UID. why is set
// when the sender is left out.
func mailRelayResolveSender(s mailRelaySender) (uid string, sender mailrelay.Sender, why string) {
	if !phpPoolUsernameRegex.MatchString(s.Username) {
		return "", sender, "invalid username"
	}
	seen := map[string]bool{}
	var domains []string
	for _, d := range s.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !sendmailDomainRe.MatchString(d) || strings.Contains(d, "..") || seen[d] {
			continue
		}
		seen[d] = true
		domains = append(domains, d)
	}
	if len(domains) == 0 {
		return "", sender, "no valid domain"
	}
	def := strings.ToLower(strings.TrimSpace(s.Default))
	if !seen[def] {
		def = domains[0]
	}
	u, err := mailRelayLookupUser(s.Username)
	if err != nil {
		return "", sender, "no such system user"
	}
	n, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return "", sender, "bad uid"
	}
	if n < mailRelayFirstLoginUID || n == 65534 {
		return "", sender, "a system account may not send"
	}
	return u.Uid, mailrelay.Sender{User: s.Username, Domains: domains, Default: def}, ""
}

// mailRelayWriteConfig replaces the config with its final owner and mode
// already set, so the relay never sees a file it can't read.
func mailRelayWriteConfig(data []byte, gid int) error {
	tmp := mailRelayConfigPath + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := sendmailChown(tmp, 0, gid); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, mailRelayConfigPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// mailRelayWriteMode points the shim at the relay or the local mail server.
// The file is world-readable: it holds no secret, and the shim runs as the
// site's user.
func mailRelayWriteMode(mode string) (bool, error) {
	content := mode + "\n"
	if existing, err := os.ReadFile(mailRelayModePath); err == nil && string(existing) == content {
		return false, nil
	}
	dir := filepath.Dir(mailRelayModePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o711); err != nil {
			return false, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v", dir, err)}
		}
	}
	if err := writeAtomic(mailRelayModePath, []byte(content), 0o644); err != nil {
		return false, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write website mail mode: %v", err)}
	}
	return true, nil
}

func mailRelayWaitSocket(ctx context.Context) error {
	deadline := time.Now().Add(mailRelaySocketWait)
	for {
		if fi, err := os.Stat(mailRelaySocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "the mail relay didn't start; see journalctl -u " + mailRelayUnit}
		}
		select {
		case <-ctx.Done():
			return &agentwire.AgentError{Code: agentwire.CodeDeadlineExceeded, Message: "waiting for the mail relay: " + ctx.Err().Error()}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// StartMailRelayIfSelected runs at agent start. The relay is PartOf the agent,
// so stopping the agent stops it, but starting the agent again doesn't: bring
// it back when the smarthost is selected. --no-block, because the agent's own
// start job is still running.
func StartMailRelayIfSelected(ctx context.Context, log *slog.Logger) {
	if mailrelay.ReadMode(mailRelayModePath) != mailrelay.ModeSmarthost {
		return
	}
	if out, err := runSystemctl(ctx, "start", "--no-block", mailRelayUnit); err != nil {
		log.Warn("website mail relay didn't start", "detail", systemctlDetail(out))
	}
}

func init() {
	Default.Register("mail.relay.apply", mailRelayApplyHandler)
}
