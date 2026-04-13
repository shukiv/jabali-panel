# Refactor: Extract ComponentRegistry from SecurityDaemon god object

## Problem

`SecurityDaemon.run()` in `daemon/server.py` is a 245-line method that manually constructs 20+ components, wires dependencies, populates the aiohttp app dict, and manages async lifecycle. It has 24 imports and knows every module's constructor signature. This makes it:

- **Untestable**: Can't instantiate SecurityDaemon without all 20+ real dependencies
- **Fragile**: Adding a new feature requires edits in 4+ locations within `run()`
- **Hard to read**: 61 variable assignments, 7 conditional blocks, interleaved lifecycle management

## Proposed Solution

Extract a `ComponentRegistry` dataclass in `lib/registry.py` with two entry points:

1. **`ComponentRegistry.build(config)`** — async classmethod that constructs all components, wires dependencies, handles conditional initialization
2. **`async with registry`** — context manager for async lifecycle (DB open, firewall init, shutdown cleanup)

Plus helper methods:
- `populate_app(app)` — inject all components into aiohttp app dict
- `background_tasks(daemon)` — return list of coroutines for TaskGroup

## Target State

`SecurityDaemon.run()` shrinks from 245 lines to ~22.

## Files Changed

- **New**: `lib/registry.py` (~230 lines — the extracted complexity)
- **Modified**: `daemon/server.py` — shrinks from ~430 to ~100 lines
- **Modified**: `api/app.py` — simplify `create_app()` signature
- **Unchanged**: `api/routes.py`, all `lib/` modules

## Testing Improvements

- `ComponentRegistry.build(test_config)` can be called in tests to verify wiring
- `SecurityDaemon(config, disabled={"waf", "webshield"})` enables partial startup
- `async with` pattern guarantees cleanup even on test failures

## Migration Strategy

1. Create `lib/registry.py` with ComponentRegistry
2. Move construction logic from `run()` to `build()` + `_build_*()` functions
3. Move lifecycle to `__aenter__`/`__aexit__`
4. Move app dict population to `populate_app()`
5. Move task assembly to `background_tasks()`
6. Simplify `daemon/server.py` to use the registry
7. Each step verified by restarting daemon and confirming same behavior
