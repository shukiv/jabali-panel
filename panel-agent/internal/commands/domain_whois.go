package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// domainWhoisParams is the input for domain.whois.
type domainWhoisParams struct {
	Domain string `json:"domain"`
}

// domainWhoisResult carries the raw whois output plus a handful of
// best-effort parsed fields. Raw is ALWAYS populated when whois produced
// any output — the parsed fields are convenience only and are empty for
// TLDs/registrars whose label format we don't recognise. The panel renders
// raw verbatim, so an unrecognised format degrades to "raw only", never an
// error.
type domainWhoisResult struct {
	Domain      string   `json:"domain"`
	Registrar   string   `json:"registrar,omitempty"`
	CreatedDate string   `json:"created_date,omitempty"`
	UpdatedDate string   `json:"updated_date,omitempty"`
	ExpiryDate  string   `json:"expiry_date,omitempty"`
	Registrant  string   `json:"registrant,omitempty"`
	NameServers []string `json:"name_servers,omitempty"`
	Statuses    []string `json:"statuses,omitempty"`
	Raw         string   `json:"raw"`
}

// domainWhoisHandler runs `whois <domain>` and returns raw + parsed output.
//
// Safety: domain is passed as a separate arg AFTER `--` so a stored value
// like "-h evil.host" can never be read as a flag (whois -h redirects the
// query server). It comes from the DB, not raw user input, but `--` is a
// cheap guarantee. No shell string is constructed anywhere.
//
// whois opens TCP/43 to the registry — a network call that can hang — so it
// runs under the caller's ctx via CommandContext, which kills the process on
// deadline. The panel sets a ~20s ceiling.
func domainWhoisHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainWhoisParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	domain := strings.TrimSpace(p.Domain)
	if domain == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain required"}
	}

	var out, errb bytes.Buffer
	cmd := execCommandContext(ctx, "whois", "--", domain)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()

	// ctx deadline/cancel wins over a generic non-zero exit.
	if ctx.Err() != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("whois timed out: %v", ctx.Err())}
	}

	raw := out.String()
	// whois exits non-zero for "no match" / rate-limit while still printing
	// useful text — treat output as authoritative. Only surface an error when
	// there's nothing to show AND the binary failed (e.g. not installed).
	if strings.TrimSpace(raw) == "" {
		if runErr != nil {
			msg := strings.TrimSpace(errb.String())
			if msg == "" {
				msg = runErr.Error()
			}
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("whois failed: %s", msg)}
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "no whois data for domain"}
	}

	res := parseWhois(domain, raw)
	return res, nil
}

// parseWhois pulls common fields out of raw whois text. Key labels vary by
// TLD/registrar, so each field matches a set of case-insensitive aliases and
// keeps the FIRST non-empty value for scalars (the registry block usually
// comes before the registrar block). Name servers and statuses accumulate
// (deduped, case-insensitive). Exported indirectly via the result; split out
// for unit testing against saved fixtures.
func parseWhois(domain, raw string) domainWhoisResult {
	res := domainWhoisResult{Domain: domain, Raw: raw}
	seenNS := map[string]bool{}
	seenStatus := map[string]bool{}

	setScalar := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v
		}
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		if val == "" {
			continue
		}
		switch {
		case key == "registrar" || key == "sponsoring registrar" || key == "registrar name":
			setScalar(&res.Registrar, val)
		case key == "creation date" || key == "created" || key == "created on" ||
			key == "registered on" || key == "registration time" || key == "domain registration date":
			setScalar(&res.CreatedDate, val)
		case key == "updated date" || key == "last updated" || key == "last modified" ||
			key == "last update" || key == "updated on" || key == "changed":
			setScalar(&res.UpdatedDate, val)
		case key == "registry expiry date" || key == "registrar registration expiration date" ||
			key == "expiry date" || key == "expiration date" || key == "expires on" ||
			key == "expire" || key == "paid-till" || key == "renewal date":
			setScalar(&res.ExpiryDate, val)
		case key == "registrant" || key == "registrant name" || key == "registrant organization" ||
			key == "registrant org" || key == "org" || key == "organization":
			setScalar(&res.Registrant, val)
		case key == "name server" || key == "nameserver" || key == "nserver" || key == "name servers":
			ns := strings.ToLower(strings.Fields(val)[0]) // some registries append an IP
			if !seenNS[ns] {
				seenNS[ns] = true
				res.NameServers = append(res.NameServers, ns)
			}
		case key == "domain status" || key == "status":
			st := val
			if !seenStatus[strings.ToLower(st)] {
				seenStatus[strings.ToLower(st)] = true
				res.Statuses = append(res.Statuses, st)
			}
		}
	}
	return res
}

func init() {
	Default.Register("domain.whois", domainWhoisHandler)
}
