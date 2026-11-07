// Package notifications hosts the M14 event fan-out pipeline.
//
// The public surface is intentionally small:
//
//   - Envelope describes what the caller wants delivered.
//   - Dispatcher owns the Redis Streams consumer + reclaim loops;
//     callers never talk to Redis directly.
//   - ChannelSender is the plug point every concrete delivery transport
//     implements (email, Slack, ntfy, webhook, webpush; added in Step 3).
//
// Restart-safety comes from Redis Streams (ADR-0056): enqueuing an
// envelope is an XADD, so an envelope survives panel-api restart.
package notifications

import (
	"errors"
	"strings"
)

// Envelope is the in-memory shape callers hand to the dispatcher.
// StreamMap converts it to the flat map[string]any that XADD expects;
// envelopeFromStream reverses that in the consumer. Keep both in sync.
//
// ChannelIDs is the optional target subset: empty slice means "every
// enabled channel whose kind routes this event". Non-empty restricts
// delivery to that exact set — admin flows use this for "send a test
// notification to this channel only".
type Envelope struct {
	EventKind  string
	Severity   string
	Title      string
	Body       string
	Deeplink   string
	ChannelIDs []string
	// UserID is the admin the bell + per-user push targets. Empty for
	// broadcast events (disk-full, cert failure). The dispatcher writes
	// one notification_history row per target; if UserID is empty and
	// the kind resolves to web-push, the dispatcher iterates every
	// enrolled subscription.
	UserID string

	// StreamID is set by the dispatcher after XREADGROUP to the entry's
	// Redis Streams id. It is NOT serialised by StreamMap — XADD already
	// assigns the id, so producers leave this empty. Consumers use it to
	// stamp notification_history.envelope_id, which lets the inbox UI
	// correlate history rows with DLQ stream entries by orig_id.
	StreamID string
}

// Stream field names — centralised so producer + consumer stay locked
// together. Any rename here is a breaking wire change that requires
// draining the queue first.
const (
	fieldEventKind  = "event_kind"
	fieldSeverity   = "severity"
	fieldTitle      = "title"
	fieldBody       = "body"
	fieldDeeplink   = "deeplink"
	fieldChannelIDs = "channel_ids"
	fieldUserID     = "user_id"
)

// StreamMap flattens an Envelope into the XADD field map. Missing
// fields stay absent (Redis doesn't need empty placeholders). Arrays
// are comma-joined — go-redis does not preserve structured types in
// stream values so we pick a stable on-wire format the consumer can
// split on. A deeplink that is not a panel path (see PanelPath) is left
// out.
func (e Envelope) StreamMap() map[string]any {
	m := map[string]any{
		fieldEventKind: e.EventKind,
		fieldSeverity:  e.Severity,
		fieldTitle:     e.Title,
		fieldBody:      e.Body,
	}
	if link := PanelPath(e.Deeplink); link != "" {
		m[fieldDeeplink] = link
	}
	if len(e.ChannelIDs) > 0 {
		m[fieldChannelIDs] = strings.Join(e.ChannelIDs, ",")
	}
	if e.UserID != "" {
		m[fieldUserID] = e.UserID
	}
	return m
}

// envelopeFromStream rebuilds an Envelope from a stream entry's
// Values. Missing required fields (event_kind / severity / title)
// return an error — the dispatcher XACKs + XADDs these to the DLQ
// rather than re-looping on a malformed entry forever.
func envelopeFromStream(values map[string]any) (Envelope, error) {
	get := func(k string) string {
		v, ok := values[k]
		if !ok {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	env := Envelope{
		EventKind: get(fieldEventKind),
		Severity:  get(fieldSeverity),
		Title:     get(fieldTitle),
		Body:      get(fieldBody),
		// Re-checked here too: entries queued before this check, or added
		// to the stream by anything but Publish, reach the senders this way.
		Deeplink: PanelPath(get(fieldDeeplink)),
		UserID:   get(fieldUserID),
	}
	if ids := get(fieldChannelIDs); ids != "" {
		env.ChannelIDs = strings.Split(ids, ",")
	}
	if env.EventKind == "" {
		return env, errors.New("envelope: missing event_kind")
	}
	if env.Severity == "" {
		return env, errors.New("envelope: missing severity")
	}
	if env.Title == "" {
		return env, errors.New("envelope: missing title")
	}
	return env, nil
}
