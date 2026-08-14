# Sortie e2e tests

End-to-end tests that drive Sortie against a generic stub agent
(`stub-agent.sh`), wired into each test's `.sortie.yml` as a headless agent
record. The stub speaks sortie's agent env contract: it reads the step prompt
from `$SORTIE_PROMPT_FILE`, writes the result text to `$SORTIE_RESULT_FILE`,
and routes on `$SORTIE_PURPOSE`/`$SORTIE_STEP` to plain-text response files
under `testdata/<scenario>/` (`step-<name>.txt`, `title.txt`, ...).

## Prerequisites

- Go 1.24+
- `git` on PATH
- `tmux` is NOT required — every current test drives headless agents only (the historical tmux-gated scenario was removed)
- `jq` and `sqlite3` are optional (tests use Go directly, not shell pipelines)

## How to run

```bash
go test -tags=e2e ./tests/e2e/...
```

With verbose output:

```bash
go test -tags=e2e -v ./tests/e2e/...
```

Run a single test:

```bash
go test -tags=e2e -run TestHappyPath ./tests/e2e/...
```

## Tmpdir preservation

Set `KEEP_E2E_TMPDIR=1` to log the per-test tmpdir paths on failure. Go controls cleanup of `t.TempDir` directories, so the directories will still be removed after the test run — this flag just ensures the paths are printed in the test output before removal, for forensics.

```bash
KEEP_E2E_TMPDIR=1 go test -tags=e2e -v ./tests/e2e/...
```

## Architecture

- Each test gets an isolated `XDG_CONFIG_HOME`, project directory, and daemon process.
- `stub-agent.sh` routes responses based on `$SORTIE_PURPOSE` and files under `testdata/<scenario>/`.
- `go test ./...` does NOT compile or run these tests (build tag `e2e` is required).
