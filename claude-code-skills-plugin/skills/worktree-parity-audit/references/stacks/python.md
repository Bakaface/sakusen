# Python — Worktree Parity Recipe

Marker files: `pyproject.toml`, `requirements.txt`, `Pipfile`, `setup.py`.

## Typically gitignored

- `.venv/`, `venv/`, `env/` — virtualenv
- `__pycache__/`, `*.pyc` — bytecode
- `.pytest_cache/`, `.mypy_cache/`, `.ruff_cache/` — caches
- `.env`, `.envrc`
- `*.egg-info/`

## Standard install

| Tool | Setup |
|---|---|
| uv | `uv sync --frozen` |
| Poetry | `poetry install --no-root` |
| pip + venv | `python -m venv .venv && .venv/bin/pip install -r requirements.txt` |
| pip + pyproject | `python -m venv .venv && .venv/bin/pip install -e .[dev]` |
| Pipenv | `pipenv install --dev` |

## Gotchas

- **uv** is by far the fastest — sub-second on a warm cache. Recommend it if the project uses it.
- **Shared `.venv/` via `link:`** works for read-only deps but breaks when pytest writes `.pyc` into installed packages. Prefer per-worktree venv + a fast installer.
- **C extensions** (`numpy`, `psycopg2`, `cryptography`) may need system headers — assume the dev environment has them; flag if compilation fails in the parity check.
- **Activate handling**: most package managers use absolute paths in `.venv/bin/activate`, which break when the venv is hard-linked across worktrees. Recreate, don't share.
