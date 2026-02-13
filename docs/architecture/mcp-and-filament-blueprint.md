# MCP and Filament Blueprint (Jabali Panel)

Last updated: 2026-02-12

This document is an internal developer blueprint for working on Jabali Panel with:

- MCP tooling (Model Context Protocol) for fast, version-correct introspection and docs.
- Filament (Admin + User panels) conventions and project-specific UI rules.

## Goals

- Keep changes consistent with the existing architecture and UI.
- Prefer version-specific documentation and project-aware inspection.
- Avoid UI regressions by following Filament-native patterns.
- Keep privileged operations isolated behind the agent.

## MCP Tooling Blueprint

Jabali is set up to be worked on with MCP tools. Use them to reduce guesswork and prevent version drift.

### 1) Laravel Boost (Most Important)

Laravel Boost MCP gives application-aware tools (routes, config, DB schema, logs, and version-specific docs).

Use it when:
- You need to confirm route names/paths and middleware.
- You need to confirm the active config (not just what you expect in `.env`).
- You need the DB schema or sample records to understand existing behavior.
- You need version-specific docs for Laravel/Livewire/Filament/Tailwind.

Preferred workflow:
- `application-info` to confirm versions and installed packages.
- `list-routes` to find the correct URL, route names, and panel prefixes.
- `get-config` for runtime config values.
- `database-schema` and `database-query` (read-only) to verify tables and relationships.
- `read-log-entries` / `last-error` to confirm the active failure.
- `search-docs` before implementing anything that depends on framework behavior.

Project rule of thumb:
- Before making a structural change in the panel, list relevant routes and key config values first.

### 2) Jabali Docs MCP Server

The repository includes `mcp-docs-server/` which exposes project docs as MCP resources/tools.

What it is useful for:
- Quick search across `README.md`, `AGENT.md`, and changelog content.
- Pulling a specific section by title.

This is not a runtime dependency of the panel. It is a developer tooling layer.

### 3) Frontend and Quality MCPs

Use these to audit and reduce UI/HTML/CSS regressions:

- `css-mcp`:
  - Analyze CSS quality/complexity.
  - Check browser compatibility for specific CSS features.
  - Pull MDN docs for CSS properties/selectors when implementing UI.
- `stylelint`:
  - Lint CSS where applicable (note: Filament pages should not use custom CSS files).
- `webdev-tools`:
  - Prettier formatting for snippets.
  - `php -l` lint for PHP syntax.
  - HTML validation for standalone HTML.

Security rule:
- Do not send secrets (tokens, passwords, private keys) into any tool query.

## Filament Blueprint (How Jabali Panels Are Built)

Jabali has two Filament panels:

- Admin panel: server-wide operations.
- User panel ("Jabali" panel): tenant/user operations.

High-level structure:
- `app/Filament/Admin/*` for admin.
- `app/Filament/Jabali/*` for user.

### Pages vs Resources

Default decision:
- Use a Filament Resource when the UI is primarily CRUD around an Eloquent model.
- Use a Filament Page when the UI is a dashboard, a multi-step wizard, or merges multiple concerns into a single screen.

### Project UI Rules (Strict)

These rules exist to keep the UI consistent and maintainable:

- Use Filament native components for layout and UI.
- Avoid raw HTML layout in Filament pages.
- Avoid custom CSS for Filament pages.
- Use Filament tables for list data.

Practical mapping:
- Layout: `Filament\Schemas\Components\Section`, `Grid`, `Tabs`, `Group`.
- Actions: `Filament\Actions\Action`.
- List data: `HasTable` / `InteractsWithTable` or `EmbeddedTable`.

### Tabs + Tables Gotcha

There is a known class of issues when a table is nested incorrectly inside schema Tabs.

Rule of thumb:
- Prefer `EmbeddedTable::make()` in schema layouts.
- Avoid mounting tables inside `View::make()` within `Tabs::make()` unless you know the action mounting behavior is preserved.

### Translations and RTL

Jabali uses JSON-based translations.

Rules:
- Use the English string as the translation key: `__('Create Domain')`.
- Do not introduce dotted translation keys like `__('domain.create')`.
- Ensure UI reads correctly in RTL locales (Arabic/Hebrew).

### Privileged Operations (Agent Boundary)

The Laravel app is the control plane. Privileged system operations are executed by the root-level agent.

Key points:
- The agent is `bin/jabali-agent`.
- The panel should call privileged operations through the Agent client service (not by shelling out directly).
- Keep all path and input validation strict before an agent call.

## Filament Blueprint Planning (Feature Specs)

When writing an implementation plan for a Filament feature, use Filament Blueprint planning docs as a checklist.

Reference:
- `vendor/filament/blueprint/resources/markdown/planning/overview.md`

At minimum, a plan should specify:
- Data model changes (tables, columns, indexes, relationships).
- Panel placement (Admin vs User) and navigation.
- Page/Resource decisions.
- Authorization model (policies/guards).
- Background jobs (for long-running operations).
- Audit logging events.
- Tests (Feature tests for endpoints and Livewire/Filament behaviors).

## Development Checklist (Per Feature)

- Confirm the correct panel and route prefix.
- List routes and verify config assumptions (Boost tools).
- Follow Filament-native components (no custom HTML/CSS in Filament pages).
- Use tables for list data.
- Keep agent boundary intact for privileged operations.
- Add or update tests and run targeted test commands.
