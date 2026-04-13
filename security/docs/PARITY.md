# Interface Notes

Jabali Security is managed through the **Jabali Panel Plugin** (Filament) and the **CLI**.

The standalone Flask web dashboard on port 8443 has been removed. All web-based management is now handled by the Jabali Panel plugin (`panel/`).

## Jabali Panel Plugin (`panel/`)

- Uses **Filament v5** components exclusively (no custom HTML/CSS)
- Single page with 5 grouped tabs: Overview, Threats, Defense, Intelligence, Settings
- Communicates with the daemon via **Unix socket** (`/run/jabali-security/jabali-security.sock`)
- API client: `panel/JabaliSecurityClient.php`
- After updating plugin code, **restart `jabali-panel` service** (FrankenPHP php_server mode)

## When Adding New Features

1. Add the **API endpoint** in `api/routes/`
2. Add the **CLI command** in `daemon/__main__.py`
3. Add the feature to the **panel plugin** (Filament tab/action)
4. Update `docs/API.md` if new endpoints were added
