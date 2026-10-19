# PHP (Composer) — Worktree Parity Recipe

Marker file: `composer.json`.

## Typically gitignored

- `vendor/`
- `.env*`

## Standard install

- `composer install --no-interaction --prefer-dist` (dev)
- `composer install --no-dev` (prod)

## Gotchas

- Composer's global cache makes fresh `composer install` fast on a warm machine — recreate over share.
