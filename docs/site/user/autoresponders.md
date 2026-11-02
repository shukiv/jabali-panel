# Autoresponders

Mail Domains → a domain → Accounts → a mailbox. Vacation-style automatic replies per mailbox.

## Per-autoresponder fields

- **Mailbox** — the address the autoresponder is attached to.
- **Subject template** — defaults to `Re: ${original-subject}`. Variables: `${original-subject}`, `${sender}`, `${recipient}`, `${date}`.
- **Body** — plain text or HTML.
- **Start date** and **end date** — outside this window the autoresponder is silent.
- **Reply once per sender per** — `day` (default), `hour`, or `never` (always reply). Throttles avoid ping-pong with newsletters.

## Activation

Save with **Active = on**. Stalwart begins responding at the start date and stops at the end date (UTC).

To temporarily pause without losing the configuration, toggle **Active = off**; the entry is retained and you may re-enable later.

## What an autoresponder does *not* reply to

- Bulk mail (mailing lists), detected by `List-Id`, `Precedence: bulk`, or `List-Unsubscribe`.
- Bounces (DSN messages from MAILER-DAEMON).
- Auto-generated mail (`Auto-Submitted: auto-generated` or `auto-replied`).
- Mail without a `From` address.
- Mail from a sender already replied to within the throttle window.

These exclusions follow RFC 3834 to prevent autoresponse storms.

## Spam interactions

Mail scored as spam by Stalwart does not trigger the autoresponder. The threshold is the same one used to deliver into the `Junk` folder.

## Multiple autoresponders per mailbox

Not currently supported in the UI — one active autoresponder per mailbox at a time. Edit the current one or delete it before creating a new one with different content.

## Headers added by the response

- `Auto-Submitted: auto-replied`
- `Precedence: bulk`
- `X-Auto-Response-Suppress: All`
- The original `Message-ID` is referenced in `In-Reply-To`.
