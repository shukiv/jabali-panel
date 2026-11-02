package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail.elsewhere.sync tells Stalwart which domains it must NOT act as the mail
// server for: email is off, or the domain's mail is hosted by another provider
// (models.MailHostedElsewhere). The panel sends the full list every time.
//
// Why this is needed: Stalwart treats every domain in its registry as local,
// and a domain stays registered while any account on it is left. Every domain
// keeps a send-only noreply@ for its website's mail, so a domain whose mail
// moved to Google Workspace stayed local: mail from this server to it (a
// contact form, another tenant) was refused with "Mailbox does not exist"
// instead of being sent to Google.
//
// The installer (converge_stalwart_mail_elsewhere) sets the global part, which
// only takes effect after a Stalwart restart:
//   - the outbound route and queue schedule send mail for a registered domain
//     whose name is in the in-memory lookup "jabali-mail-elsewhere" by its MX,
//     like any other remote domain;
//   - the RCPT stage runs the system Sieve script "jabali-mail-elsewhere"
//     (550 5.7.1) for unauthenticated mail to such a domain.
//
// This verb keeps the per-domain part, which is live without a restart:
//   - one lookup key per listed domain (x:MemoryLookupKey, namespace
//     "jabali-mail-elsewhere"); a key change is followed by ReloadLookupStores
//     (ReloadSettings does not reload lookup keys, verified on 0.16);
//   - x:Domain.allowRelaying on each listed domain that Stalwart has, so an
//     authenticated sender's mail to an address with no mailbox here is
//     accepted and sent to the MX instead of refused. allowRelaying alone
//     would also accept unauthenticated mail for the domain; the RCPT script
//     refuses that, so allowRelaying is only turned on when the RCPT script is
//     configured, and only for a domain whose lookup key is already live.
//
// The list lives in Stalwart, not in a SQL lookup: with a SQL lookup, an
// unreachable database made the expressions fail, Stalwart fell back to its
// built-in route, and unauthenticated mail to such a domain was accepted and
// relayed to the domain's MX (drill on 0.16, 2026-11-02). key_exists() reads
// memory and cannot fail that way.
//
// allowRelaying is owned here for every domain in Stalwart: a domain that is
// not listed is set to false. Nothing else in Jabali sets it, and true on a
// hosted domain would accept mail for addresses that don't exist.

const (
	// mailElsewhereNamespace is the in-memory lookup the installer's
	// expressions read with key_exists(). Keep in step with install.sh
	// converge_stalwart_mail_elsewhere.
	mailElsewhereNamespace = "jabali-mail-elsewhere"
	// mailElsewhereMaxDomains bounds one request.
	mailElsewhereMaxDomains = 100000
)

// mailElsewhereRcptMarkers must all appear in the MtaStageRcpt script for the
// RCPT refusal to count as configured: the lookup test and the Sieve script
// name it runs.
var mailElsewhereRcptMarkers = []string{
	"key_exists('" + mailElsewhereNamespace + "', rcpt_domain)",
	"'" + mailElsewhereNamespace + "'",
}

var mailElsewhereDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)

var (
	mailElsewhereMu sync.Mutex
	// mailElsewhereReloadPending is set when lookup keys were written but the
	// reload that makes them live failed, so the next call reloads even when
	// the keys already match.
	mailElsewhereReloadPending bool
)

type mailElsewhereSyncParams struct {
	Domains []string `json:"domains"`
}

type mailElsewhereSyncResponse struct {
	// Skipped: Stalwart was not reachable; nothing was changed.
	Skipped bool `json:"skipped,omitempty"`
	// Routing: the RCPT refusal is configured, so allowRelaying may be on.
	Routing     bool `json:"routing"`
	KeysAdded   int  `json:"keys_added"`
	KeysRemoved int  `json:"keys_removed"`
	RelayOn     int  `json:"relay_on"`
	RelayOff    int  `json:"relay_off"`
}

type mailElsewhereDomainRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	AllowRelaying bool   `json:"allowRelaying"`
}

