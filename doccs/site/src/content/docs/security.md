---
title: Security Hardening
---

Use this page as a baseline security checklist.

## Core practices

- Use SSH keys and disable password auth where possible.
- Enable 2FA for all admin accounts.
- Keep the OS up to date with regular security patches.

## Firewall and intrusion prevention

- Validate firewall rules match your required ports.
- Enable Fail2ban and review ban logs.

## Mail security

- Confirm PTR, SPF, DKIM, and DMARC records.
- Review mail logs and reject spikes.

## SSL and TLS

- Ensure certificates renew automatically.
- Use strong TLS settings and disable obsolete protocols.

## Least privilege

- Limit admin access to known IPs when possible.
- Remove unused accounts and API tokens.

## Demo mode

- Demo environments should be isolated and read-only.
- Never reuse demo credentials in production.
