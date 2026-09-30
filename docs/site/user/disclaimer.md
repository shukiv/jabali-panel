# Disclaimer

A per-domain disclaimer that the mail server appends to mail sent from the domain.

## Where to set it

**Mail → Mail Domains → your domain → Settings → Disclaimer**
(`/jabali-panel/mail-domains/<domain id>/settings`). It used to be a separate
Disclaimer tab; old links to that tab open Settings.

The Settings tab shows the disclaimer only when the domain has email enabled.

- **Enable Disclaimer**: on or off.
- **Disclaimer Text**: the text to append. It is required while the disclaimer is on.

Click **Save** to apply. The change takes effect for the next message; nothing
has to be restarted.

The same setting is available from the command line:

```
jabali domain disclaimer show  <domain-name-or-id>
jabali domain disclaimer set   <domain-name-or-id> --text "..."   # or --file <path>
jabali domain disclaimer clear <domain-name-or-id>
```

## What gets added

The text is plain text. The mail server adds it to every readable part of the
message, and leaves attachments alone:

- **Plain-text part**: the text goes at the end, after a `-- ` signature separator line.
- **HTML part**: the text goes just before `</body>`, after a horizontal rule. It is
  HTML-escaped, so markup such as `<b>` or a link shows up as literal text.
  You cannot put HTML in a disclaimer.

A multipart message (plain text and HTML together) gets the disclaimer in both parts.

The disclaimer is chosen by the sender's domain. It is added when the message's
envelope sender is an address on a domain that has email and the disclaimer
turned on.

## Delivery is never blocked

- The mail server re-signs DKIM after the disclaimer is added, so signatures stay valid.
- If the disclaimer cannot be added (the disclaimer service fails, or the rewritten
  message would be larger than 48 MB), the message is delivered **without** the
  disclaimer. It is never bounced or delayed because of the disclaimer.

## Common uses

- A legal notice: a confidentiality clause and what to do if the mail reached the wrong person.
- A required disclosure for a regulated business.
