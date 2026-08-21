package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildWrapperScript_Shape(t *testing.T) {
	script := BuildWrapperScript("echo hello", map[string]string{
		"SAKUSEN_TASK_ID": "42",
	})

	if !strings.HasPrefix(script, "#!/bin/bash\n") {
		t.Errorf("script should start with bash shebang, got %q", script)
	}
	if !strings.Contains(script, `export SAKUSEN_TASK_ID='42'`+"\n") {
		t.Errorf("script should export env vars, got %q", script)
	}
	if !strings.Contains(script, "echo hello\n") {
		t.Errorf("script should contain the command, got %q", script)
	}
	if !strings.HasSuffix(script, "\nexec bash\n") {
		t.Errorf("script should end with `exec bash` so the pane survives, got %q", script)
	}
}

func TestBuildWrapperScript_SortedKeysAreDeterministic(t *testing.T) {
	env := map[string]string{
		"SAKUSEN_WORKTREE": "/w",
		"SAKUSEN_TASK_ID":  "1",
		"SAKUSEN_STEP":     "plan",
	}
	first := BuildWrapperScript("cmd", env)
	for i := 0; i < 10; i++ {
		if got := BuildWrapperScript("cmd", env); got != first {
			t.Fatalf("script not deterministic across builds:\n%q\nvs\n%q", got, first)
		}
	}
	stepIdx := strings.Index(first, "SAKUSEN_STEP")
	taskIdx := strings.Index(first, "SAKUSEN_TASK_ID")
	wtIdx := strings.Index(first, "SAKUSEN_WORKTREE")
	if !(stepIdx < taskIdx && taskIdx < wtIdx) {
		t.Errorf("env exports should be in sorted key order, got:\n%s", first)
	}
}

func TestBuildWrapperScript_QuotesEnvValues(t *testing.T) {
	script := BuildWrapperScript("cmd", map[string]string{
		"SAKUSEN_PROMPT_FILE": `/tmp/dir with spaces/prompt "quoted".txt`,
	})
	if !strings.Contains(script, `export SAKUSEN_PROMPT_FILE='/tmp/dir with spaces/prompt "quoted".txt'`) {
		t.Errorf("env values should be single-quoted, got %q", script)
	}
}

// TestBuildWrapperScript_EnvValuesAreLiteral executes the generated script and
// verifies that shell metacharacters in env values ($vars, `command`
// substitutions, quotes) reach the agent byte-for-byte rather than being
// expanded — matching Process.SetEnv semantics on the headless path. The old
// %q quoting left $ and backticks live inside double quotes, so a value like
// $(rm -rf x) would have been executed by the wrapper.
func TestBuildWrapperScript_EnvValuesAreLiteral(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	outFile := filepath.Join(dir, "out.txt")
	hostile := `$(touch ` + marker + ") `touch " + marker + "` $HOME 'quoted' \"double\""

	script := BuildWrapperScript(`printf '%s' "$HOSTILE" > `+shellQuote(outFile), map[string]string{
		"HOSTILE": hostile,
	})

	scriptFile := filepath.Join(dir, "wrapper.sh")
	if err := os.WriteFile(scriptFile, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Stdin defaults to /dev/null, so the trailing `exec bash` sees EOF and
	// exits immediately instead of hanging interactively.
	if out, err := exec.Command("bash", scriptFile).CombinedOutput(); err != nil {
		t.Fatalf("wrapper script failed: %v\n%s\nscript:\n%s", err, out, script)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("command substitution in env value was executed:\nscript:\n%s", script)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hostile {
		t.Errorf("env value not preserved literally:\ngot  %q\nwant %q", got, hostile)
	}
}

func TestBuildWrapperScript_IsValidBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script := BuildWrapperScript("echo 'agent run'", map[string]string{
		"SAKUSEN_TASK_ID":     "7",
		"SAKUSEN_PROMPT_FILE": "/tmp/it's got a quote.txt",
	})
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("generated script is not valid bash: %v\n%s\nscript:\n%s", err, out, script)
	}
}

// TestBuildWrapperScript_EmptyEnv verifies that a nil env map yields just the
// shebang, the command, and the trailing `exec bash` — no export lines.
func TestBuildWrapperScript_EmptyEnv(t *testing.T) {
	script := BuildWrapperScript("echo hello", nil)
	want := "#!/bin/bash\necho hello\nexec bash\n"
	if script != want {
		t.Errorf("script with nil env = %q, want %q", script, want)
	}
	if strings.Contains(script, "export") {
		t.Errorf("script with nil env should have no export lines, got %q", script)
	}
}

func TestMergeEnv_ContractWins(t *testing.T) {
	contract := map[string]string{
		"SAKUSEN_TASK_ID": "1",
		"SAKUSEN_STEP":    "plan",
	}
	agentEnv := map[string]string{
		"SAKUSEN_TASK_ID": "masked", // must lose to the contract
		"AGENT_FLAG":      "on",
	}

	got := MergeEnv(contract, agentEnv)

	if got["SAKUSEN_TASK_ID"] != "1" {
		t.Errorf("contract must win on collisions, got SAKUSEN_TASK_ID=%q", got["SAKUSEN_TASK_ID"])
	}
	if got["SAKUSEN_STEP"] != "plan" || got["AGENT_FLAG"] != "on" {
		t.Errorf("merged env missing entries: %v", got)
	}
	if agentEnv["SAKUSEN_TASK_ID"] != "masked" || contract["SAKUSEN_TASK_ID"] != "1" {
		t.Errorf("inputs must not be mutated: contract=%v agentEnv=%v", contract, agentEnv)
	}
}

// TestMergeEnv_NilInputs verifies that MergeEnv(nil, nil) returns a non-nil
// empty map — callers assign into the result, so a nil map would panic.
func TestMergeEnv_NilInputs(t *testing.T) {
	got := MergeEnv(nil, nil)
	if got == nil {
		t.Fatal("MergeEnv(nil, nil) returned nil, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("MergeEnv(nil, nil) = %v, want empty map", got)
	}
	got["extra"] = "ok" // must be assignable without panicking
	if got["extra"] != "ok" {
		t.Errorf("assignment into result failed: %v", got)
	}
}
