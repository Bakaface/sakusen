package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// routineTestModel builds a list-view model whose config declares the given
// routines against a single workflow, and whose client is the given fake.
func routineTestModel(t *testing.T, svc TaskService, workflows []config.WorkflowConfig, routines []config.RoutineConfig) Model {
	t.Helper()
	return Model{
		keys:        newKeyMap(),
		client:      svc,
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, "", ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg:         &config.Config{Workflows: workflows, Routines: routines},
	}
}

var routineTestWorkflows = []config.WorkflowConfig{
	{Name: "wiki-compose", Steps: []config.StepConfig{{Name: "compose", Prompt: "compose it"}}},
	{Name: "digest", Steps: []config.StepConfig{{Name: "run", Prompt: "work on {{task.input}}"}}},
}

func TestMatchRunRoutine(t *testing.T) {
	tests := []struct {
		input    string
		wantArgs string
		wantOK   bool
	}{
		{"RunRoutine", "", true},
		{"RunRoutine compose-wiki", "compose-wiki", true},
		{"RunRoutine  compose-wiki ", "compose-wiki", true},
		{"RunTask compose-wiki", "", false},
		{"Run", "", false},
	}
	for _, tt := range tests {
		args, ok := matchRunRoutine(tt.input)
		if ok != tt.wantOK {
			t.Errorf("matchRunRoutine(%q): ok = %v, want %v", tt.input, ok, tt.wantOK)
		}
		if args != tt.wantArgs {
			t.Errorf("matchRunRoutine(%q): args = %q, want %q", tt.input, args, tt.wantArgs)
		}
	}
}

func TestExecRunRoutine_ByNameFires(t *testing.T) {
	var gotName, gotInput string
	fake := &fakeTaskService{
		runRoutine: func(_, name, input string) (*daemon.TaskInfo, error) {
			gotName, gotInput = name, input
			return &daemon.TaskInfo{ID: 7}, nil
		},
		listRoutines: func(string) ([]daemon.PeriodicInfo, error) { return nil, nil },
	}
	m := routineTestModel(t, fake, routineTestWorkflows, []config.RoutineConfig{
		{Name: "compose-wiki", Workflow: "wiki-compose"},
	})

	result, cmd := execRunRoutine(m, "compose-wiki")
	updated := result.(Model)
	if updated.err != nil {
		t.Fatalf("unexpected error: %v", updated.err)
	}
	if cmd == nil {
		t.Fatal("expected a command firing the routine")
	}
	cmd()
	if gotName != "compose-wiki" || gotInput != "" {
		t.Errorf("ran (%q, %q), want (compose-wiki, \"\")", gotName, gotInput)
	}
}

func TestExecRunRoutine_EmptyArgsOpensPicker(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "compose-wiki", Workflow: "wiki-compose", Description: "Rebuild the wiki"},
	})

	updated := mustModel(execRunRoutine(m, ""))
	if updated.selector.kind != selectorRoutine {
		t.Fatalf("expected a selectorRoutine picker, got kind=%d", updated.selector.kind)
	}
	if len(updated.selector.items) != 1 || updated.selector.items[0] != "compose-wiki" {
		t.Errorf("picker items = %v", updated.selector.items)
	}
	if len(updated.selector.descriptions) != 1 || updated.selector.descriptions[0] != "Rebuild the wiki" {
		t.Errorf("picker descriptions = %v", updated.selector.descriptions)
	}
}

func TestExecRunRoutine_UnknownName(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "compose-wiki", Workflow: "wiki-compose"},
	})

	updated := mustModel(execRunRoutine(m, "nope"))
	if updated.err == nil || !strings.Contains(updated.err.Error(), "unknown routine") {
		t.Errorf("expected an unknown-routine error, got %v", updated.err)
	}
}

