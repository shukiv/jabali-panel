---
title: Migrations (cPanel and WHM)
---

Jabali supports imports from common panels with detailed logs and staged steps.

## cPanel migration

1) Upload the cPanel archive from **Admin → Migration** or **User → cPanel Migration**.
2) Run **Analyze** to validate the archive contents.
3) Review findings (domains, databases, mailboxes, forwarders, SSL).
4) Run **Restore** and monitor the live log output.

## WHM migration

1) Open **Admin → WHM Migration**.
2) Enter WHM credentials and select accounts.
3) Start the migration and track progress per account.

## CLI examples

Analyze a cPanel archive:

```
jabali cpanel analyze <file>
```

Restore a cPanel archive:

```
jabali cpanel restore <file> <user>
```

## Notes

- Some migration tabs appear only after a successful **Analyze**.
- Provide a sample cPanel backup to capture all tab screenshots.
