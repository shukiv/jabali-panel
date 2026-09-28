package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
)

// MailThrottles pushes outbound mail throttles into Stalwart through the
// agent's mail.throttle.* verbs. The panel cannot call Stalwart's admin API
// itself (its credential is not readable by the panel user, JAB-357). The
// wire types live in internal/mailthrottle, which the agent's handlers
// import too, so the two sides cannot drift.
type MailThrottles struct {
	Agent AgentInterface
}

// Apply makes one throttle window exist in Stalwart and returns the id the
// window owns now (a new one when Stalwart had lost req.StalwartID).
func (m MailThrottles) Apply(ctx context.Context, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error) {
	raw, err := m.Agent.Call(ctx, mailthrottle.VerbApply, req)
	if err != nil {
		return mailthrottle.ApplyResult{}, err
	}
	var res mailthrottle.ApplyResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return mailthrottle.ApplyResult{}, fmt.Errorf("%s: parse result: %w", mailthrottle.VerbApply, err)
	}
	if !mailthrottle.ValidStalwartID(res.StalwartID) {
		return mailthrottle.ApplyResult{}, fmt.Errorf("%s: agent returned stalwart_id %q", mailthrottle.VerbApply, res.StalwartID)
	}
	return res, nil
}

// Delete removes one throttle from Stalwart. An id Stalwart no longer has
// counts as removed.
func (m MailThrottles) Delete(ctx context.Context, stalwartID string) error {
	_, err := m.Agent.Call(ctx, mailthrottle.VerbDelete, mailthrottle.DeleteRequest{StalwartID: stalwartID})
	return err
}

// List returns every MtaOutboundThrottle in Stalwart, the panel's and
// anyone else's.
func (m MailThrottles) List(ctx context.Context) ([]mailthrottle.ListItem, error) {
	raw, err := m.Agent.Call(ctx, mailthrottle.VerbList, nil)
	if err != nil {
		return nil, err
	}
	var res mailthrottle.ListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: parse result: %w", mailthrottle.VerbList, err)
	}
	return res.Throttles, nil
}
