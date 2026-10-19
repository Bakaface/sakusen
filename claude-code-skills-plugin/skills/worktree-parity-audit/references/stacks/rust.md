# Rust — Worktree Parity Recipe

Marker file: `Cargo.toml`.

## Typically gitignored

- `target/` — build output (large, slow to rebuild)
- `Cargo.lock` for libraries; committed for binaries

## Standard install

- `cargo fetch` (warms the registry cache; optional)
- The verify command (`cargo test`, `cargo clippy`) does the build itself

## Gotchas

- **`target/` is per-worktree by default.** Cold rebuild can take minutes. Two ways to speed it up:
  - Set `CARGO_TARGET_DIR` to a shared location (e.g., `~/.cargo-target-shared/<project>`). Recommended over sharing the directory itself. Configure via `worktree-setup-command` exporting the env var, or via a project-level `.cargo/config.toml`.
  - Share via `worktree-sync-paths.link: [target]` (same-FS only). Risk: lockfile contention if two worktrees `cargo build` simultaneously. `CARGO_TARGET_DIR` is safer.
- **Registry cache** (`$CARGO_HOME/registry`) is global — no per-worktree setup.
