package workflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallStopHook_WritesSettings verifies that InstallStopHook creates a
// well-formed Claude Code settings.json with a single Stop hook entry.
func TestInstallStopHook_WritesSettings(t *testing.T) {
	worktree := t.TempDir()

	if err := InstallStopHook(worktree, "implementing"); err != nil {
		t.Fatalf("InstallStopHook: %v", err)
	}

	settingsPath := filepath.Join(SortieSettingsDir(worktree), "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	if got := len(settings.Hooks.Stop); got != 1 {
		t.Fatalf("expected 1 Stop matcher, got %d", got)
	}
	matcher := settings.Hooks.Stop[0]
	if got := len(matcher.Hooks); got != 1 {
		t.Fatalf("expected 1 hook command, got %d", got)
	}
	if matcher.Hooks[0].Type != "command" {
		t.Errorf("expected hook type %q, got %q", "command", matcher.Hooks[0].Type)
	}
	// The command must reference the step-done directory (not just any path)
	// so we know the sentinel lands where the daemon polls.
	if !strings.Contains(matcher.Hooks[0].Command, StepDoneDir(worktree)) {
		t.Errorf("hook command missing step-done dir: %q", matcher.Hooks[0].Command)
	}
}

// TestInstallStopHook_CreatesStepDoneDir verifies that the directory the hook
// writes into exists before the hook ever fires.
func TestInstallStopHook_CreatesStepDoneDir(t *testing.T) {
	worktree := t.TempDir()

	if err := InstallStopHook(worktree, "implementing"); err != nil {
		t.Fatalf("InstallStopHook: %v", err)
	}

	info, err := os.Stat(StepDoneDir(worktree))
	if err != nil {
		t.Fatalf("expected step-done dir to exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("step-done path is not a directory")
	}
}

// TestInstallStopHook_IsIdempotent verifies that calling InstallStopHook twice
// for the same worktree+step does not error and produces the same settings.
func TestInstallStopHook_IsIdempotent(t *testing.T) {
	worktree := t.TempDir()

	if err := InstallStopHook(worktree, "implementing"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(SortieSettingsDir(worktree), "settings.json"))
	if err != nil {
		t.Fatalf("read first settings: %v", err)
	}

	if err := InstallStopHook(worktree, "implementing"); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(SortieSettingsDir(worktree), "settings.json"))
	if err != nil {
		t.Fatalf("read second settings: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("expected identical settings.json across runs")
	}
}

// TestInstallStopHook_CommandIsValidShell verifies that the generated hook
// command parses as valid POSIX shell. Running the command end-to-end requires
// a real Claude Code hook payload, so we only smoke-test the syntax here.
func TestInstallStopHook_CommandIsValidShell(t *testing.T) {
	worktree := t.TempDir()
	if err := InstallStopHook(worktree, "step name with spaces!"); err != nil {
		t.Fatalf("InstallStopHook: %v", err)
	}

	settingsPath := filepath.Join(SortieSettingsDir(worktree), "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	command := settings.Hooks.Stop[0].Hooks[0].Command

	// Pipe an empty payload through the command; bash -n only does a syntax
	// check (no execution), which is what we want.
	if err := exec.Command("bash", "-nc", command).Run(); err != nil {
		t.Errorf("hook command is not valid shell: %v\ncommand: %s", err, command)
	}
}

// TestInstallStopHook_EndToEndDropsSentinel actually executes the generated
// hook command with a fake stdin payload and verifies a sentinel JSON file
// lands in the step-done directory with the payload preserved.
func TestInstallStopHook_EndToEndDropsSentinel(t *testing.T) {
	worktree := t.TempDir()
	if err := InstallStopHook(worktree, "implementing"); err != nil {
		t.Fatalf("InstallStopHook: %v", err)
	}

	settingsPath := filepath.Join(SortieSettingsDir(worktree), "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	command := settings.Hooks.Stop[0].Hooks[0].Command

	payload := `{"session_id":"abc123","last_assistant_message":"all done"}`
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdin = strings.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook command failed: %v\noutput: %s", err, out)
	}

	entries, err := os.ReadDir(StepDoneDir(worktree))
	if err != nil {
		t.Fatalf("read step-done dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 sentinel file, got %d", len(entries))
	}

	content, err := os.ReadFile(filepath.Join(StepDoneDir(worktree), entries[0].Name()))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(content) != payload {
		t.Errorf("sentinel content mismatch:\n  got:  %q\n  want: %q", string(content), payload)
	}
}
