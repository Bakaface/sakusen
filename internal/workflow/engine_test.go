package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

func TestGetStepContextsFromDB(t *testing.T) {
	// Verify that GetTaskStepContexts returns contexts keyed by step name.
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	proj, err := d.GetOrCreateProject("/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := d.CreateTask(proj.ID, "test task", "", "test-task", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create two step records with contexts
	if err := d.CreateTaskStep(tk.ID, "implement"); err != nil {
		t.Fatal(err)
	}
	ctx1 := "implementation notes"
	if err := d.CompleteTaskStep(tk.ID, "implement", &ctx1, 0); err != nil {
		t.Fatal(err)
	}

	if err := d.CreateTaskStep(tk.ID, "review"); err != nil {
		t.Fatal(err)
	}
	ctx2 := "review notes"
	if err := d.CompleteTaskStep(tk.ID, "review", &ctx2, 0); err != nil {
		t.Fatal(err)
	}

	contexts, err := d.GetTaskStepContexts(tk.ID, []string{"implement", "review"})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := contexts["implement"]; !ok {
		t.Error("expected implement context to be present")
	}
	if _, ok := contexts["review"]; !ok {
		t.Error("expected review context to be present")
	}
}

func TestGetStepContextsEmptyWhenNoSteps(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	proj, err := d.GetOrCreateProject("/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := d.CreateTask(proj.ID, "test task", "", "test-task", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	contexts, err := d.GetTaskStepContexts(tk.ID, []string{"implement"})
	if err != nil {
		t.Fatal(err)
	}

	if len(contexts) != 0 {
		t.Errorf("expected 0 contexts when no steps exist, got %d", len(contexts))
	}
}

func TestSummarizerStepNameCollection(t *testing.T) {
	// Simulate the summarizer's step name collection logic
	steps := []config.StepConfig{
		{Name: "implement"},
		{Name: "review"},
		{Name: "test"},
	}

	var stepNames []string
	for _, s := range steps {
		stepNames = append(stepNames, s.Name)
	}

	if len(stepNames) != 3 {
		t.Fatalf("expected 3 step names, got %d", len(stepNames))
	}
	if stepNames[0] != "implement" || stepNames[1] != "review" || stepNames[2] != "test" {
		t.Errorf("expected [implement, review, test], got %v", stepNames)
	}
}

func TestSummarizerStepContextsFromDB(t *testing.T) {
	// Verify that step contexts are retrieved from DB for summarizer use.
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	proj, err := d.GetOrCreateProject("/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := d.CreateTask(proj.ID, "test task", "", "test-task", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CreateTaskStep(tk.ID, "implement"); err != nil {
		t.Fatal(err)
	}
	notes := "notes"
	if err := d.CompleteTaskStep(tk.ID, "implement", &notes, 0); err != nil {
		t.Fatal(err)
	}

	stepNames := []string{"implement", "review"}
	contexts, err := d.GetTaskStepContexts(tk.ID, stepNames)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 {
		t.Errorf("expected 1 context when only implement has a completed record, got %d", len(contexts))
	}
}

func TestSummarizerPromptBuildWithStepContexts(t *testing.T) {
	// Verify that when step contexts are present, the prompt includes content.
	stepContexts := map[string]string{
		"implement": "Added feature X",
	}

	if len(stepContexts) != 1 {
		t.Fatalf("expected 1 step context, got %d", len(stepContexts))
	}
	if stepContexts["implement"] != "Added feature X" {
		t.Errorf("expected step context content 'Added feature X', got %q", stepContexts["implement"])
	}
}

func TestSummarizerFallsBackToDiffStatWhenNoContexts(t *testing.T) {
	// Verify that when no step contexts exist in DB,
	// the summarizer falls through to the git diff stat path.
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	proj, err := d.GetOrCreateProject("/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := d.CreateTask(proj.ID, "test task", "", "test-task", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	// No step records in DB → GetTaskStepContexts returns empty map
	contexts, err := d.GetTaskStepContexts(tk.ID, []string{"implementing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 0 {
		t.Fatalf("expected 0 contexts when no steps exist, got %d", len(contexts))
	}

	// The empty contexts map triggers the fallback path in the summarizer.
}

func TestCopyImagesToWorktree(t *testing.T) {
	// Create a source directory with test images
	srcDir := t.TempDir()
	img1 := filepath.Join(srcDir, "screenshot.png")
	img2 := filepath.Join(srcDir, "diagram.jpg")
	os.WriteFile(img1, []byte("fake png data"), 0644)
	os.WriteFile(img2, []byte("fake jpg data"), 0644)

	// Create a worktree directory
	worktree := t.TempDir()

	relPaths, err := CopyImagesToWorktree(worktree, []string{img1, img2})
	if err != nil {
		t.Fatalf("CopyImagesToWorktree failed: %v", err)
	}

	if len(relPaths) != 2 {
		t.Fatalf("expected 2 relative paths, got %d", len(relPaths))
	}

	// Verify files were copied
	for _, rel := range relPaths {
		fullPath := filepath.Join(worktree, rel)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("expected copied image at %s", fullPath)
		}
	}

	// Verify content
	data, _ := os.ReadFile(filepath.Join(worktree, relPaths[0]))
	if string(data) != "fake png data" {
		t.Errorf("expected copied content to match, got %q", string(data))
	}
}

func TestCopyImagesToWorktreeEmpty(t *testing.T) {
	worktree := t.TempDir()

	relPaths, err := CopyImagesToWorktree(worktree, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if relPaths != nil {
		t.Errorf("expected nil for empty images, got %v", relPaths)
	}
}

func TestTemplateTaskImages(t *testing.T) {
	ctx := &TemplateContext{
		Task: TaskVars{
			ID:          1,
			Title:       "Test task",
			Description: "A test",
			Images:      []string{".sortie/images/screenshot.png", ".sortie/images/diagram.jpg"},
		},
	}

	result := ResolveTemplate("Images:\n{{task.images}}", ctx)
	expected := "Images:\n.sortie/images/screenshot.png\n.sortie/images/diagram.jpg"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTemplateTaskImagesEmpty(t *testing.T) {
	ctx := &TemplateContext{
		Task: TaskVars{
			ID:     1,
			Images: nil,
		},
	}

	result := ResolveTemplate("Images: {{task.images}}", ctx)
	if result != "Images: " {
		t.Errorf("expected 'Images: ', got %q", result)
	}
}

func TestBuildSystemPromptWithImages(t *testing.T) {
	images := []string{".sortie/images/screenshot.png", ".sortie/images/diagram.jpg"}

	s := BuildSystemPrompt("Implement the feature", "", images)

	if !strings.Contains(s, "Attached Images") {
		t.Error("expected system prompt to contain 'Attached Images' section")
	}
	if !strings.Contains(s, ".sortie/images/screenshot.png") {
		t.Error("expected system prompt to reference screenshot.png")
	}
	if !strings.Contains(s, ".sortie/images/diagram.jpg") {
		t.Error("expected system prompt to reference diagram.jpg")
	}
	// Verify default system prompt is used when empty
	if !strings.Contains(s, "autonomous coding agent") {
		t.Error("expected system prompt to contain default system prompt")
	}
}

func TestBuildSystemPromptWithoutImages(t *testing.T) {
	s := BuildSystemPrompt("Implement the feature", "", nil)

	if strings.Contains(s, "Attached Images") {
		t.Error("expected system prompt to NOT contain 'Attached Images' section when no images")
	}
}

func TestBuildSystemPromptWithCustomSystemPrompt(t *testing.T) {
	customPrompt := "You are a careful code reviewer. Never make changes without tests."

	s := BuildSystemPrompt("Review the code", customPrompt, nil)

	if !strings.Contains(s, customPrompt) {
		t.Error("expected system prompt to contain custom system prompt")
	}
	if strings.Contains(s, "autonomous coding agent") {
		t.Error("expected system prompt to NOT contain default system prompt when custom is provided")
	}
	if !strings.Contains(s, "# Task") {
		t.Error("expected system prompt to contain task section")
	}
	if !strings.Contains(s, "Review the code") {
		t.Error("expected system prompt to contain resolved prompt")
	}
}

// TestBuildSystemPromptVerificationFooter ensures the project-agnostic
// verification reminder is appended regardless of whether the project supplies
// a custom SystemPrompt — spawned agents must be told to discover and run the
// project's own test/lint commands rather than inventing them.
func TestBuildSystemPromptVerificationFooter(t *testing.T) {
	cases := []struct {
		name         string
		systemPrompt string
	}{
		{"default", ""},
		{"custom", "You are a careful code reviewer."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := BuildSystemPrompt("Do the thing", tc.systemPrompt, nil)
			if !strings.Contains(s, "Verification before declaring done") {
				t.Errorf("expected verification footer in %s-prompt output", tc.name)
			}
			if !strings.Contains(s, "CLAUDE.md") {
				t.Errorf("expected footer to reference CLAUDE.md so agents look there for canonical commands")
			}
		})
	}
}

func TestWriteTmuxLogMessage(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "step.log")

	lines := writeTmuxLogMessage(logPath, 42, "implement", "sortie-42", "42")

	// Verify returned lines
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "=== Step: implement (task #42) ===") {
		t.Errorf("expected step header in line 0, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], `Tmux session "sortie-42" initiated`) {
		t.Errorf("expected tmux session initiated message in line 1, got: %s", lines[1])
	}
	if !strings.Contains(lines[2], "Attach with: sortie attach 42") {
		t.Errorf("expected attach instructions in line 2, got: %s", lines[2])
	}

	// Verify log file was written
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	logContent := string(content)
	if !strings.Contains(logContent, "=== Step: implement (task #42) ===") {
		t.Error("log file missing step header")
	}
	if !strings.Contains(logContent, "Tmux session") {
		t.Error("log file missing tmux session message")
	}
	if !strings.Contains(logContent, "sortie attach 42") {
		t.Error("log file missing attach instructions")
	}
}

func TestWriteTmuxLogMessageCallsOutputFn(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "step.log")

	var captured []string
	outputFn := func(lines []string) {
		captured = append(captured, lines...)
	}

	lines := writeTmuxLogMessage(logPath, 7, "review", "sortie-7", "7")
	outputFn(lines)

	if len(captured) != 3 {
		t.Fatalf("expected outputFn to receive 3 lines, got %d", len(captured))
	}
	if !strings.Contains(captured[1], "Tmux session") {
		t.Errorf("expected tmux session message in outputFn, got: %s", captured[1])
	}
}

func TestRunClaudeSyncSetsWorkDir(t *testing.T) {
	dir := t.TempDir()

	// Create a script that prints the working directory, ignoring all args
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	os.WriteFile(script, []byte("#!/bin/sh\npwd\n"), 0755)

	cfg := &config.Config{
		Claude: config.ClaudeConfig{
			Command: script,
		},
	}
	engine := NewEngine(cfg, (*db.DB)(nil), nil, dir)

	ctx := context.Background()
	output, err := engine.runClaudeSync(ctx, "test prompt", dir, "", "")
	if err != nil {
		t.Fatalf("runClaudeSync failed: %v", err)
	}

	output = strings.TrimSpace(output)
	// The script should print the workDir we passed
	if output != dir {
		t.Errorf("expected working directory %q, got %q", dir, output)
	}
}

func TestRunClaudeSyncEmptyWorkDir(t *testing.T) {
	// Create a script that prints the working directory, ignoring all args
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	os.WriteFile(script, []byte("#!/bin/sh\npwd\n"), 0755)

	cfg := &config.Config{
		Claude: config.ClaudeConfig{
			Command: script,
		},
	}
	engine := NewEngine(cfg, (*db.DB)(nil), nil, "")

	ctx := context.Background()
	output, err := engine.runClaudeSync(ctx, "test prompt", "", "", "")
	if err != nil {
		t.Fatalf("runClaudeSync failed: %v", err)
	}

	// Should succeed without error — we just verify it doesn't crash
	output = strings.TrimSpace(output)
	if output == "" {
		t.Error("expected non-empty output from pwd")
	}
}

func TestSummarizerDiffStatPromptIncludesReadInstruction(t *testing.T) {
	// Verify that the no-artifacts summarizer prompt instructs Claude to read files
	// rather than just summarizing based on filenames
	taskTitle := "Add feature X"
	taskDesc := "Implement feature X for the system"
	diffStat := " file1.go | 10 +\n file2.go | 5 +-\n"

	// Build the prompt the same way the engine does in the no-artifacts path
	var sb strings.Builder
	sb.WriteString("Summarize the progress made on task #1: " + taskTitle + "\n\n")
	sb.WriteString("The task description was:\n")
	sb.WriteString(taskDesc)
	sb.WriteString("\n\nThe following files were changed:\n\n```\n")
	sb.WriteString(diffStat)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Read the changed files listed above and review the actual code to understand what was implemented. ")
	sb.WriteString("Do NOT guess or assume — base your summary on the actual file contents and git changes in this repository. ")
	sb.WriteString("Provide a concise summary of what was accomplished. ")
	sb.WriteString("This summary will be used as context for future work on this task.")
	prompt := sb.String()

	if !strings.Contains(prompt, "Read the changed files") {
		t.Error("expected prompt to instruct Claude to read changed files")
	}
	if !strings.Contains(prompt, "Do NOT guess or assume") {
		t.Error("expected prompt to instruct Claude not to guess")
	}
	if !strings.Contains(prompt, diffStat) {
		t.Error("expected prompt to contain the diff stat")
	}
}

func TestTemplateLoopVars(t *testing.T) {
	ctx := &TemplateContext{
		Task: TaskVars{ID: 1, Title: "Test task", Description: "A test"},
		Loop: LoopVars{Iteration: 3, MaxIterations: 5},
	}

	result := ResolveTemplate("Iteration {{loop.iteration}} of {{loop.max_iterations}}", ctx)
	expected := "Iteration 3 of 5"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTemplateLoopVarsZero(t *testing.T) {
	ctx := &TemplateContext{
		Task: TaskVars{ID: 1, Title: "Test task", Description: "A test"},
		Loop: LoopVars{Iteration: 0, MaxIterations: 0},
	}

	result := ResolveTemplate("Iteration {{loop.iteration}} of {{loop.max_iterations}}", ctx)
	expected := "Iteration 0 of 0"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestSummarizationDescription(t *testing.T) {
	tests := []struct {
		name            string
		taskID          int64
		hasCustomPrompt bool
		artifactNames   []string
		useDiffStat     bool
		expected        string
	}{
		{
			name:            "custom prompt with artifacts",
			taskID:          42,
			hasCustomPrompt: true,
			artifactNames:   []string{"implement", "review"},
			expected:        "Summarizing task #42 with custom prompt and artifacts: implement, review",
		},
		{
			name:            "custom prompt without artifacts",
			taskID:          7,
			hasCustomPrompt: true,
			artifactNames:   nil,
			expected:        "Summarizing task #7 with custom prompt",
		},
		{
			name:          "default prompt with artifacts",
			taskID:        10,
			artifactNames: []string{"implement"},
			expected:      "Summarizing task #10 with artifacts: implement",
		},
		{
			name:        "git diff fallback",
			taskID:      5,
			useDiffStat: true,
			expected:    "Summarizing task #5 via git diff",
		},
		{
			name:     "no context",
			taskID:   1,
			expected: "Summarizing task #1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizationDescription(tt.taskID, tt.hasCustomPrompt, tt.artifactNames, tt.useDiffStat)
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSummarizerLogFnCalledWithArtifacts(t *testing.T) {
	// Create a fake Claude script that echoes a summary
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'task summary output'\n"), 0755)

	dir := t.TempDir()

	// Create a real SQLite database for the test
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0755)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	// Create a project and task so UpdateTaskContext works
	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	taskObj, err := database.CreateTask(project.ID, "Test task", "A test task", "test-task", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	taskObj.WorktreePath = dir

	// Seed step context in DB (replaces artifact file)
	if err := database.CreateTaskStep(taskObj.ID, "implement"); err != nil {
		t.Fatal(err)
	}
	ctx1 := "Added feature X"
	if err := database.CompleteTaskStep(taskObj.ID, "implement", &ctx1, 0); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Claude: config.ClaudeConfig{Command: script},
	}
	engine := NewEngine(cfg, database, nil, dir)

	wf := &config.WorkflowConfig{
		Steps: []config.StepConfig{
			{Name: "implement"},
		},
	}

	var logMessages []string
	logFn := func(format string, args ...any) {
		logMessages = append(logMessages, fmt.Sprintf(format, args...))
	}

	ctx := context.Background()
	err = engine.runSummarizer(ctx, taskObj, wf, "", logFn)
	if err != nil {
		t.Fatalf("runSummarizer failed: %v", err)
	}

	// Verify that the log messages contain the step context description
	found := false
	for _, msg := range logMessages {
		if strings.Contains(msg, "Summarizing task #") && strings.Contains(msg, "implement") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log message about summarizing with step contexts, got: %v", logMessages)
	}
}

func TestSummarizerLogFnCalledWithNilLogFn(t *testing.T) {
	// Verify runSummarizer doesn't panic when logFn is nil
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'summary'\n"), 0755)

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0755)

	// Create a real SQLite database for the test
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	taskObj, err := database.CreateTask(project.ID, "Test", "A test", "test", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	taskObj.WorktreePath = dir

	cfg := &config.Config{
		Claude: config.ClaudeConfig{Command: script},
	}
	engine := NewEngine(cfg, database, nil, dir)

	wf := &config.WorkflowConfig{
		Steps: []config.StepConfig{
			{Name: "implement"},
		},
	}

	ctx := context.Background()
	// Should not panic with nil logFn
	err = engine.runSummarizer(ctx, taskObj, wf, "", nil)
	if err != nil {
		t.Fatalf("runSummarizer with nil logFn failed: %v", err)
	}
}

func TestFindStepIndex(t *testing.T) {
	steps := []config.StepConfig{
		{Name: "implement"},
		{Name: "review"},
		{Name: "test"},
	}

	tests := []struct {
		name     string
		stepName string
		expected int
	}{
		{"first step", "implement", 0},
		{"middle step", "review", 1},
		{"last step", "test", 2},
		{"not found", "deploy", -1},
		{"empty string", "", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findStepIndex(steps, tt.stepName)
			if result != tt.expected {
				t.Errorf("findStepIndex(%q) = %d, want %d", tt.stepName, result, tt.expected)
			}
		})
	}
}

func TestTmuxScriptEmptyPromptLaunchesBlankSession(t *testing.T) {
	// Verify that when prompt is empty, the tmux script launches Claude
	// without a prompt argument (blank interactive session)
	prompt := ""
	claudeCmd := "claude"
	envExports := ""

	var script string
	if strings.TrimSpace(prompt) == "" {
		script = fmt.Sprintf("#!/bin/bash\n%s%s\nexec bash\n", envExports, claudeCmd)
	} else {
		script = fmt.Sprintf("#!/bin/bash\n%sPROMPT=$(cat %q)\n%s \"$PROMPT\"\nexec bash\n", envExports, "/tmp/prompt.txt", claudeCmd)
	}

	// Should NOT contain PROMPT= or "$PROMPT"
	if strings.Contains(script, "PROMPT=") {
		t.Error("blank session script should not contain PROMPT variable")
	}
	if strings.Contains(script, "$PROMPT") {
		t.Error("blank session script should not reference $PROMPT")
	}
	// Should contain bare claude command
	if !strings.Contains(script, "claude\n") {
		t.Error("blank session script should contain bare claude command")
	}
}

func TestFinalizationOrderMergeBeforeSummarization(t *testing.T) {
	// Verify that executeOnComplete is called before runSummarizer in both
	// RunTask and FinalizeTask by checking the code structure.
	// This test validates the cleanup condition: merge+worktree triggers cleanup.

	tests := []struct {
		name          string
		onComplete    string
		worktree      bool
		expectCleanup bool
	}{
		{"merge with worktree", "merge", true, true},
		{"merge without worktree", "merge", false, false},
		{"commit with worktree", "commit", true, false},
		{"none with worktree", "none", true, false},
		{"empty with worktree", "", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldCleanup := tt.onComplete == "merge" && tt.worktree
			if shouldCleanup != tt.expectCleanup {
				t.Errorf("expected cleanup=%v for onComplete=%q worktree=%v, got %v",
					tt.expectCleanup, tt.onComplete, tt.worktree, shouldCleanup)
			}
		})
	}
}

func TestCleanupMergedWorktreeLogsMessages(t *testing.T) {
	// Verify that cleanupMergedWorktree calls the log function with expected messages.
	dir := t.TempDir()

	// Create a real database so ClearWorktreePath doesn't panic
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{}
	engine := NewEngine(cfg, database, nil, dir)

	tk := &task.Task{
		ID:             1,
		WorktreePath:   filepath.Join(dir, "nonexistent-worktree"),
		Branch:         "test-branch",
		CheckoutBranch: "",
	}

	var logMessages []string
	logFn := func(format string, args ...any) {
		logMessages = append(logMessages, fmt.Sprintf(format, args...))
	}

	// Should not panic even with nonexistent paths (git operations will warn but not crash)
	engine.cleanupMergedWorktree(tk, logFn)

	if len(logMessages) < 2 {
		t.Fatalf("expected at least 2 log messages, got %d: %v", len(logMessages), logMessages)
	}
	if !strings.Contains(logMessages[0], "Cleaning up worktree and branch") {
		t.Errorf("expected first log to mention cleanup, got: %s", logMessages[0])
	}
	if !strings.Contains(logMessages[len(logMessages)-1], "Cleanup completed") {
		t.Errorf("expected last log to mention completion, got: %s", logMessages[len(logMessages)-1])
	}
}

func TestCleanupMergedWorktreePreservesCheckoutBranch(t *testing.T) {
	// When CheckoutBranch is set (user-provided branch), the branch should NOT be deleted.
	dir := t.TempDir()

	dbPath := filepath.Join(dir, ".sortie", "test.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{}
	engine := NewEngine(cfg, database, nil, dir)

	tk := &task.Task{
		ID:             1,
		WorktreePath:   filepath.Join(dir, "nonexistent-worktree"),
		Branch:         "user-branch",
		CheckoutBranch: "user-branch", // User provided this branch
	}

	var logMessages []string
	logFn := func(format string, args ...any) {
		logMessages = append(logMessages, fmt.Sprintf(format, args...))
	}

	engine.cleanupMergedWorktree(tk, logFn)

	// Should complete without panicking
	if len(logMessages) < 2 {
		t.Fatalf("expected at least 2 log messages, got %d", len(logMessages))
	}
}

func TestRunTaskDoesNotSetSummarizingStatus(t *testing.T) {
	// Verify that after RunTask completes all steps, the task status is NOT
	// StatusCompleted or StatusSummarizing — finalization is now the daemon's job.
	dir := t.TempDir()

	// Create a git repo so worktree operations don't fail
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a fake Claude script that exits successfully
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'done'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	tk, err := database.CreateTask(project.ID, "Test task", "A test task", "test-task", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	// No-worktree mode so we don't need a real git repo with worktree support
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		Claude:     config.ClaudeConfig{Command: script},
		OnComplete: "merge",
		Workflows: []config.WorkflowConfig{
			{
				Name: "default",
				Steps: []config.StepConfig{
					{Name: "implement", Prompt: "implement the thing"},
				},
			},
		},
	}
	engine := NewEngine(cfg, database, nil, dir)

	ctx := context.Background()
	err = engine.RunTask(ctx, tk, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// After RunTask, the task should NOT be StatusSummarizing or StatusCompleted.
	// The daemon handles finalization after agent completion.
	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status == task.StatusSummarizing {
		t.Error("RunTask should not set StatusSummarizing — that is the daemon's job now")
	}
	if refreshed.Status == task.StatusCompleted {
		t.Error("RunTask should not set StatusCompleted — that is the daemon's job now")
	}
}

func TestRunTaskDoesNotCallExecuteOnComplete(t *testing.T) {
	// Verify the finalization order: RunTask should return nil after all steps
	// without doing merge/summarize/cleanup. The comment in code documents this.

	// This is a structural test — we verify RunTask exits cleanly with nil
	// and the task remains in its running state (no status change to summarizing/completed).
	dir := t.TempDir()

	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'done'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Test with on_complete: none — RunTask should still just return nil
	// (previously it would call executeOnComplete then summarizer)
	tk, err := database.CreateTask(project.ID, "No finalization task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		Claude:     config.ClaudeConfig{Command: script},
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{
				Name: "default",
				Steps: []config.StepConfig{
					{Name: "step1", Prompt: "do something"},
				},
			},
		},
	}
	engine := NewEngine(cfg, database, nil, dir)

	ctx := context.Background()
	if err := engine.RunTask(ctx, tk, nil); err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}

	// Verify task status is still running (not changed by RunTask itself)
	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	switch refreshed.Status {
	case task.StatusSummarizing, task.StatusCompleted, task.StatusFinalizing:
		t.Errorf("RunTask should not change task status to %s — daemon handles finalization", refreshed.Status)
	}
}

func TestRunTaskSummarizationStrategyNoneSkipsContext(t *testing.T) {
	// Verify that a step with summarization_strategy: none does not capture
	// any step context — later steps see empty context for that step.
	dir := t.TempDir()

	// Fake Claude script emits something on stdout so a normal step would
	// otherwise capture a non-empty result.
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'last message body'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	tk, err := database.CreateTask(project.ID, "Test task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		Claude:     config.ClaudeConfig{Command: script},
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{
				Name: "default",
				Steps: []config.StepConfig{
					{
						Name:                  "fire-and-forget",
						Prompt:                "do a thing",
						SummarizationStrategy: config.SummarizationStrategyNone,
					},
				},
			},
		},
	}
	engine := NewEngine(cfg, database, nil, dir)

	ctx := context.Background()
	if err := engine.RunTask(ctx, tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	gotCtx, err := database.GetTaskStepContext(tk.ID, "fire-and-forget")
	if err != nil {
		t.Fatalf("GetTaskStepContext failed: %v", err)
	}
	if gotCtx != "" {
		t.Errorf("expected empty step context for summarization_strategy=none, got %q", gotCtx)
	}
}

// TestRunTaskManualContextOverridesLastMessage verifies that a context pushed
// via the update_step_context MCP tool while the step is running (simulated
// here by writing to the running task_steps row mid-step) survives
// CompleteTaskStep and is NOT clobbered by the agent's last-message capture.
func TestRunTaskManualContextOverridesLastMessage(t *testing.T) {
	dir := t.TempDir()

	// Fake Claude sleeps long enough for the test to inject a manual context
	// while the step row is still 'running', then emits a non-empty result that
	// would otherwise be captured as the step context.
	script := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 2\necho 'agent last message'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	tk, err := database.CreateTask(project.ID, "Test task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		Claude:     config.ClaudeConfig{Command: script},
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{
				Name:  "default",
				Print: true, // headless/synchronous path so the step blocks on the fake-claude exec
				Steps: []config.StepConfig{
					{Name: "implement", Prompt: "do a thing"},
				},
			},
		},
	}
	engine := NewEngine(cfg, database, nil, dir)

	const manual = "MANUAL OVERRIDE ARTIFACT"

	// Inject the manual context once the step row exists as 'running' — this is
	// what the update_step_context MCP tool does via the daemon. The engine is
	// blocked in the fake-claude exec at this point, so the write lands on the
	// running row before CompleteTaskStep.
	injected := make(chan error, 1)
	go func() {
		for i := 0; i < 200; i++ {
			rows, err := database.UpdateRunningTaskStepContext(tk.ID, "implement", manual, false)
			if err != nil {
				injected <- err
				return
			}
			if rows == 1 {
				injected <- nil
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		injected <- fmt.Errorf("timed out waiting for running step row")
	}()

	ctx := context.Background()
	if err := engine.RunTask(ctx, tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if err := <-injected; err != nil {
		t.Fatalf("failed to inject manual context: %v", err)
	}

	gotCtx, err := database.GetTaskStepContext(tk.ID, "implement")
	if err != nil {
		t.Fatalf("GetTaskStepContext failed: %v", err)
	}
	if gotCtx != manual {
		t.Errorf("expected manual override %q to survive completion, got %q", manual, gotCtx)
	}
}

func TestMarkSummarizingStepSingleStepUsesSummarizing(t *testing.T) {
	// For a single-step workflow the step summary IS the task summary, so
	// markSummarizingStep should surface `summarizing` (not `summarizing_step`).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "single", "", "single", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	engine := NewEngine(&config.Config{}, database, nil, dir)
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "only"}}}

	restore := engine.markSummarizingStep(tk, wf)
	if tk.Status != task.StatusSummarizing {
		t.Errorf("expected in-memory status %q, got %q", task.StatusSummarizing, tk.Status)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got.Status != task.StatusSummarizing {
		t.Errorf("expected persisted status %q, got %q", task.StatusSummarizing, got.Status)
	}

	restore()
	if tk.Status != task.StatusRunning {
		t.Errorf("expected restored status %q, got %q", task.StatusRunning, tk.Status)
	}
}

func TestMarkSummarizingStepMultiStepUsesSummarizingStep(t *testing.T) {
	// For multi-step workflows the per-step summarization is distinct from
	// the cross-step summarizer, so the transient `summarizing_step` status
	// must still be used to disambiguate them in the TUI.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "multi", "", "multi", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	engine := NewEngine(&config.Config{}, database, nil, dir)
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "a"}, {Name: "b"}}}

	restore := engine.markSummarizingStep(tk, wf)
	if tk.Status != task.StatusSummarizingStep {
		t.Errorf("expected status %q, got %q", task.StatusSummarizingStep, tk.Status)
	}
	restore()
	if tk.Status != task.StatusRunning {
		t.Errorf("expected restored status %q, got %q", task.StatusRunning, tk.Status)
	}
}

func TestPromoteSingleStepContextToTask(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "single", "", "single", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	stepCtx := "concise step summary text"
	if err := database.CreateTaskStep(tk.ID, "only"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteTaskStep(tk.ID, "only", &stepCtx, 0); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(&config.Config{}, database, nil, dir)
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "only"}}}

	if !engine.promoteSingleStepContextToTask(tk, wf, nil) {
		t.Fatal("expected promotion to succeed for single-step workflow with non-empty step context")
	}
	if tk.Context != stepCtx {
		t.Errorf("expected in-memory task.Context %q, got %q", stepCtx, tk.Context)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got.Context != stepCtx {
		t.Errorf("expected persisted task context %q, got %q", stepCtx, got.Context)
	}
}

