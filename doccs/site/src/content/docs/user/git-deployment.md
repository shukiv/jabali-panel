---
title: Git Deployment
description: Git Deployment page details, screenshots, and usage examples.
---

This page provides git deployment features for the jabali panel.

## Screenshot

![Git Deployment Screenshot](/screenshots/user-git-deployment.png)

## What this page does

- This page provides git deployment features for the jabali panel.
- Use git deployment to complete common operational tasks.
- Review this page after configuration changes to confirm results.

## Webhook Security

- Use the provided `Webhook URL` with the `Webhook Secret`.
- Jabali validates `X-Jabali-Signature` (or `X-Hub-Signature-256`) as HMAC-SHA256 over the raw request body.
- Legacy tokenized webhook URLs remain supported for older integrations.

## Typical examples

- Example 1: Use git deployment to complete common operational tasks.
- Example 2: Review this page after configuration changes to confirm results.
