package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// security.trust.test — agent-side IP test bench (M43 / ADR-0089).
//
// Was historically run inline by panel-api (uid=jabali); cscli reads
// /etc/crowdsec/config.yaml (root-readable) and ufw needs root for
// the netfilter inspect, so both calls EACCES'd and the trust bench
// returned "unknown" with a confusing error. Routing through the
// agent (uid=root) is the correct fix — same pattern every other
// security-tab endpoint uses (M26 ADR-0053).

type trustTestParams struct {
	IP string `json:"ip"`
}

type trustVerdict struct {
	Layer   string `json:"layer"`
	Outcome string `json:"outcome"` // allow | deny | unknown
	Detail  string `json:"detail"`
}

type trustTestResponse struct {
	IP       string         `json:"ip"`
	Verdicts []trustVerdict `json:"verdicts"`
}

func trustTestHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p trustTestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse params: %v", err),
		}
	}
	ip := strings.TrimSpace(p.IP)
	if ip == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "ip required",
		}
	}
	if net.ParseIP(ip) == nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid IP: %q", ip),
		}
	}

	return trustTestResponse{
		IP: ip,
		Verdicts: []trustVerdict{
			trustCrowdSecVerdict(ctx, ip),
			trustUfwVerdict(ctx, ip),
		},
	}, nil
}

func trustCrowdSecVerdict(ctx context.Context, ip string) trustVerdict {
	if _, err := exec.LookPath("cscli"); err != nil {
		return trustVerdict{Layer: "crowdsec", Outcome: "unknown", Detail: "cscli not installed"}
	}
	out, err := runCmdWithStderr(execCommandContext(ctx, "cscli", "decisions", "list", "-i", ip, "-o", "json"))
	if err != nil {
		return trustVerdict{Layer: "crowdsec", Outcome: "unknown", Detail: "cscli error: " + err.Error()}
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return trustVerdict{Layer: "crowdsec", Outcome: "allow", Detail: "no active decision"}
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return trustVerdict{Layer: "crowdsec", Outcome: "unknown", Detail: "parse: " + err.Error()}
	}
	if len(rows) == 0 {
		return trustVerdict{Layer: "crowdsec", Outcome: "allow", Detail: "no active decision"}
	}
	return trustVerdict{
		Layer:   "crowdsec",
		Outcome: "deny",
		Detail:  "active decision(s) present (count=" + strconv.Itoa(len(rows)) + ")",
	}
}

func trustUfwVerdict(ctx context.Context, ip string) trustVerdict {
	if _, err := exec.LookPath("ufw"); err != nil {
		return trustVerdict{Layer: "ufw", Outcome: "unknown", Detail: "ufw not installed"}
	}
	out, err := runCmdWithStderr(execCommandContext(ctx, "ufw", "status", "numbered"))
	if err != nil {
		return trustVerdict{Layer: "ufw", Outcome: "unknown", Detail: "ufw error: " + err.Error()}
	}
	if strings.Contains(string(out), ip) {
		return trustVerdict{
			Layer:   "ufw",
			Outcome: "deny",
			Detail:  "matching UFW rule found",
		}
	}
	return trustVerdict{Layer: "ufw", Outcome: "allow", Detail: "no matching UFW rule"}
}

// runCmdWithStderr is the agent-side mirror of panel-api's runWithStderr.
// Returns the bare cmd.Run error plus a trimmed stderr suffix when
// non-empty, so the trust bench surfaces real diagnostics instead of
// "exit status 1".
func runCmdWithStderr(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 256 {
			msg = msg[:256] + "…"
		}
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

func init() {
	Default.Register("security.trust.test", trustTestHandler)
}
