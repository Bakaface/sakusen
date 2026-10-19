# Go — Worktree Parity Recipe

Marker file: `go.mod`.

## Typically gitignored

- `vendor/` (rare — usually committed if used)
- `bin/`, `dist/` — build outputs

## Standard install

- `go mod download` (optional — `go test ./...` does it implicitly)
- `go generate ./...` if codegen is used

## Gotchas

- **Module cache** (`$GOMODCACHE`, default `$GOPATH/pkg/mod`) is global, shared across worktrees — no setup needed.
- **Build cache** (`$GOCACHE`) is also global.
- **If `vendor/` is committed**, nothing to do. If gitignored but referenced (`-mod=vendor`), add `go mod vendor` to setup commands.
- **`tools.go` pattern**: tools listed in `go.mod` are fetched by `go mod download` but binaries are not built until `go install` or `go run` — fine for tests but flag if a `Makefile` runs `protoc-gen-go` etc.

Go is the most parity-friendly stack out of the box.
