# Notifications — Channels

`/jabali-admin/notifications/channels`. The server-wide places where notifications are delivered (M14). Every enabled channel receives every event that is on; see [Routing](./notifications-routing.md).

## Channel kinds

| Kind | Configuration |
|---|---|
| **Email** | To and From addresses. Mail goes out through the panel's own Stalwart by default. You can use an external SMTP server instead: set its host, port, username, password and TLS mode (`starttls`, `tls` or `none`). |
| **Slack** | Incoming-webhook URL. |
| **Discord** | Webhook URL. |
| **Telegram** | Bot token and chat ID. |
| **ntfy** | Topic URL, on ntfy.sh or a self-hosted ntfy server. |
| **Webhook** | URL, with an optional bearer token and HMAC secret. |
| **SMS** | Gateway URL and a phone number in E.164 format. |
| **Web Push** | Browser push. Turn it on for your browser on the **Web Push** tab. |

The in-app bell is not a channel. Every event that is on reaches the bell.

## Testing a channel

**Test** on a channel's row sends that channel a test message. The edit drawer has a **Send test** button too. A received test proves that the credentials and the connection work.

## Enabling and disabling

A disabled channel receives nothing. A channel that fails 3 times in a row is disabled automatically, and the panel fires `notifications.channel.auto_disabled`.

## Tenant channels

When an admin turns on tenant notifications, tenants can create their own channels. Those channels appear in this list with the **Owner** column set to Tenant, and an admin can disable or delete them. A tenant channel receives only events about that tenant that the tenant routed to it. See [Notifications](../notifications.md).

## CLI

```bash
jabali notification channels list
jabali notification channels create --name ops-ntfy --kind ntfy --config '{"url":"https://ntfy.sh/my-topic"}'
jabali notification channels test <id>
jabali notification channels delete <id> --force
```

## Architecture

Producers add each event to the Redis stream `jabali:notifications:queue`. The dispatcher inside the panel process reads the stream and calls the sender for each target channel. An event that still fails after 5 tries moves to `jabali:notifications:dlq`, shown on the **Dead Letter** tab. Each sender is one Go file under `panel-api/internal/notifications/senders/`.

ADR-0056 covers the data model; ADRs 0057–0059 cover the sender interface, Web Push, and the bell dropdown.
