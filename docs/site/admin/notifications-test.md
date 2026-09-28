# Notifications — Test

The panel has no separate test page. You can test notifications in two ways.

## Test one channel

On [Channels](./notifications-channels.md), click **Test** on the channel's row. The panel sends that channel a test message. If the message arrives, the channel's credentials and connection work. If it does not arrive, check the channel's token, URL or chat ID.

From a shell:

```bash
jabali notification channels test <id>
```

## Test every channel

A broadcast sends one message to every enabled channel:

```bash
jabali notification broadcast --title "Test" --body "Checking every channel" --severity info
```

## When to test

- After you add or edit a channel.
- When a notification you expected did not arrive. First check that its event is on under [Events](./notifications-events.md). Then check that the channel is enabled; the panel turns off a channel after 3 failures in a row.
