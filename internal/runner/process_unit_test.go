package runner

// Untagged unit tests for Process behaviors that need no subprocess spawn.
// Spawning tests live in process_test.go behind the `integration` build tag
// (see internal/runner/CLAUDE.md); these cover the pure result-file and env
// logic on the default `go test ./...` path.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResultText_Branches covers every branch of the result contract's read
// side: the result file wins when present and non-empty (trimmed), while an
// absent, empty, whitespace-only, or unconfigured result file falls back to
// the retained stdout tail. The empty-file case matters in practice: an agent
// pipeline that creates $SAKUSEN_RESULT_FILE and then crashes before writing
// must not yield an empty step context when stdout carried usable output.
func TestResultText_Branches(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tail := []string{"tail line one", "tail line two"}
	wantTail := "tail line one\ntail line two"

	tests := []struct {
		name       string
		resultFile string
		want       string
	}{
		{"result file wins over stdout tail", writeFile("full.txt", "final result"), "final result"},
		{"result file content is trimmed", writeFile("padded.txt", "\n  padded result \n\n"), "padded result"},
		{"empty result file falls back to tail", writeFile("empty.txt", ""), wantTail},
		{"whitespace-only result file falls back to tail", writeFile("blank.txt", " \n\t\n"), wantTail},
		{"missing result file falls back to tail", filepath.Join(dir, "does-not-exist.txt"), wantTail},
		{"no result file configured uses tail", "", wantTail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Process{ResultFile: tt.resultFile, stdoutTail: tail}
			if got := p.ResultText(); got != tt.want {
				t.Errorf("ResultText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildEnv_StripsClaudeCode pins the critical invariant from
// internal/runner/CLAUDE.md on the Process side (the RunSync side is covered
// in sync_test.go): CLAUDECODE is filtered from the child environment on both
// the SetEnv path and the nil-env (inherit os.Environ) path, so claude-based
// agents spawned from within a Claude Code session don't refuse to launch.
func TestBuildEnv_StripsClaudeCode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDECODEX", "keep") // prefix sibling must survive

	assertStripped := func(t *testing.T, env []string) {
		t.Helper()
		var keptSibling bool
		for _, e := range env {
			if strings.HasPrefix(e, "CLAUDECODE=") {
				t.Errorf("CLAUDECODE leaked into the child env: %q", e)
			}
			if e == "CLAUDECODEX=keep" {
				keptSibling = true
			}
		}
		if !keptSibling {
			t.Error("CLAUDECODEX (prefix sibling) must not be stripped")
		}
	}

	t.Run("after SetEnv", func(t *testing.T) {
		p := NewProcess("1", t.TempDir(), "true", "")
		p.SetEnv(map[string]string{"SAKUSEN_TASK_ID": "1"})
		env := p.buildEnv()
		assertStripped(t, env)
		var foundContract bool
		for _, e := range env {
			if e == "SAKUSEN_TASK_ID=1" {
				foundContract = true
			}
		}
		if !foundContract {
			t.Error("SetEnv overlay entry missing from buildEnv output")
		}
	})

	t.Run("without SetEnv (os.Environ fallback)", func(t *testing.T) {
		p := NewProcess("1", t.TempDir(), "true", "")
		assertStripped(t, p.buildEnv())
	})
}
