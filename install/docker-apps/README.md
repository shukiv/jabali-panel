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

## Why a static catalog, not dynamic discovery

Per ADR-0116 Decision 11, the catalog ships with the panel. New apps land via `jabali update`. We don't have a community-submission story yet — the design space (signing, sandbox testing, malware scanning of upstream images) is significant, and v1 ships without it.

## Validating an entry locally

The loader runs at panel startup. To exercise it without booting the panel:

```bash
go test ./panel-api/internal/dockerapp/...
```

The `TestCatalogLoad_ValidatesAllEntries` test walks every directory in `install/docker-apps/` and asserts the entry parses + matches the schema; a malformed `app.yaml` fails CI.
