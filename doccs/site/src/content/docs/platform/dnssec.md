---
title: DNSSEC
description: DNSSEC management and DS records.
sidebar:
  order: 5
---

DNSSEC adds cryptographic signatures to DNS records for stronger integrity guarantees.

## What it does

- Signs DNS zones with cryptographic keys
- Lets resolvers validate responses
- Reduces risk of DNS spoofing

## Setup flow

1. Enable DNSSEC for a domain
2. Copy the DS record from the panel
3. Add the DS record at the registrar
4. Wait for propagation

## Management tips

- Rotate keys only when necessary
- Keep DS records in sync with the panel
- Monitor for DNSSEC validation errors
