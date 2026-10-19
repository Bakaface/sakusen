# Ruby / Rails — Worktree Parity Recipe

Marker file: `Gemfile`.

## Typically gitignored

- `vendor/bundle/` (if `bundle config set --local path 'vendor/bundle'`)
- `tmp/`, `log/`
- `db/*.sqlite3` — SQLite dev DB
- `config/master.key`, `config/credentials/*.key` — sensitive
- `.env`, `.env.local`
- `node_modules/` (asset pipeline)

## Standard install

- `bundle install` (system gem path, no copy) or `bundle install --path vendor/bundle` (local)
- `bin/rails db:prepare` for DB
- `pnpm install` (or yarn/npm) if assets use a Node toolchain

## Gotchas

- **`config/master.key`** is required for Rails to decrypt credentials. Share via `worktree-sync-paths.link: [config/master.key]`.
- **Per-worktree SQLite is correct**; multiple worktrees on a shared DB will lock-contend.
- **`bin/setup`** (if present) is often the canonical recipe — read it before guessing.
