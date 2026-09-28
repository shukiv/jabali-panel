# Email Logs (Admin)

Cross-domain view of mail flow through Stalwart. Reachable from [Mail Deliverability](./mail-deliverability.md) → **All Logs**, and from Server Status → Mail card.

## Columns

- Timestamp
- Direction (inbound / outbound)
- From, To
- Status (`accepted`, `delivered`, `deferred`, `bounced`, `quarantined`)
- Size (bytes)
- DKIM / SPF / DMARC verdicts
- Server response code (SMTP)

## Filters

- Time range (last hour, 24 h, 7 d, custom)
- Direction
- Status
- Domain (sender or recipient)
- Free-text on subject (only available when full-text indexing is enabled at install time; default off for privacy)

## What is shown vs. what is stored

The panel surfaces structured metadata only. Message bodies are not shown in the admin logs view; they live in mailbox storage and are accessible only via authenticated IMAP / JMAP / webmail by the owning mailbox.

## Per-row drill-in

Click a row to see:

- The full SMTP transcript (HELO, MAIL FROM, RCPT TO, DATA boundary, response codes).
- DKIM verification details (selector, body hash match, signature validation result).
- SPF lookup chain (DNS queries performed, final verdict).
- DMARC alignment (header-from vs envelope-from, policy applied).
- For inbound: any CrowdSec or AppSec decision that intervened.

## Quarantined messages

Stalwart's quarantine is visible in a separate tab. The admin may release, delete, or download the EML for forensic review. Async post-delivery YARA hits (M33.2) appear here with the matched rule and severity.

## Retention

Logs default to 30 days. Configurable under Server Settings → Mail → Retention. Older entries are pruned by a daily timer.

## CLI

Not currently exposed. Use `journalctl -u jabali-stalwart -f` for live tail with all the same data in structured form.
