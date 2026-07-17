package notifications

import (
	"fmt"
	"strings"
)

// RenderForChannel formats a title + body pair for a given channel kind.
// The dispatcher feeds the stored envelope (which already carries a
// title/body) straight to senders most of the time, but some transports
// want their own formatting (Discord embeds, ntfy plain-text headers,
// email HTML). This is the single place that knows about those quirks.
//
// Unknown (kind, event_kind) combinations fall back to the envelope's
// raw title/body — callers never have to worry about a blank render.
func RenderForChannel(env Envelope, kind string) (title, body string) {
	switch kind {
	case "slack", "discord":
		// Markdown-friendly transports. Bold the title, append deeplink.
		title = env.Title
		body = strings.TrimSpace(env.Body)
		if env.Deeplink != "" {
			body = strings.TrimSpace(body + "\n\n" + deeplinkLine(env.Deeplink, "View in Jabali"))
		}
		return
	case "ntfy":
		// ntfy sticks the title in a header and the body in the request
		// body — plain text only, no markdown.
		title = env.Title
		body = strings.TrimSpace(env.Body)
		if env.Deeplink != "" {
			body = strings.TrimSpace(body + "\n" + env.Deeplink)
		}
		return
	case "telegram":
		// Telegram Bot API plain-text message (no parse_mode → no escaping).
		// The sender joins title + body; append the deeplink as a bare URL.
		title = env.Title
		body = strings.TrimSpace(env.Body)
		if env.Deeplink != "" {
			body = strings.TrimSpace(body + "\n\n" + env.Deeplink)
		}
		return
	case "webpush":
		// Push payloads are size-limited (~4KB). Truncate aggressively.
		title = truncate(env.Title, 100)
		body = truncate(env.Body, 300)
		return
	case "email":
		title = env.Title
		// Minimal HTML wrapper. Admin-configured channel, not public —
		// no sanitisation needed beyond the html.EscapeString the
		// email sender applies before template substitution.
		body = fmt.Sprintf(
			"<p>%s</p><p>Severity: %s</p>%s",
			strings.ReplaceAll(env.Body, "\n", "<br>"),
			env.Severity,
			deeplinkHTML(env.Deeplink),
		)
		return
	case "webhook":
		// Generic webhook gets the raw envelope in JSON; this path only
		// runs if a caller asks for the rendered form. Senders should
		// marshal the envelope directly instead.
		title = env.Title
		body = env.Body
		return
	default:
		title = env.Title
		body = env.Body
		return
	}
}

func deeplinkLine(url, label string) string {
	if url == "" {
		return ""
	}
	return fmt.Sprintf("<%s|%s>", url, label)
}

func deeplinkHTML(url string) string {
	if url == "" {
		return ""
	}
	return fmt.Sprintf(`<p><a href=%q>Open in Jabali</a></p>`, url)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
