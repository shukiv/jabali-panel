package stalwartadmin

import (
	"context"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailspam"
)

// SpamScores writes the panel's spam score thresholds (GH #2017) into
// Stalwart's SpamSettings singleton. It is the reconciler's
// SpamScoresApplier.
//
// Stalwart keeps scoring with the thresholds it loaded until a settings
// reload: on the .60 test box (0.16.24) a message scoring 5.5 still went to
// Junk after scoreSpam was set to 60, and went to the Inbox once a reload
// followed. So every write is followed by a reload.
type SpamScores struct {
	Client *Client
}

// Apply makes Stalwart's thresholds equal want. It reads the singleton and
// writes the three score fields only when they differ, leaving enable, the
// trust switches and the rules URL to install.sh. reload asks for a settings
// reload even when nothing differs: the caller's retry after a write whose
// reload failed. changed reports a write, also when the reload after it
// failed (the thresholds are stored but not live yet).
func (s SpamScores) Apply(ctx context.Context, want mailspam.Scores, reload bool) (changed bool, err error) {
	if err := want.Validate(); err != nil {
		return false, err
	}
	raw, err := s.Client.Get(ctx, mailspam.StalwartType, mailspam.StalwartID)
	if err != nil {
		return false, err
	}
	have, err := mailspam.FromStalwart(raw)
	if err != nil {
		return false, err
	}
	if !have.Equal(want) {
		if err := s.Client.Update(ctx, mailspam.StalwartType, mailspam.StalwartID, want.Payload()); err != nil {
			return false, err
		}
		changed = true
	}
	if changed || reload {
		if err := s.Client.ReloadSettings(ctx); err != nil {
			return changed, err
		}
	}
	return changed, nil
}
