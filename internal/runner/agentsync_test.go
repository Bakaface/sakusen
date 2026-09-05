package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAgentSync_FileContract verifies the round trip: the prompt reaches
// the command through $SAKUSEN_PROMPT_FILE and the answer comes back from
// $SAKUSEN_RESULT_FILE (not from stdout, which carries a decoy here).
func TestRunAgentSync_FileContract(t *testing.T) {
	requireSh(t)

	out, err := RunAgentSync(context.Background(), AgentSyncCall{
		Command: `printf 'answer for: %s' "$(cat "$SAKUSEN_PROMPT_FILE")" > "$SAKUSEN_RESULT_FILE"; echo decoy`,
	}, "summarize this")
	if err != nil {
		t.Fatalf("RunAgentSync: %v", err)
	}
	if out != "answer for: summarize this" {
		t.Errorf("answer = %q, want the result file content", out)
	}
}

// TestRunAgentSync_StdoutFallback verifies the crude fallback for commands
// that print their answer instead of writing the result file, including when
// the file exists but is empty.
func TestRunAgentSync_StdoutFallback(t *testing.T) {
	requireSh(t)

	tests := []struct {
		name    string
		command string
	}{
		{"no result file written", "echo from-stdout"},
		{"result file left empty", `: > "$SAKUSEN_RESULT_FILE"; echo from-stdout`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := RunAgentSync(context.Background(), AgentSyncCall{Command: tt.command}, "prompt")
			if err != nil {
				t.Fatalf("RunAgentSync: %v", err)
			}
			if out != "from-stdout" {
				t.Errorf("answer = %q, want the stdout fallback %q", out, "from-stdout")
			}
		})
	}
}

// TestRunAgentSync_EnvAndWorkDir verifies the exported contract variables, the
// agent record's env merged underneath it (contract wins), and the working
// directory.
func TestRunAgentSync_EnvAndWorkDir(t *testing.T) {
	requireSh(t)

	workDir := t.TempDir()
	dump := filepath.Join(t.TempDir(), "env.txt")

	_, err := RunAgentSync(context.Background(), AgentSyncCall{
		Command: `{
			echo "prompt=$SAKUSEN_PROMPT_FILE"
			echo "result=$SAKUSEN_RESULT_FILE"
			echo "purpose=$SAKUSEN_PURPOSE"
			echo "project=$SAKUSEN_PROJECT_PATH"
			echo "marker=$AGENT_MARKER"
			echo "cwd=$(pwd)"
		} > "` + dump + `"`,
		WorkDir:     workDir,
		ProjectPath: "/repo/root",
		Purpose:     "title",
		// SAKUSEN_PURPOSE from the record must not mask the contract value.
		Env: map[string]string{"AGENT_MARKER": "set", "SAKUSEN_PURPOSE": "hijacked"},
	}, "the prompt")
	if err != nil {
		t.Fatalf("RunAgentSync: %v", err)
	}

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}

	if env["purpose"] != "title" {
		t.Errorf("SAKUSEN_PURPOSE = %q, want the contract value %q", env["purpose"], "title")
	}
	if env["project"] != "/repo/root" {
		t.Errorf("SAKUSEN_PROJECT_PATH = %q, want %q", env["project"], "/repo/root")
	}
	if env["marker"] != "set" {
		t.Errorf("AGENT_MARKER = %q, want the agent record's env to be exported", env["marker"])
	}
	if env["prompt"] == "" || env["result"] == "" {
		t.Fatalf("prompt/result file vars not exported: %v", env)
	}
	if got := resolvePath(t, env["cwd"]); got != resolvePath(t, workDir) {
		t.Errorf("cwd = %q, want the requested workDir %q", got, workDir)
	}

	// The scratch directory (and both files in it) is removed after the call.
	if _, err := os.Stat(filepath.Dir(env["prompt"])); !os.IsNotExist(err) {
		t.Errorf("scratch dir %q survived the call (stat err %v)", filepath.Dir(env["prompt"]), err)
	}
	if filepath.Dir(env["prompt"]) == workDir {
		t.Error("scratch files must not live in the caller's workDir")
	}
}

// TestRunAgentSync_CommandFailure verifies a non-zero exit surfaces as an
// error carrying both streams.
func TestRunAgentSync_CommandFailure(t *testing.T) {
	requireSh(t)

	_, err := RunAgentSync(context.Background(), AgentSyncCall{
		Command: "echo out-detail; echo err-detail >&2; exit 3",
	}, "prompt")
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "out-detail") || !strings.Contains(err.Error(), "err-detail") {
		t.Errorf("error = %v, want it to carry stdout and stderr", err)
	}
}

// resolvePath resolves symlinks so /var vs /private/var (macOS temp dirs)
// don't fail the comparison.
func resolvePath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}
