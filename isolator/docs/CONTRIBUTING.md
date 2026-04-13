# Contributing to jabali-isolator

## Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/) (package manager)
- Linux with systemd (for integration testing)

## Setup

```bash
git clone gitea:shukivaknin/jabali-isolator.git
cd jabali-isolator
uv sync
```

## Development Commands

<!-- AUTO-GENERATED from pyproject.toml -->
| Command | Description |
|---------|-------------|
| `uv sync` | Install/update all dependencies |
| `uv run pytest -v` | Run test suite (51 tests, async) |
| `uv run ruff check .` | Lint (E, F, W, I, S rules) |
| `uv run ruff format .` | Auto-format code |
| `uv run ruff check --fix .` | Auto-fix lint issues |
<!-- END AUTO-GENERATED -->

## CLI Entry Point

The `jabali-isolate` command is defined in `pyproject.toml` as:
```
jabali-isolate = jabali_isolator.__main__:cli
```

Install in development mode: `uv pip install -e .`

## Project Structure

```
jabali_isolator/
    config.py       Constants, validation
    machine.py      {user}-php naming convention (single source of truth)
    rootfs.py       Minimal rootfs builder
    units.py        systemd unit file generators
    container.py    Async orchestrator (create/start/stop/destroy/status/list)
    __main__.py     Click CLI entry point

tests/
    conftest.py     Shared fixtures (isolator_dirs, fake_user)
    test_container.py
    test_rootfs.py
    test_units.py
```

## Testing

All tests are async and use pytest-asyncio with `asyncio_mode = "auto"`.

### Mocking Conventions

- **Subprocess calls**: `patch("jabali_isolator.container._run", new_callable=AsyncMock)`
- **User lookup**: `patch("jabali_isolator.rootfs._lookup_user")`
- **Config paths**: The `isolator_dirs` fixture in `conftest.py` patches all `config.*` paths to `tmp_path`

### Running Tests

```bash
uv run pytest -v              # all tests
uv run pytest tests/test_container.py  # single module
uv run pytest -k "test_create"         # by name pattern
```

## Code Style

- Line length: 120 characters
- Formatter: ruff format
- Linter: ruff (E, F, W, I, S rule sets)
- Target: Python 3.12+
- `tests/` ignores S101 (assert usage)

## Security Checklist

Before submitting changes:

- [ ] No `shell=True` in subprocess calls (use list args)
- [ ] Username validated against `USERNAME_RE` at every public entry point
- [ ] `shutil.rmtree()` paths canonicalized with `.resolve().is_relative_to()`
- [ ] No hardcoded secrets or credentials
- [ ] Resource limits validated before writing to unit files

## PR Process

1. Create a feature branch from `master`
2. Write tests first (TDD)
3. Implement the feature
4. Run `uv run pytest -v && uv run ruff check .`
5. Push and create PR against `master`
