---
title: DNS and Mail Deliverability
---

This guide helps you ship reliable DNS and email delivery.

## DNS basics

- Ensure the panel hostname resolves to the server IP.
- If hosting DNS, add glue records at the registrar.
- Keep zone templates consistent for new domains.

## Required mail DNS records

- **PTR**: reverse DNS must match your mail hostname.
- **SPF**: authorize your server IP.
- **DKIM**: enable signing for outgoing mail.
- **DMARC**: enforce policy and reporting.

## Checklist

1) PTR set and matches `mail.your-domain`.
2) SPF includes your server IP.
3) DKIM published and active in your MTA.
4) DMARC policy set (start with `p=none` then tighten).

## Testing deliverability

- Send a test email to a mailbox at a major provider.
- Review headers for SPF/DKIM/DMARC pass.
- Use log files to confirm message flow.

## Logs

- Postfix: `/var/log/mail.log` (path may vary by distro)
- Rspamd: `/var/log/rspamd/rspamd.log`
