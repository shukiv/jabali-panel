# Jabali Docker App Catalog

This directory is the source of truth for the M48 Docker App Marketplace catalog. Each subdirectory is one installable app; the catalog format is documented in [ADR-0116](../../docs/adr/0116-m48-docker-app-marketplace.md) and validated against [`_schema/app.schema.json`](_schema/app.schema.json) at panel startup.

## Layout

```
install/docker-apps/
├── _schema/
│   └── app.schema.json          JSON-schema for app.yaml (load-time validation)
├── README.md                    you are here
└── <slug>/                      one directory per app; slug = directory name
    ├── app.yaml                 metadata (required)
    ├── compose.yml.tmpl         Go text/template (required)
    └── icon.svg                 SVG icon (required)
```

## Adding an app

1. Create the directory `install/docker-apps/<slug>/` where `<slug>` matches `^[a-z][a-z0-9-]{1,31}$`. The slug is what appears in the admin UI, in the data path (`/var/lib/jabali/docker-apps/<slug>/`), and in the API URL.
2. Write `app.yaml`. The schema is the contract; the loader rejects entries that fail validation. Use one of the existing apps as a starting point.
3. Write `compose.yml.tmpl`. It is a Go `text/template` rendered by the agent. Variables are documented in the [Template variables](#template-variables) section below.
4. Drop in `icon.svg`. Square SVG, viewBox 0 0 32 32 is convention.

## Template variables

The agent renders `compose.yml.tmpl` with this struct:

| Variable | Description |
|---|---|
| `.Slug` | Catalog slug. |
| `.Name` | Operator-chosen display name. |
| `.Domain` | Hostname operator picked at install time. |
| `.ImageChannel` | Image reference from `app.yaml`. |
| `.DataRoot` | Absolute path to `/var/lib/jabali/docker-apps/<slug>`. |
| `.CPULimit` | CPU limit string (e.g. `"0.5"`). |
| `.MemoryLimit` | Memory limit string (e.g. `"512m"`). |
| `.PIDsLimit` | PID limit integer. |
| `.Ports` | Map of port-name → `{HostPort: int, ContainerPort: int, BindInterface: string, Protocol: string}` for ports the admin enabled. |
| `.Env` | Map of env var name → value (catalog-declared + secrets auto-generated at install time). |

### Emitting an env value

An `.Env` value can come from a tenant install override, so it may hold any
character except a newline. Emit it only as the whole scalar of a mapping entry,
through `q`, which JSON-quotes it and doubles `$` so compose does not
interpolate it:

```yaml
DB_PASSWORD: {{ q (index .Env "DB_PASSWORD") }}
ADMIN_PASSWORD: {{ q (printf "%sAa1!" (index .Env "ADMIN_PASSWORD")) }}
DATABASE_URL: {{ q (printf "postgres://app:%s@db:5432/app" (userinfo (index .Env "DB_PASSWORD"))) }}
```

- Never write `"{{ index .Env "X" }}"`: a `$`, `"` or `\` in the value alters or breaks it.
- Inside a URL, wrap the value in `userinfo` first, so `@`, `:`, `/`, `%` and spaces decode back to the original.
- A shell script (`entrypoint: [sh, -c, ...]`) never splices a value. Put it in the service's `environment:` through `q` and read it in the script as `"$${NAME}"` (`$$` is compose's escape for `$`).
- Reading a value in a condition (`{{ if (index .Env "SMTP_HOST") }}`) is fine.

`TestCatalogTemplates_EnvOnlyThroughQ` fails CI on any other shape, and
`TestRender_AllApps_EnvValuesReachContainerVerbatim` renders every app with a
hostile value and checks that each container receives it exactly.

## Release tracks (major versions)

Update re-renders an install from the current catalog entry, so without a guard a bump to a new major reaches every existing install. Some apps can't take that in place: Odoo can't open an older major's database without a migration, and Nextcloud upgrades one major at a time (GH #1956). An entry for such an app declares a release track:

```yaml
version: "35.0.1"
track: "35"              # new installs get this track; version must be on it
update_from: ["34"]      # older tracks Update may move to this one
image_channel: nextcloud:35-apache@sha256:...
held_tracks:             # older lines still served, newest first
  - track: "34"
    version: "34.0.4"
    image_channel: nextcloud:34-apache@sha256:...
    update_from: ["33"]  # Update moves a 33 install here first
```

A version is on a track when it equals the track or starts with the track and a dot: `19.0` is on `19`, `1.27.3` is on `1.27` but not on `1.2`. The panel matches the version label it recorded for the install (`catalog_version`).

| The install is on | Update | An env, domain or port edit |
|---|---|---|
| the entry's track | the current version | the current version |
| a track in `update_from` | moves it to the current version | its held track's image, or the current one when it has none |
| a held track | that held track's image; the response carries a `notice` saying why it isn't moving | that held track's image |
| a track in a held track's `update_from` | moves it to that held track, with a `notice` | refused: 409 `update_required`, "Update it first" |
| none of these | refused: 409 `update_blocked`, nothing changes | refused: 409 `update_required` |

The update check offers each install the image Update would move it to, so an Odoo 19 install isn't told that Odoo 20 is its update. After a recreate onto a new image, the install's version label follows the image it now runs.

Rules for the entry:

- A held track keeps the entry's compose template, env and volumes. Only the image and the version label differ. A track that needs a different template needs its own catalog entry.
- Every held `image_channel` is digest-pinned, like the entry's own.
- The weekly `catalog-bump` job moves a tracked entry only within its track, and reports a newer major as held ("⏸ new major … held"). Moving the track, and refreshing a held track's pin, are reviewed changes.

## Why a static catalog, not dynamic discovery

Per ADR-0116 Decision 11, the catalog ships with the panel. New apps land via `jabali update`. We don't have a community-submission story yet — the design space (signing, sandbox testing, malware scanning of upstream images) is significant, and v1 ships without it.

## Validating an entry locally

The loader runs at panel startup. To exercise it without booting the panel:

```bash
go test ./panel-api/internal/dockerapp/...
```

The `TestCatalogLoad_ValidatesAllEntries` test walks every directory in `install/docker-apps/` and asserts the entry parses + matches the schema; a malformed `app.yaml` fails CI.
