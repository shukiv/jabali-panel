# Email Logs (User)

Mail Domains → a domain → Logs (`/jabali-panel/mail-domains/<domain id>/logs`). Live tail of inbound and outbound mail for that domain.

## Columns

- Timestamp
- Direction (inbound / outbound)
- From, To
- Subject (when full-text indexing is enabled by the operator; otherwise absent)
- Status (`accepted`, `delivered`, `deferred`, `bounced`, `quarantined`)
- Size

## Filters

- Time range
- Direction
- Status
- Specific domain (when you own multiple)
- Specific mailbox

## Per-row drill-in

Click a row to view:

- The SMTP transcript with response codes.
- DKIM / SPF / DMARC verdicts for inbound mail.
- The delivery destination(s) for outbound mail.

You can see the **metadata** for every message in your domains. You cannot see message bodies — those live in mailbox storage and are accessible only via authenticated IMAP / webmail by the owning mailbox.

## Quarantined messages

If a message is quarantined by Stalwart's spam filter or by the async post-delivery YARA scan, the row shows status `quarantined`. The quarantined message is **not** delivered; the operator (admin) can release or delete it. Tenants do not have release authority from this page.

## Retention

Logs default to 30 days. If your operator has configured a different retention window, the page header notes it.

## Use cases

- Confirm whether an outbound message left the panel and what the recipient's MTA said.
- Diagnose "where did my mail go?" reports from senders.
- Audit a delivery you expected to receive that did not show up in the mailbox.

## Why no message body view

Message body access is gated by mailbox authentication. The logs page is admin-restricted by default for body view (and even then is admin-only — never tenant-visible) because mailbox content is the sensitive surface; logs metadata is sufficient for delivery troubleshooting.
