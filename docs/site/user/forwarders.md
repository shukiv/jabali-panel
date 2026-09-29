# Forwarders

`/jabali-panel/mail/forwarders`. Forward mail addressed to one address to one or more destinations.

## Forwarder vs. mailbox

A **forwarder** does not have storage. Mail addressed to a forwarder is immediately relayed to the configured destinations and the original is discarded. The forwarder address does not appear in any IMAP mailbox.

A **mailbox** has storage. Mail is stored, accessible via IMAP / webmail / JMAP.

You can have a forwarder and a mailbox on the same local part: incoming mail is both stored in the mailbox and forwarded to the destinations. Use this pattern to deliver a personal copy plus a workflow CC (CRM, ticketing).

## Adding a forwarder

Click **Add forwarder**, supply:

- Source address (local part plus domain).
- One or more destination addresses, separated by newline or comma.

On save, the agent creates the forwarder in Stalwart's routing table.

The source address cannot be `postmaster@<domain>`: that address belongs to the server administrator. See [The postmaster address](./email.md#the-postmaster-address).

## Loop prevention

The panel refuses to save a forwarder whose destination is itself, or whose destination forms a known loop with another forwarder on the same panel. External loops (your forwarder sends to an external address that forwards back to you) are caught by Stalwart's loop detection at delivery time — the resulting bounce explains the loop.

## SPF and DMARC implications

Forwarding rewrites the envelope sender to a `SRS` (Sender Rewriting Scheme) address so that the destination's SPF check passes. DMARC alignment is preserved where possible; in some configurations DMARC `quarantine` policies on the original sender may still cause downstream filtering. Test with a small set of recipients first when migrating a high-volume address.

## Rate considerations

Forwarders count against the per-mailbox-equivalent outbound throttle (see admin [Mail Throttles](../admin/mail-throttles.md)). A forwarder relaying thousands of messages per hour can trigger the throttle; contact the operator if you need a higher limit for a legitimate flow.

## Removing a forwarder

Per-row **Delete**. Active deliveries already in the queue complete normally; new deliveries to the source address bounce with `550 5.1.1 User unknown` if no mailbox exists for the local part either.
