package main

import (
	"bytes"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
)

func TestCreateCmd_MissingDescription(t *testing.T) {
	// create with no args and no stdin should fail
	cmd := createCmd
	cmd.SetArgs([]string{})
	cmd.SetIn(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing description")
	}
	if got := err.Error(); got != "input is required (provide as argument or via stdin)" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestCreateCmd_InvalidPriority(t *testing.T) {
	cmd := createCmd
	cmd.SetArgs([]string{"test task"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Set("priority", "invalid")

	err := cmd.RunE(cmd, []string{"test task"})
	if err == nil {
		t.Fatal("expected error for invalid priority")
	}
	expected := `invalid priority "invalid" (allowed: low, medium, high, urgent)`
	if got := err.Error(); got != expected {
		t.Errorf("unexpected error: %q, want %q", got, expected)
	}
	// Reset flag for other tests
	cmd.Flags().Set("priority", "")
}

func TestTasksCmd_JsonFlag(t *testing.T) {
	if f := tasksCmd.Flag("json"); f == nil {
		t.Fatal("expected --json flag on tasks command")
	}
}

func TestAgentsCmd_JsonFlag(t *testing.T) {
	if f := listCmd.Flag("json"); f == nil {
		t.Fatal("expected --json flag on agents command")
	}
}

func TestCreateCmd_TitleFlagParses(t *testing.T) {
	cmd := createCmd
	if f := cmd.Flag("title"); f == nil {
		t.Fatal("expected --title flag to be registered on create command")
	}
	if f := cmd.Flag("title"); f.Shorthand != "t" {
		t.Errorf("expected --title shorthand 't', got %q", f.Shorthand)
	}
}

func TestEditCmd_MissingTaskID(t *testing.T) {
	// edit with non-numeric task ID
	err := editCmd.RunE(editCmd, []string{"abc"})
	if err == nil {
		t.Fatal("expected error for invalid task ID")
	}
	if got := err.Error(); got != "invalid task ID: abc" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestEditCmd_NoFlags(t *testing.T) {
	cmd := editCmd
	// Ensure all field flags are empty
	cmd.Flags().Set("title", "")
	cmd.Flags().Set("description", "")
	cmd.Flags().Set("context", "")
	cmd.Flags().Set("priority", "")

	err := cmd.RunE(cmd, []string{"1"})
	if err == nil {
		t.Fatal("expected error when no field flags provided")
	}
	expected := "at least one field flag is required (--title, --input, --context, --priority)"
	if got := err.Error(); got != expected {
		t.Errorf("unexpected error: %q, want %q", got, expected)
	}
}

func TestEditCmd_InvalidPriority(t *testing.T) {
	cmd := editCmd
	cmd.Flags().Set("priority", "banana")

	err := cmd.RunE(cmd, []string{"1"})
	if err == nil {
		t.Fatal("expected error for invalid priority")
	}
	expected := `invalid priority "banana" (allowed: low, medium, high, urgent)`
	if got := err.Error(); got != expected {
		t.Errorf("unexpected error: %q, want %q", got, expected)
	}
	cmd.Flags().Set("priority", "")
}

func TestDeleteCmd_InvalidTaskID(t *testing.T) {
	err := deleteCmd.RunE(deleteCmd, []string{"notanumber"})
	if err == nil {
		t.Fatal("expected error for invalid task ID")
	}
	if got := err.Error(); got != "invalid task ID: notanumber" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestWorkflowAllowsEmptyDescription(t *testing.T) {
	tmuxFirst := config.WorkflowConfig{
		// Resolves to a tmux-mode agent, so the first step is interactive.
		Name:  "interact",
		Agent: "tmux-agent",
		Steps: []config.StepConfig{
			{Name: "shell"},
			{Name: "review"},
		},
	}
	plain := config.WorkflowConfig{
		// No agent configured — headless execution; tasks that use this
		// workflow must supply a description because there is no
		// interactive tmux session for the user to drive.
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement"},
		},
	}
	pinned := config.WorkflowConfig{
		// Input is pre-pinned by the workflow, so no -i flag is needed.
		Name:  "pinned",
		Input: "pre-set input",
		Steps: []config.StepConfig{
			{Name: "implement"},
		},
	}

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"tmux-agent": {Mode: config.AgentModeTmux, Command: "true"},
		},
		Workflows: []config.WorkflowConfig{plain, tmuxFirst, pinned},
	}

	if workflowAllowsEmptyInput(nil, "anything") {
		t.Error("nil cfg should never allow empty descriptions")
	}
	if workflowAllowsEmptyInput(cfg, "default") {
		t.Error("headless workflow should not allow empty descriptions")
	}
	if !workflowAllowsEmptyInput(cfg, "interact") {
		t.Error("tmux-first workflow should allow empty descriptions")
	}
	// Empty workflow name resolves to the first registered workflow,
	// which in this fixture is the headless workflow.
	if workflowAllowsEmptyInput(cfg, "") {
		t.Error("empty workflow name should resolve to first workflow (headless)")
	}
	// A workflow that pins its own description allows an empty -d flag.
	if !workflowAllowsEmptyInput(cfg, "pinned") {
		t.Error("description-pinned workflow should allow empty descriptions")
	}
}