// A routine whose workflow needs an input it doesn't supply asks for it in the
// prompt's input-only mode; submitting runs the routine with the typed text.
func TestLaunchRoutine_RequiresInputOpensPrompt(t *testing.T) {
	var gotName, gotInput string
	fake := &fakeTaskService{
		runRoutine: func(_, name, input string) (*daemon.TaskInfo, error) {
			gotName, gotInput = name, input
			return &daemon.TaskInfo{ID: 8}, nil
		},
		listRoutines: func(string) ([]daemon.PeriodicInfo, error) { return nil, nil },
	}
	m := routineTestModel(t, fake, routineTestWorkflows, []config.RoutineConfig{
		{Name: "digest-now", Workflow: "digest"},
	})

	updated := mustModel(execRunRoutine(m, "digest-now"))
	if updated.view != viewPrompt {
		t.Fatalf("expected the input prompt to open, got view=%d", updated.view)
	}
	if updated.prompt.routineName != "digest-now" {
		t.Fatalf("prompt.routineName = %q", updated.prompt.routineName)
	}
	if fields := updated.prompt.visibleFields(); len(fields) != 1 || fields[0] != promptFieldInput {
		t.Errorf("expected an input-only form, got fields %v", fields)
	}

	updated.prompt.textarea.SetValue("the section")
	result, cmd := updated.handlePromptSubmit()
	submitted := result.(Model)
	if submitted.view != viewList {
		t.Errorf("expected a return to the list view, got %d", submitted.view)
	}
	if cmd == nil {
		t.Fatal("expected a command running the routine")
	}
	cmd()
	if gotName != "digest-now" || gotInput != "the section" {
		t.Errorf("ran (%q, %q), want (digest-now, \"the section\")", gotName, gotInput)
	}
}

func TestPromptSubmit_RoutineRejectsEmptyInput(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "digest-now", Workflow: "digest"},
	})
	m = mustModel(execRunRoutine(m, "digest-now"))

	result, cmd := m.handlePromptSubmit()
	updated := result.(Model)
	if cmd != nil {
		t.Error("expected no command for an empty routine input")
	}
	if updated.prompt.validationError == "" {
		t.Error("expected a validation error for an empty routine input")
	}
}

func TestCompleteRunRoutine(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "compose-wiki", Workflow: "wiki-compose"},
		{Name: "digest-now", Workflow: "digest"},
	})

	if got, ok := completeRunRoutine(m, "RunRoutine"); !ok || got != "RunRoutine " {
		t.Errorf("completeRunRoutine(bare) = %q, %v", got, ok)
	}
	if got, ok := completeRunRoutine(m, "RunRoutine comp"); !ok || got != "RunRoutine compose-wiki" {
		t.Errorf("completeRunRoutine(prefix) = %q, %v", got, ok)
	}
	if _, ok := completeRunRoutine(m, "RunTask comp"); ok {
		t.Error("completeRunRoutine must not complete RunTask input")
	}
}

// mustModel unwraps an exec function's (tea.Model, tea.Cmd) pair into the
// concrete Model, so a call can be written inline.
func mustModel(result tea.Model, _ tea.Cmd) Model {
	return result.(Model)
}

// Cancelling the routine input prompt leaves no routine mode behind for the
// next New Task screen.
func TestRoutinePrompt_CancelClearsMode(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "digest-now", Workflow: "digest"},
	})
	m = mustModel(execRunRoutine(m, "digest-now"))

	result, _ := m.handlePromptKey(tea.KeyMsg{Type: tea.KeyEsc})
	updated := result.(Model)
	if updated.prompt.routineName != "" {
		t.Errorf("routineName = %q after cancel, want empty", updated.prompt.routineName)
	}
	if updated.view != viewList {
		t.Errorf("view = %d after cancel, want the list view", updated.view)
	}
}

// The input-only prompt renders just the argument box: no title/slug rows, no
// git frame, no workflow pane — and no line overflows the terminal.
func TestRenderRoutineInputPrompt(t *testing.T) {
	m := routineTestModel(t, &fakeTaskService{}, routineTestWorkflows, []config.RoutineConfig{
		{Name: "digest-now", Workflow: "digest"},
	})
	m.width, m.height = 100, 30
	m = mustModel(execRunRoutine(m, "digest-now"))
	m.prompt.SetSize(m.width, m.height)

	out := m.prompt.View()
	if !strings.Contains(out, "Run Routine: digest-now") {
		t.Errorf("missing title, got:\n%s", out)
	}
	for _, absent := range []string{"Title:", "Slug:", "Worktree:", "Branch:", "Target:"} {
		if strings.Contains(out, absent) {
			t.Errorf("input-only prompt must not render %q, got:\n%s", absent, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line exceeds width %d: %d (%q)", m.width, w, line)
		}
	}
}
