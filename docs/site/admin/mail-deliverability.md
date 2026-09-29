# Mail Deliverability

`/jabali-admin/mail/deliverability`. A score from 0 to 100 for how well the server's outbound mail is received, built from blocklist checks and from the reports other mail servers send back. 100 is clean.

## The score

Four signals are counted over the last 7 days. Each one can take up to 25 points off a clean 100:

| Signal | What is counted | Points off |
|---|---|---|
| `rbl` | Blocklists (Spamhaus, SpamCop, Barracuda, SURBL) that list the server's public IPv4 address | 25 per listing |
| `dmarc_dkim_failures` | Records in received DMARC aggregate reports whose DKIM result was not a pass | 5 per record |
| `tlsrpt_failures` | TLS sessions that failed, as received SMTP TLS reports count them | 10 per failed session |
| `abuse_reports` | Abuse-feedback (ARF) reports from receivers about mail the server sent | 1 per report |

A score of 90 or more is **OK**, 60 to 89 is **Warning**, and below 60 is **Critical**. The page refreshes every minute.

## Per domain

A domain's edit page shows the same score for that domain alone: the DMARC, TLS and abuse signals counted only for that domain. The blocklist signal is left out there, because every domain sends from the same IP address.

## Where the reports come from

A receiver sends a report only to the address the domain's DNS asks for. For each mail domain the panel publishes:

- **DMARC reports** — `rua=mailto:postmaster@<domain>` in the domain's `_dmarc` record. If you edit a domain's `_dmarc` record, the panel treats it as yours and does not rewrite it.
- **TLS reports** — `_smtp._tls TXT "v=TLSRPTv1; rua=mailto:postmaster@<domain>"`.
- **Abuse reports** — receivers send them to the addresses registered with their feedback-loop programs.

Stalwart recognizes these reports in the mail it receives and keeps them for about 30 days. Every 5 minutes the panel copies the new ones into its database, where the score counts them. The panel keeps them for 90 days.

Big receivers send reports once a day, so a domain's first report can take 24 to 48 hours.

## The postmaster mailbox

Every mail domain accepts mail to `postmaster@<domain>`, as RFC 5321 requires. A domain whose owner has made a postmaster mailbox, alias or group gets that mail itself. For every other domain, mail to `postmaster@` goes to the postmaster mailbox on the panel's own domain, `postmaster@<panel hostname>`. The panel creates that mailbox when it starts, if the panel domain has email and no postmaster yet. It is listed with the panel domain's mailboxes and opens in webmail like any other.

Receivers' reports are delivered there too: about one message per receiver, per domain, per day. Report mail that Stalwart files as spam is deleted from Junk after 30 days; delete the rest when you no longer need it. The panel has already read every report for this page, so deleting the messages loses nothing here. The mailbox has a 1 GiB quota.

## Notifications

Each time a check finds new reports, the panel sends one notification per report type: `mail.dmarc.report_received`, `mail.tls.report_received` or `mail.feedback.received`. The DMARC and TLS notifications name the domains; all three link to this page. A TLS notification is sent only when a report counts failed sessions. See [Notifications — Events](./notifications-events.md).

Anyone can send a report to a postmaster address, so the panel treats a report's contents as untrusted. It cuts each value to fit its column, drops control characters, keeps only valid IP addresses, and sends one notification per check no matter how many reports arrive.

## Why this page exists

A domain can have correct SPF, DKIM and DMARC records and still have its mail rejected: the IP address is blocklisted, a certificate expired, or recipients mark the mail as spam. The reports receivers send back are how the operator finds out. This page shows them in one place.

## CLI

```bash
jabali mail deliverability
jabali mail deliverability --domain <domain>
jabali domain email-dkim-rotate <domain>
```

`jabali mail deliverability` prints the same score as the page. `jabali domain email-dkim-rotate` makes a new DKIM key for a domain.
