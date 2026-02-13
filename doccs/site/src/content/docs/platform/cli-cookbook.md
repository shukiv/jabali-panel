---
title: CLI Cookbook
---

This page provides practical CLI examples. For the full command list, see the CLI reference.

## Backups

Create a backup for a user:

```
jabali backup create <user>
```

Restore a backup to a user:

```
jabali backup restore <path> --user=<user>
```

## cPanel migration

Analyze a cPanel archive:

```
jabali cpanel analyze <file>
```

Restore a cPanel archive into a user:

```
jabali cpanel restore <file> <user>
```

## DNS

List DNS zones:

```
jabali dns zones list
```

Create a new DNS zone:

```
jabali dns zones create example.com --template=default
```

## SSL

Issue a certificate:

```
jabali ssl issue example.com
```

Renew all certificates:

```
jabali ssl renew --all
```

## Users

Create a user:

```
jabali users create user@example.com --plan=starter
```

Suspend a user:

```
jabali users suspend user@example.com
```

## Help and discovery

List all commands:

```
jabali --help
```

Get help for a specific command:

```
jabali <command> --help
```
