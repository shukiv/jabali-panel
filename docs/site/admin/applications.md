# Applications (Admin)

`/jabali-admin/applications`. Server-wide controls for the 15-app one-click registry (M10 / M19, see [Applications](../applications.md)).

## Per-app rows

Each app row shows: app id, latest upstream version, pinned version (if set), enabled state, total install count across the server.

## Per-app actions

- **Enable / Disable** — when disabled, users cannot install this app from their own Applications page; existing installs remain.
- **Pin version** — pin to a specific upstream version (use during a known-bad upstream release window). Unpin to track `latest`.
- **Prerequisites** — declare PHP extensions or system packages required by this app; validated against the user's chosen PHP version at install time.
- **Cache refresh** — re-fetch the latest upstream version metadata.
- **List installs** — drill into every install of this app across the server, with owner, domain, install path, version.

## Adding a new app

The registry is code-shipped (`internal/apps/registry.go`). Adding a new app currently requires a code change plus a release:

1. Add a registry entry (id, display name, source URL, install / upgrade / delete callbacks).
2. Add an icon under `panel-ui/src/shells/admin/applications/icons/`.
3. Test against the install pipeline (the same 6 stages every app uses).
4. Ship as part of the next `jabali update`.

Third-party app contributions are welcome via PR.

## Upgrade behavior

When the panel detects a newer upstream version than what is installed for an app, the affected install rows surface an "Upgrade available" badge on the user's [Applications](../user/applications.md) page. Upgrade does not happen automatically (operator policy); the user clicks **Upgrade** to consent and the agent runs the per-app upgrade callback.

## Per-user override

A user's package may further restrict which apps are installable (Package → Allowed applications). This admin page is the server-wide ceiling; package selections are subsets.

## CLI

```bash
jabali app list
jabali app registry                                        # the app types you can install
jabali app install --domain <fqdn> --app-type wordpress --user <id> --wait
jabali app delete <install-id>
```
