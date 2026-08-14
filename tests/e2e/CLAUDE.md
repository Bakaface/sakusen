# tests/e2e — End-to-End Tests

Tests in this directory are gated by the `e2e` build tag. They drive sortie against a generic
stub agent (`stub-agent.sh`, wired in as an `agents:` record + `default_agent: stub`) under
per-test isolated `XDG_CONFIG_HOME` + git repos. The stub follows sortie's agent env contract
(`$SORTIE_PROMPT_FILE` in, `$SORTIE_RESULT_FILE` out, routing via `$SORTIE_PURPOSE`/`$SORTIE_STEP`).

## Running

```bash
go test -tags=e2e ./tests/e2e/...
```

`go test ./...` will **NOT** compile or run these tests. Always pass `-tags=e2e` here.

See [README.md](README.md) for prerequisites (Go 1.24+, `git`; `tmux` is not needed),
`KEEP_E2E_TMPDIR=1` for forensic tmpdir paths on failure, and the per-scenario `testdata/`
layout.

## When to run

Any change to `internal/workflow/`, `internal/daemon/`, `internal/merge/`, `cmd/sortie/`,
or step-execution plumbing should be followed by an e2e run.
