# Elixir / Phoenix — Worktree Parity Recipe

Marker file: `mix.exs`.

## Typically gitignored

- `deps/` — dependencies
- `_build/` — compiled output
- `.elixir_ls/` — language server cache

## Standard install

- `mix deps.get`
- `mix compile`
- `mix ecto.create && mix ecto.migrate` if using Ecto

## Gotchas

- **`_build/` is per-arch / per-env.** Per-worktree is correct.
- **Hex cache** (`$MIX_HOME/.hex`) is global.
