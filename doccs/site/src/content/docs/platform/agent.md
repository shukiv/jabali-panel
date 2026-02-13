---
title: Privileged Agent
description: Root-level automation for system tasks.
sidebar:
  order: 2
---

The jabali-agent daemon runs as root and executes system-level tasks that cannot be safely handled by the web app alone.

## Responsibilities

- System user creation and deletion
- Vhost and PHP-FPM pool configuration
- SSL certificate issuance and renewal
- DNS zone updates
- Mail domain and mailbox operations
- Backup and restore workflows

## Why it exists

- Ensures privileged operations are centralized and auditable
- Reduces direct root access requirements in the UI
- Allows consistent, repeatable system changes

## Communication

- The panel communicates with the agent over a local Unix socket at /var/run/jabali/agent.sock.