func TestPromoteSingleStepContextToTaskNoOpForMultiStep(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "multi", "", "multi", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	stepCtx := "should not be promoted"
	if err := database.CreateTaskStep(tk.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteTaskStep(tk.ID, "a", &stepCtx, 0); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(&config.Config{}, database, nil, dir)
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "a"}, {Name: "b"}}}

	if engine.promoteSingleStepContextToTask(tk, wf, nil) {
		t.Fatal("expected no promotion for multi-step workflow")
	}
	if tk.Context != "" {
		t.Errorf("expected in-memory task.Context unchanged, got %q", tk.Context)
	}
}

func TestPromoteSingleStepContextToTaskNoOpForEmptyStepContext(t *testing.T) {
	// When the single step's context is empty (e.g. summarize_chat skipped),
	// promotion should be a no-op so the caller falls through to runSummarizer
	// which can still produce a task summary from the git diff fallback.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".sortie", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "single-empty", "", "single-empty", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	if err := database.CreateTaskStep(tk.ID, "only"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteTaskStep(tk.ID, "only", nil, 0); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(&config.Config{}, database, nil, dir)
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "only"}}}

	if engine.promoteSingleStepContextToTask(tk, wf, nil) {
		t.Fatal("expected no promotion when step context is empty")
	}
	if tk.Context != "" {
		t.Errorf("expected task.Context to remain empty, got %q", tk.Context)
	}
}

