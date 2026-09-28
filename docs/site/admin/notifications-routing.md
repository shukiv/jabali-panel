# Notifications — Routing

The panel has no routing rules. Where an event goes follows from three settings:

- whether the event is on;
- which server-wide channels are enabled;
- for an event about one tenant, which of their own channels the tenant routed it to.

## Where an event goes

When a producer fires an event:

1. If the event is off on [Events](./notifications-events.md), the panel drops it.
2. The panel writes the event to the in-app bell. An event about one user goes to that user's bell. A server event goes to every admin's bell.
3. The event goes to every enabled server-wide channel on [Channels](./notifications-channels.md). A channel cannot filter events by name or by severity. To keep an event off every channel, turn the event off.
4. An event about one tenant also goes to each of that tenant's own channels that they routed the event to. This happens only when an admin has turned on tenant notifications. See [Notifications](../notifications.md) for tenant channels.

## Failed deliveries

The panel retries a failed delivery. After 5 tries, the event moves to the **Dead Letter** tab. `jabali notification dlq replay` sends it again, and `jabali notification dlq drop` discards it.

A channel that fails 3 times in a row is turned off, and the panel fires `notifications.channel.auto_disabled`.

## Testing a channel

Click **Test** on a channel's row to send that channel a test message. To send one message to every enabled channel:

```bash
jabali notification broadcast --title "Test" --body "Checking every channel"
```
