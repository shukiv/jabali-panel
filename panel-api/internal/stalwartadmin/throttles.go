package stalwartadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailthrottle"
)

// Throttles writes the panel's outbound throttles (M47 Wave 3) into
// Stalwart as MtaOutboundThrottle objects. It is the reconciler's
// ThrottleApplier and the throttle API handler's delete path.
//
// Apply is idempotent: with a known id it reads the object first and writes
// only when it differs, so the reconciler can call it on every tick. An id
// Stalwart no longer has is replaced by a new object. Delete treats an id
// Stalwart no longer has as done. Every write is followed by a settings
// reload.
type Throttles struct {
	Client *Client
}

// Apply makes one throttle window exist in Stalwart and returns the id the
// window owns now.
func (t Throttles) Apply(ctx context.Context, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error) {
	if err := req.Validate(); err != nil {
		return mailthrottle.ApplyResult{}, err
	}
	want := mailthrottle.Payload(req)

	if req.StalwartID != "" {
		raw, err := t.Client.Get(ctx, mailthrottle.StalwartType, req.StalwartID)
		switch {
		case errors.Is(err, ErrNotFound):
			// Gone from Stalwart (deleted by hand, or a restore): create a new one below.
		case err != nil:
			return mailthrottle.ApplyResult{}, err
		default:
			var cur mailthrottle.Throttle
			if err := json.Unmarshal(raw, &cur); err != nil {
				return mailthrottle.ApplyResult{}, fmt.Errorf("stalwartadmin: parse throttle %s: %w", req.StalwartID, err)
			}
			if cur.Equal(want) {
				return mailthrottle.ApplyResult{StalwartID: req.StalwartID, Changed: false}, nil
			}
			err := t.Client.Update(ctx, mailthrottle.StalwartType, req.StalwartID, want)
			if err == nil {
				if err := t.Client.ReloadSettings(ctx); err != nil {
					return mailthrottle.ApplyResult{}, err
				}
				return mailthrottle.ApplyResult{StalwartID: req.StalwartID, Changed: true}, nil
			}
			if !errors.Is(err, ErrNotFound) {
				return mailthrottle.ApplyResult{}, err
			}
		}
	}

	id, err := t.Client.Create(ctx, mailthrottle.StalwartType, want)
	if err != nil {
		return mailthrottle.ApplyResult{}, err
	}
	if err := t.Client.ReloadSettings(ctx); err != nil {
		return mailthrottle.ApplyResult{}, err
	}
	return mailthrottle.ApplyResult{StalwartID: id, Changed: true}, nil
}

// Delete removes one throttle. An id Stalwart no longer has counts as
// removed.
func (t Throttles) Delete(ctx context.Context, stalwartID string) error {
	if !mailthrottle.ValidStalwartID(stalwartID) {
		return fmt.Errorf("stalwartadmin: invalid throttle id %q", stalwartID)
	}
	err := t.Client.Delete(ctx, mailthrottle.StalwartType, stalwartID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return t.Client.ReloadSettings(ctx)
}

// List returns every MtaOutboundThrottle in Stalwart (id and description),
// the panel's and anyone else's. An id that could not be passed back to
// Delete is dropped.
func (t Throttles) List(ctx context.Context) ([]mailthrottle.ListItem, error) {
	objs, err := t.Client.Query(ctx, mailthrottle.StalwartType, nil, []string{"description"})
	if err != nil {
		return nil, err
	}
	items := make([]mailthrottle.ListItem, 0, len(objs))
	for _, raw := range objs {
		var o struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, fmt.Errorf("stalwartadmin: parse throttle list: %w", err)
		}
		if !mailthrottle.ValidStalwartID(o.ID) {
			continue
		}
		items = append(items, mailthrottle.ListItem{StalwartID: o.ID, Description: o.Description})
	}
	return items, nil
}
