.PHONY: test lint typecheck format clean all venv

VENV     := .venv
PYTHON   := $(VENV)/bin/python
PIP      := $(VENV)/bin/pip
PYTEST   := $(VENV)/bin/pytest
RUFF     := $(VENV)/bin/ruff
MYPY     := $(VENV)/bin/mypy
BLACK    := $(VENV)/bin/black

all: test lint typecheck

# Bootstrap a local venv and install the daemon package with dev extras.
# Stamps $(VENV)/.ready on success so subsequent `make test` runs are fast.
$(VENV)/.ready: pyproject.toml
	@python3 -m venv $(VENV)
	@$(PIP) install --upgrade --quiet pip
	@$(PIP) install --quiet -e ".[dev]"
	@touch $@

venv: $(VENV)/.ready

test: $(VENV)/.ready
	$(PYTEST) -v --tb=short

lint: $(VENV)/.ready
	$(RUFF) check daemon/

typecheck: $(VENV)/.ready
	$(MYPY) daemon/ --strict

format: $(VENV)/.ready
	$(RUFF) check --fix daemon/
	$(BLACK) daemon/

clean:
	find . -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find . -type f -name "*.pyc" -delete
	rm -rf .pytest_cache .mypy_cache .ruff_cache build dist *.egg-info

distclean: clean
	rm -rf $(VENV)

help:
	@echo "Available targets:"
	@echo "  test       - Run pytest (bootstraps $(VENV) on first use)"
	@echo "  lint       - Run ruff linter"
	@echo "  typecheck  - Run mypy type checker"
	@echo "  format     - Format code with ruff and black"
	@echo "  venv       - Create/refresh the local virtualenv"
	@echo "  clean      - Remove build artifacts"
	@echo "  distclean  - Also remove $(VENV)"
	@echo "  all        - Run test, lint, typecheck"
