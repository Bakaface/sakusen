package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".sortie.yml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestValidateFile_Valid(t *testing.T) {
	path := writeTempConfig(t, `
max_workers: 2
default_priority: high
git:
  branch_template: "sortie/{{task_id}}-{{task_slug}}"
on_complete: commit
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "do the thing"
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateFile_MissingFile(t *testing.T) {
	err := ValidateFile(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("expected read config error, got: %v", err)
	}
}

func TestValidateFile_BadYAML(t *testing.T) {
	path := writeTempConfig(t, "max_workers: : :\n")
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "parse yaml") {
		t.Fatalf("expected parse yaml error, got: %v", err)
	}
}

func TestValidateFile_UnknownTopLevelField(t *testing.T) {
	// The skill notes that `worktree_sync_paths` (snake_case) is a common typo
	// for the canonical `worktree-sync-paths` — strict parsing should flag it.
	path := writeTempConfig(t, "worktree_sync_paths:\n  copy: [.env]\n")
	err := ValidateFile(path)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateFile_InvalidOnComplete(t *testing.T) {
	path := writeTempConfig(t, "on_complete: yeet\n")
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "on_complete") {
		t.Fatalf("expected on_complete error, got: %v", err)
	}
}

func TestValidateFile_GitOnCompleteRemoved(t *testing.T) {
	// git.on_complete moved to the top level; the legacy location must surface a
	// migration error rather than being silently ignored.
	path := writeTempConfig(t, "git:\n  on_complete: merge\n")
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "git.on_complete was moved") {
		t.Fatalf("expected git.on_complete migration error, got: %v", err)
	}
}

func TestValidateFile_InvalidWorkflowOnComplete(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: default
    on_complete: yeet
    steps:
      - name: implementing
        prompt: "do the thing"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "on_complete") {
		t.Fatalf("expected workflow on_complete error, got: %v", err)
	}
}

func TestValidateFile_InvalidPriority(t *testing.T) {
	path := writeTempConfig(t, "default_priority: chill\n")
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "default_priority") {
		t.Fatalf("expected default_priority error, got: %v", err)
	}
}

func TestValidateFile_InvalidTmuxNestedBehavior(t *testing.T) {
	path := writeTempConfig(t, "tmux_nested_attach_behavior: explode\n")
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "tmux_nested_attach_behavior") {
		t.Fatalf("expected tmux_nested_attach_behavior error, got: %v", err)
	}
}

func TestValidateFile_ForwardLoopGoto(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: bad
    steps:
      - name: a
        prompt: "a"
        loop:
          goto: b
          max_iterations: 2
      - name: b
        prompt: "b"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "earlier step") {
		t.Fatalf("expected forward goto error, got: %v", err)
	}
}

func TestValidateFile_InvalidSummarizationStrategy(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: bad
    steps:
      - name: a
        prompt: "a"
        summarization_strategy: telepathy
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "summarization_strategy") {
		t.Fatalf("expected summarization_strategy error, got: %v", err)
	}
}

func TestValidateFile_DuplicateWorkflowName(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: dup
    steps:
      - name: a
        prompt: "a"
  - name: dup
    steps:
      - name: a
        prompt: "a"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate workflow") {
		t.Fatalf("expected duplicate workflow error, got: %v", err)
	}
}

func TestValidateFile_LegacyWorkflowTmuxFieldFails(t *testing.T) {
	// The legacy `tmux:` field at the workflow level was removed — execution
	// mode now comes from the agent record; loading an old config should
	// surface a clear migration error rather than silently dropping the setting.
	path := writeTempConfig(t, `
workflows:
  - name: w
    tmux: true
    steps:
      - name: s
        prompt: "do"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "tmux") || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("expected migration error mentioning tmux and the agent replacement, got: %v", err)
	}
}