type mailElsewhereKeyRow struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

func mailElsewhereSyncHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailElsewhereSyncParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if len(p.Domains) > mailElsewhereMaxDomains {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "too many domains"}
	}
	want := make(map[string]bool, len(p.Domains))
	for _, d := range p.Domains {
		name := strings.ToLower(d)
		if !mailElsewhereDomainRe.MatchString(name) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid domain %q", d)}
		}
		want[name] = true
	}

	mailElsewhereMu.Lock()
	defer mailElsewhereMu.Unlock()

	resp, err := mailElsewhereSync(ctx, want)
	if err != nil && isStalwartUnavailable(err) {
		return mailElsewhereSyncResponse{Skipped: true}, nil
	}
	return resp, err
}

func mailElsewhereSync(ctx context.Context, want map[string]bool) (mailElsewhereSyncResponse, error) {
	var resp mailElsewhereSyncResponse

	routing, err := mailElsewhereRoutingConfigured(ctx)
	if err != nil {
		return resp, err
	}
	resp.Routing = routing

	domains, err := mailElsewhereStalwartDomains(ctx)
	if err != nil {
		return resp, err
	}
	keys, err := mailElsewhereKeys(ctx)
	if err != nil {
		return resp, err
	}

	// 1. allowRelaying off first, so no domain relays while its key is gone.
	off := map[string]any{}
	for _, d := range domains {
		if d.AllowRelaying && !(routing && want[strings.ToLower(d.Name)]) {
			off[d.ID] = map[string]any{"allowRelaying": false}
		}
	}
	if err := mailElsewhereUpdateDomains(ctx, off); err != nil {
		return resp, err
	}
	resp.RelayOff = len(off)

	// 2. Lookup keys: exactly one per listed domain.
	create := map[string]any{}
	var destroy []string
	have := map[string]bool{}
	for _, k := range keys {
		name := strings.ToLower(k.Key)
		if !want[name] || have[name] {
			destroy = append(destroy, k.ID)
			continue
		}
		have[name] = true
	}
	names := make([]string, 0, len(want))
	for name := range want {
		if !have[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for i, name := range names {
		create[fmt.Sprintf("k%d", i)] = map[string]any{
			"namespace":     mailElsewhereNamespace,
			"key":           name,
			"isGlobPattern": false,
		}
	}
	if len(create) > 0 || len(destroy) > 0 {
		args := map[string]any{}
		if len(create) > 0 {
			args["create"] = create
		}
		if len(destroy) > 0 {
			args["destroy"] = destroy
		}
		var sr jmapSetResult
		if err := jmapCall(ctx, "x:MemoryLookupKey/set", args, &sr); err != nil {
			return resp, err
		}
		mailElsewhereReloadPending = true
		if err := mailElsewhereSetErrors("lookup key", sr.NotCreated, sr.NotDestroyed); err != nil {
			return resp, err
		}
		resp.KeysAdded = len(create)
		resp.KeysRemoved = len(destroy)
	}
	if mailElsewhereReloadPending {
		args := map[string]any{"create": map[string]any{"reload": map[string]any{"@type": "ReloadLookupStores"}}}
		var sr jmapSetResult
		if err := jmapCall(ctx, "x:Action/set", args, &sr); err != nil {
			return resp, fmt.Errorf("reload lookup stores: %w", err)
		}
		if len(sr.NotCreated) > 0 {
			return resp, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "stalwart refused to reload lookup stores"}
		}
		mailElsewhereReloadPending = false
	}

	// 3. allowRelaying on, now that every listed domain's key is live.
	on := map[string]any{}
	for _, d := range domains {
		if !d.AllowRelaying && routing && want[strings.ToLower(d.Name)] {
			on[d.ID] = map[string]any{"allowRelaying": true}
		}
	}
	if err := mailElsewhereUpdateDomains(ctx, on); err != nil {
		return resp, err
	}
	resp.RelayOn = len(on)
	return resp, nil
}

// mailElsewhereRoutingConfigured reports whether the installer's RCPT refusal
// is in Stalwart's RCPT stage script.
func mailElsewhereRoutingConfigured(ctx context.Context) (bool, error) {
	var gr jmapGetResult
	if err := jmapCall(ctx, "x:MtaStageRcpt/get", map[string]any{"ids": []string{"singleton"}}, &gr); err != nil {
		return false, err
	}
	if len(gr.List) == 0 {
		return false, nil
	}
	var row struct {
		Script json.RawMessage `json:"script"`
	}
	if json.Unmarshal(gr.List[0], &row) != nil || len(row.Script) == 0 {
		return false, nil
	}
	var script any
	if json.Unmarshal(row.Script, &script) != nil {
		return false, nil
	}
	// Compare decoded strings, not the raw JSON, which escapes quotes.
	text := strings.Join(mailElsewhereStrings(script), "\n")
	for _, m := range mailElsewhereRcptMarkers {
		if !strings.Contains(text, m) {
			return false, nil
		}
	}
	return true, nil
}

func mailElsewhereStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string
		for _, x := range t {
			out = append(out, mailElsewhereStrings(x)...)
		}
		return out
	case []any:
		var out []string
		for _, x := range t {
			out = append(out, mailElsewhereStrings(x)...)
		}
		return out
	}
	return nil
}

