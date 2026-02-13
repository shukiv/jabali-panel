---
title: Platform Stack
description: Core services and runtime components.
sidebar:
  order: 1
---

Jabali uses a modern Laravel application for the control plane and a privileged agent for system operations.

## Application stack

- Laravel 12 with Filament v5 and Livewire v4
- Tailwind-based UI
- SQLite for panel metadata

## Service stack

- Nginx and PHP-FPM
- MariaDB for user databases
- Postfix, Dovecot, and Rspamd for mail
- BIND9 for DNS
- Redis for cache and queue

## Runtime targets

- Debian 12 or 13
- PHP 8.4
- Node 20 and Vite for assets