func TestValidateFile_LegacyStepTmuxFieldFails(t *testing.T) {
	// The legacy step-level `tmux:` field is also removed; surface the same
	// migration guidance from the step decoder.
	path := writeTempConfig(t, `
workflows:
  - name: w
    steps:
      - name: s
        prompt: "do"
        tmux: false
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "tmux") || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("expected migration error mentioning tmux and the agent replacement, got: %v", err)
	}
}

func TestValidateFile_LegacyPrintFieldFails(t *testing.T) {
	// The `print:` field (workflow and step level) was removed; both levels
	// must surface the agent-mechanism migration error.
	t.Run("workflow level", func(t *testing.T) {
		path := writeTempConfig(t, `
workflows:
  - name: w
    print: true
    steps:
      - name: s
        prompt: "do"
`)
		err := ValidateFile(path)
		if err == nil || !strings.Contains(err.Error(), "print") || !strings.Contains(err.Error(), "agent") {
			t.Fatalf("expected migration error mentioning print and agent, got: %v", err)
		}
	})

	t.Run("step level", func(t *testing.T) {
		path := writeTempConfig(t, `
workflows:
  - name: w
    steps:
      - name: s
        prompt: "do"
        print: true
`)
		err := ValidateFile(path)
		if err == nil || !strings.Contains(err.Error(), "print") || !strings.Contains(err.Error(), "agent") {
			t.Fatalf("expected migration error mentioning print and agent, got: %v", err)
		}
	})
}

// TestValidateFile_RemovedTopLevelKeys verifies the removed top-level keys are
// caught by single-file validation with their migration messages, before the
// strict decoder can turn them into generic "field not found" errors.
func TestValidateFile_RemovedTopLevelKeys(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"claude block", "claude:\n  command: /tmp/foo\n", "`claude:` block was removed"},
		{"yolo", "yolo: true\n", "`yolo:` was removed"},
		{"system_prompt", "system_prompt: \"be terse\"\n", "`system_prompt:` was removed"},
		{"allowed_summarization_models", "allowed_summarization_models: [haiku]\n", "`allowed_summarization_models` was removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			err := ValidateFile(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected migration error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestValidateFile_StepAllowedSummarizationModelsRemoved verifies the removed
// step-level `allowed_summarization_models:` field errors with a pointer at
// the top-level summarizer command.
func TestValidateFile_StepAllowedSummarizationModelsRemoved(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: w
    steps:
      - name: s
        prompt: "do"
        allowed_summarization_models: [haiku]
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "allowed_summarization_models") || !strings.Contains(err.Error(), "summarizer") {
		t.Fatalf("expected migration error pointing at `summarizer:`, got: %v", err)
	}
}

// TestValidateFile_AgentRecordShapeChecked verifies single-file diagnosis
// validates agent record shapes (mode, command, tmux-only fields, slug).
func TestValidateFile_AgentRecordShapeChecked(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"invalid mode", "agents:\n  a:\n    mode: quantum\n    command: x\n", "invalid mode"},
		{"missing command", "agents:\n  a:\n    mode: tmux\n", "command is required"},
		{"resume_command on headless", "agents:\n  a:\n    command: x\n    resume_command: y\n", "resume_command is only valid for tmux-mode agents"},
		{"chat_log_command on headless", "agents:\n  a:\n    command: x\n    chat_log_command: y\n", "chat_log_command is only valid for tmux-mode agents"},
		{"invalid slug", "agents:\n  Bad_Slug:\n    command: x\n", "kebab-case"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			err := ValidateFile(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestValidateFile_AgentRefsNotResolved verifies single-file diagnosis does
// NOT resolve agent references (default_agent, workflow/step agent:) — they may
// point at agents defined in the global tier, which only the full Load path
// merges and validates.
func TestValidateFile_AgentRefsNotResolved(t *testing.T) {
	path := writeTempConfig(t, `
default_agent: defined-elsewhere
workflows:
  - name: w
    agent: also-elsewhere
    steps:
      - name: s
        prompt: "do"
        agent: elsewhere-too
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("agent refs must not be resolved by single-file validation, got: %v", err)
	}
}

func TestValidateFile_MissingFileRefIsError(t *testing.T) {
	// String ref to a non-existent file should be a hard error.
	dir := t.TempDir()
	path := filepath.Join(dir, ".sortie.yml")
	if err := os.WriteFile(path, []byte(`
workflows:
  - phantom
`), 0644); err != nil {
		t.Fatal(err)
	}
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "phantom") {
		t.Fatalf("expected missing-ref error, got %v", err)
	}
}

func TestValidateFile_InlineFileCollisionIsError(t *testing.T) {
	// Inline workflow that collides with a file-based workflow of the same name.
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".sortie", "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "foo.yml"), []byte("steps:\n  - name: a\n    prompt: a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ymlPath := filepath.Join(dir, ".sortie.yml")
	if err := os.WriteFile(ymlPath, []byte(`
workflows:
  - name: foo
    steps:
      - name: a
        prompt: "a"
`), 0644); err != nil {
		t.Fatal(err)
	}
	err := ValidateFile(ymlPath)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected collision error, got %v", err)
	}
}

func TestValidateFile_DuplicateStepName(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: w
    steps:
      - name: a
        prompt: "a"
      - name: a
        prompt: "a"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate step") {
		t.Fatalf("expected duplicate step error, got: %v", err)
	}
}

// TestValidateFile_PinsBranchAndCheckoutTogether verifies that setting both
// branch and checkout on a workflow is rejected.
func TestValidateFile_PinsBranchAndCheckoutTogether(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: bad
    worktree: true
    branch: "feature/x"
    checkout: "main"
    steps:
      - name: implement
        prompt: "do"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "cannot set both branch and checkout") {
		t.Fatalf("expected branch+checkout error, got: %v", err)
	}
}

// TestValidateFile_PinsBranchWithWorktreeFalse verifies that branch is
// rejected when worktree is pinned false.
func TestValidateFile_PinsBranchWithWorktreeFalse(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: bad
    worktree: false
    branch: "feature/x"
    steps:
      - name: implement
        prompt: "do"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "branch/checkout/target cannot be set when worktree: false") {
		t.Fatalf("expected worktree=false+branch error, got: %v", err)
	}
}

// TestValidateFile_SubdirectoryInWorkflowsRejected verifies that subdirectories
// under .sortie/workflows/ produce an error (flat-only layout).
func TestValidateFile_SubdirectoryInWorkflowsRejected(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, ".sortie", "workflows", "tasks")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Subdirectory exists — loadWorkflowFilePool should reject it.
	ymlPath := filepath.Join(dir, ".sortie.yml")
	if err := os.WriteFile(ymlPath, []byte("workflows: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ValidateFile(ymlPath)
	if err == nil || !strings.Contains(err.Error(), "subdirector") {
		t.Fatalf("expected subdirectory error, got %v", err)
	}
}