func mailElsewhereStalwartDomains(ctx context.Context) ([]mailElsewhereDomainRow, error) {
	var qr jmapQueryResult
	if err := jmapCall(ctx, "x:Domain/query", map[string]any{"limit": mailElsewhereMaxDomains}, &qr); err != nil {
		return nil, err
	}
	if len(qr.IDs) == 0 {
		return nil, nil
	}
	var gr jmapGetResult
	if err := jmapCall(ctx, "x:Domain/get", map[string]any{"ids": qr.IDs, "properties": []string{"name", "allowRelaying"}}, &gr); err != nil {
		return nil, err
	}
	rows := make([]mailElsewhereDomainRow, 0, len(gr.List))
	for _, raw := range gr.List {
		var row mailElsewhereDomainRow
		if json.Unmarshal(raw, &row) != nil || row.ID == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func mailElsewhereKeys(ctx context.Context) ([]mailElsewhereKeyRow, error) {
	var qr jmapQueryResult
	if err := jmapCall(ctx, "x:MemoryLookupKey/query", map[string]any{
		"filter": map[string]any{"namespace": mailElsewhereNamespace},
		"limit":  mailElsewhereMaxDomains,
	}, &qr); err != nil {
		return nil, err
	}
	if len(qr.IDs) == 0 {
		return nil, nil
	}
	var gr jmapGetResult
	if err := jmapCall(ctx, "x:MemoryLookupKey/get", map[string]any{"ids": qr.IDs, "properties": []string{"namespace", "key"}}, &gr); err != nil {
		return nil, err
	}
	rows := make([]mailElsewhereKeyRow, 0, len(gr.List))
	for _, raw := range gr.List {
		var row mailElsewhereKeyRow
		if json.Unmarshal(raw, &row) != nil || row.ID == "" {
			continue
		}
		// The filter is a text match; keep only this exact namespace, so a
		// key of another lookup is never removed.
		if row.Namespace != mailElsewhereNamespace {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func mailElsewhereUpdateDomains(ctx context.Context, update map[string]any) error {
	if len(update) == 0 {
		return nil
	}
	var sr jmapSetResult
	if err := jmapCall(ctx, "x:Domain/set", map[string]any{"update": update}, &sr); err != nil {
		return err
	}
	return mailElsewhereSetErrors("domain", sr.NotUpdated)
}

func mailElsewhereSetErrors(what string, sets ...map[string]json.RawMessage) error {
	for _, set := range sets {
		for id, reason := range set {
			return &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("stalwart refused %s change for %s: %s", what, id, string(reason)),
			}
		}
	}
	return nil
}

func init() {
	Default.Register("mail.elsewhere.sync", mailElsewhereSyncHandler)
}
