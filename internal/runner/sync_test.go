package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireSh skips the test when no sh binary is available (RunSync executes
// commands via `sh -c`).
func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

// TestRunSync_Success verifies that RunSync returns the command's stdout
// verbatim on a zero exit.
func TestRunSync_Success(t *testing.T) {
	requireSh(t)
	out, err := RunSync(context.Background(), "echo hello", "", nil, "")
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	if out != "hello\n" {
		t.Errorf("stdout = %q, want %q", out, "hello\n")
	}
}

// TestRunSync_StdinPiped verifies that the stdin argument is piped to the
// command's standard input.
func TestRunSync_StdinPiped(t *testing.T) {
	requireSh(t)
	out, err := RunSync(context.Background(), "cat", "", nil, "hello")
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	if out != "hello" {
		t.Errorf("stdout = %q, want %q", out, "hello")
	}
}

// TestRunSync_EnvOverlay verifies that entries in the env map are added on top
// of the inherited process environment.
func TestRunSync_EnvOverlay(t *testing.T) {
	requireSh(t)
	out, err := RunSync(context.Background(), `echo "$FOO"`, "", map[string]string{"FOO": "bar"}, "")
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	if out != "bar\n" {
		t.Errorf("stdout = %q, want %q", out, "bar\n")
	}
}

// TestRunSync_StripsClaudeCode verifies that the CLAUDECODE variable is
// stripped from the child environment so claude-based commands work when
// sortie itself runs inside a Claude Code session.
func TestRunSync_StripsClaudeCode(t *testing.T) {
	requireSh(t)
	t.Setenv("CLAUDECODE", "1")
	out, err := RunSync(context.Background(), `echo "${CLAUDECODE:-unset}"`, "", nil, "")
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	if out != "unset\n" {
		t.Errorf("stdout = %q, want %q (CLAUDECODE should be stripped)", out, "unset\n")
	}
}

// TestRunSync_Failure verifies that a non-zero exit yields an empty result and
// an error that surfaces both stdout and stderr for diagnosis.
func TestRunSync_Failure(t *testing.T) {
	requireSh(t)
	out, err := RunSync(context.Background(), "echo out; echo err >&2; exit 3", "", nil, "")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if out != "" {
		t.Errorf("stdout on failure = %q, want empty", out)
	}
	for _, want := range []string{"command failed", "out", "err"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestRunSync_WorkDir verifies that the command runs in the given workDir.
func TestRunSync_WorkDir(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	out, err := RunSync(context.Background(), "pwd", dir, nil, "")
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	// Compare with symlinks resolved: on macOS the temp dir is under
	// /var -> /private/var.
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("EvalSymlinks(output): %v", err)
	}
	if gotDir != wantDir {
		t.Errorf("working directory = %q, want %q", gotDir, wantDir)
	}
}

// TestRunSync_ContextTimeout verifies that a context deadline kills the
// command and surfaces an error.
func TestRunSync_ContextTimeout(t *testing.T) {
	requireSh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := RunSync(ctx, "sleep 5", "", nil, ""); err == nil {
		t.Fatal("expected error when context times out")
	}
}

// TestTruncateForError verifies that truncateForError trims whitespace first,
// leaves strings up to 500 bytes unchanged, and clips longer strings to a
// 500-byte prefix plus a truncation suffix carrying the total byte count.
func TestTruncateForError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short unchanged", "hello", "hello"},
		{"exactly 500 bytes unchanged", strings.Repeat("a", 500), strings.Repeat("a", 500)},
		{
			"over 500 bytes truncated",
			strings.Repeat("a", 501),
			strings.Repeat("a", 500) + fmt.Sprintf("... (truncated, %d total bytes)", 501),
		},
		{"whitespace trimmed", "  hi\n", "hi"},
		{
			"trim before length check",
			strings.Repeat("a", 490) + strings.Repeat(" ", 20),
			strings.Repeat("a", 490),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateForError(tt.input); got != tt.want {
				t.Errorf("truncateForError(len %d) = %q, want %q", len(tt.input), got, tt.want)
			}
		})
	}
}

// TestStripClaudeCode verifies that stripClaudeCode drops exactly the entries
// with a "CLAUDECODE=" key prefix and keeps everything else, including keys
// that merely start with "CLAUDECODE" (e.g. CLAUDECODEX).
func TestStripClaudeCode(t *testing.T) {
	got := stripClaudeCode([]string{"CLAUDECODE=1", "PATH=/x", "CLAUDECODEX=1"})
	want := []string{"PATH=/x", "CLAUDECODEX=1"}
	if len(got) != len(want) {
		t.Fatalf("stripClaudeCode = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}