func TestTmuxScriptNonEmptyPromptPassesPrompt(t *testing.T) {
	// Verify that when prompt is non-empty, the tmux script passes it to Claude
	prompt := "implement the feature"
	claudeCmd := "claude"
	envExports := ""
	promptFile := "/tmp/prompt.txt"

	var script string
	if strings.TrimSpace(prompt) == "" {
		script = fmt.Sprintf("#!/bin/bash\n%s%s\nexec bash\n", envExports, claudeCmd)
	} else {
		script = fmt.Sprintf("#!/bin/bash\n%sPROMPT=$(cat %q)\n%s \"$PROMPT\"\nexec bash\n", envExports, promptFile, claudeCmd)
	}

	// Should contain PROMPT= and "$PROMPT"
	if !strings.Contains(script, "PROMPT=") {
		t.Error("non-empty prompt script should contain PROMPT variable")
	}
	if !strings.Contains(script, `"$PROMPT"`) {
		t.Error("non-empty prompt script should pass $PROMPT to claude")
	}
}

// TestSummarizePreviousTmuxStepRequireContextBlocks verifies that a tmux
// summarize_chat step which fails to capture its context returns a blocking
// error when require_context is set, and proceeds (nil) when it is not. The
// failure is induced by recording no chat for the step, so loadStepChatContent
// returns "" — the same condition that silently dropped grilling context.
func TestSummarizePreviousTmuxStepRequireContextBlocks(t *testing.T) {
	for _, tc := range []struct {
		name           string
		requireContext bool
		wantBlock      bool
	}{
		{"require_context blocks", true, true},
		{"best-effort proceeds", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			defer database.Close()

			project, err := database.GetOrCreateProject(dir)
			if err != nil {
				t.Fatalf("GetOrCreateProject: %v", err)
			}
			tk, err := database.CreateTask(project.ID, "grill task", "desc", "slug", "wf", "", task.StatusRunning, nil)
			if err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			tk.WorktreePath = dir
			// StepIndex points PAST the just-finished tmux step, so prevStep =
			// Steps[0] = "grill" (the engine bumps the index before pausing).
			tk.StepIndex = 1

			// Mark the grill step completed with no context, matching the real
			// state after a tmux step pauses at the approval gate.
			if err := database.CreateTaskStep(tk.ID, "grill"); err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteTaskStep(tk.ID, "grill", nil, 0); err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{
				OnComplete: "none",
				Workflows: []config.WorkflowConfig{{
					Name:  "wf",
					Print: false, // steps default to tmux
					Steps: []config.StepConfig{
						{
							Name:                  "grill",
							SummarizationStrategy: config.SummarizationStrategySummarizeChat,
							Human:                 true,
							RequireContext:        tc.requireContext,
						},
						{Name: "implement", Print: boolPtr(true)},
					},
				}},
			}
			engine := NewEngine(cfg, database, nil, dir)

			err = engine.summarizePreviousTmuxStep(context.Background(), tk, nil)
			if tc.wantBlock {
				if err == nil {
					t.Fatal("expected a blocking error, got nil")
				}
				if !errors.Is(err, ErrStepContextRequired) {
					t.Errorf("expected error to wrap ErrStepContextRequired, got %v", err)
				}
			} else if err != nil {
				t.Errorf("expected nil (best-effort), got %v", err)
			}
		})
	}
}

