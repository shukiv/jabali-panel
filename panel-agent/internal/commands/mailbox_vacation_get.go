package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailbox.vacation.get reads the mailbox account's NATIVE Stalwart
// VacationResponse (RFC 8621 singleton) and returns it to the panel. It exists
// so the panel can ADOPT a vacation reply that was set outside jabali — the
// Bulwark webmail's Vacation Responder writes the native VacationResponse
// directly via the user's JMAP session, not through jabali's autoresponder API
// (GH #1795 follow-up).
//
// Why adoption is needed: jabali composes forwards + the autoresponder into ONE
// active standard SieveScript (jabali-managed). Activating that script flips the
// native VacationResponse's isEnabled to false (verified on Stalwart 0.16.15 —
// the two are mutually exclusive), so a webmail-set vacation stops firing once a
// forward exists. jabali reads the native value here BEFORE it applies the
// composite, then persists it into email_autoresponders so it becomes part of
// the durable composite and the panel Auto Reply UI shows it too.
//
// Read-only. If the account is not registered with Stalwart yet, there is
// nothing to adopt, so it returns is_enabled=false rather than provisioning.
type mailboxVacationGetParams struct {
	MailboxEmail string `json:"mailbox_email"`
}

type mailboxVacationGetResponse struct {
	IsEnabled bool    `json:"is_enabled"`
	FromDate  *string `json:"from_date"`
	ToDate    *string `json:"to_date"`
	Subject   *string `json:"subject"`
	TextBody  *string `json:"text_body"`
	HTMLBody  *string `json:"html_body"`
}

func mailboxVacationGetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailboxVacationGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.MailboxEmail); err != nil {
		return nil, err
	}

	acctID, err := accountIDByEmail(ctx, p.MailboxEmail)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account: %v", err)}
	}
	if acctID == "" {
		// Not registered yet → nothing to adopt.
		return mailboxVacationGetResponse{IsEnabled: false}, nil
	}

	var result jmapGetResult
	if err := jmapCall(ctx, "VacationResponse/get", map[string]any{"accountId": acctID, "ids": []string{"singleton"}}, &result); err != nil {
		return nil, err
	}
	if len(result.List) == 0 {
		return mailboxVacationGetResponse{IsEnabled: false}, nil
	}
	var vr struct {
		IsEnabled bool    `json:"isEnabled"`
		FromDate  *string `json:"fromDate"`
		ToDate    *string `json:"toDate"`
		Subject   *string `json:"subject"`
		TextBody  *string `json:"textBody"`
		HTMLBody  *string `json:"htmlBody"`
	}
	if err := json.Unmarshal(result.List[0], &vr); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("parse VacationResponse: %v", err)}
	}
	return mailboxVacationGetResponse{
		IsEnabled: vr.IsEnabled,
		FromDate:  vr.FromDate,
		ToDate:    vr.ToDate,
		Subject:   vr.Subject,
		TextBody:  vr.TextBody,
		HTMLBody:  vr.HTMLBody,
	}, nil
}

func init() {
	Default.Register("mailbox.vacation.get", mailboxVacationGetHandler)
}