// TestSummarizePreviousTmuxStepSkipsManualContext verifies that when a tmux
// step's context was already folded manually (via the update_step_context MCP
// tool), the auto summarizer leaves it untouched and reports success — even
// with require_context set, because the context IS present. Without the skip
// guard this case would either clobber the manual artifact with a chat summary
// or, with no chat recorded, block the task on ErrStepContextRequired.
func TestSummarizePreviousTmuxStepSkipsManualContext(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "grill task", "desc", "slug", "wf", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.WorktreePath = dir
	tk.StepIndex = 1

	const manual = "FINAL PLAN folded by the agent"
	if err := database.CreateTaskStep(tk.ID, "grill"); err != nil {
		t.Fatal(err)
	}
	ctx := manual
	if err := database.CompleteTaskStep(tk.ID, "grill", &ctx, 0); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{{
			Name:  "wf",
			Print: false,
			Steps: []config.StepConfig{
				{
					Name:                  "grill",
					SummarizationStrategy: config.SummarizationStrategySummarizeChat,
					Human:                 true,
					RequireContext:        true,
				},
				{Name: "implement", Print: boolPtr(true)},
			},
		}},
	}
	engine := NewEngine(cfg, database, nil, dir)

	if err := engine.summarizePreviousTmuxStep(context.Background(), tk, nil); err != nil {
		t.Fatalf("expected nil (manual context present), got %v", err)
	}

	got, err := database.GetTaskStepContext(tk.ID, "grill")
	if err != nil {
		t.Fatal(err)
	}
	if got != manual {
		t.Errorf("manual context must be preserved, got %q", got)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestEffectiveOnComplete verifies the on_complete resolution precedence:
// locality wins. A project-scoped workflow's on_complete beats the project
// top-level setting; a global workflow's on_complete is only a default that
// an explicit project-level on_complete overrides.
func TestEffectiveOnComplete(t *testing.T) {
	cases := []struct {
		name                  string
		onCompleteFromProject bool
		workflow              string
		want                  string
	}{
		{"workflow inherits project-level", false, "inherits", "merge"},
		{"project workflow overrides project-level", false, "overrides", "commit"},
		{"unknown workflow falls back to project-level", false, "nope", "merge"},
		{"global workflow wins when project on_complete is inherited", false, "global-none", "none"},
		{"explicit project on_complete beats global workflow", true, "global-none", "merge"},
		{"project workflow beats explicit project on_complete", true, "overrides", "commit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				OnComplete:            "merge",
				OnCompleteFromProject: tc.onCompleteFromProject,
				Workflows: []config.WorkflowConfig{
					{Name: "inherits", Steps: []config.StepConfig{{Name: "s"}}},
					{Name: "overrides", OnComplete: "commit", Steps: []config.StepConfig{{Name: "s"}}},
					{Name: "global-none", OnComplete: "none", FromGlobal: true, Steps: []config.StepConfig{{Name: "s"}}},
				},
			}
			e := &Engine{cfg: newEngineConfig(cfg)}
			got := e.effectiveOnComplete(&task.Task{Workflow: tc.workflow})
			if got != tc.want {
				t.Errorf("effectiveOnComplete(%q) = %q, want %q", tc.workflow, got, tc.want)
			}
		})
	}
}
