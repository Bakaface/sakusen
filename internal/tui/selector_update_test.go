package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

// --- selectorWorkflow: navigation and selection ---

func TestHandleWorkflowSelectKey_Navigation(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 0,
			items:  []string{"implement", "review", "test"},
		},
		projectPath: "/tmp/test",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement", Description: "Desc A"},
				{Name: "review", Description: "Desc B"},
				{Name: "test", Description: "Desc C"},
			},
		},
	}

	// Move down
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1 after j, got %d", updated.selector.cursor)
	}

	// Move down again
	m = updated
	result, _ = m.handleSelectorKey(msg)
	updated = result.(Model)
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor at 2 after j, got %d", updated.selector.cursor)
	}

	// Move down at bottom — should stay
	m = updated
	result, _ = m.handleSelectorKey(msg)
	updated = result.(Model)
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", updated.selector.cursor)
	}

	// Move up
	m = updated
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	result, _ = m.handleSelectorKey(msg)
	updated = result.(Model)
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1 after k, got %d", updated.selector.cursor)
	}
}

func TestHandleWorkflowSelectKey_EscCancels(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 1,
			items:  []string{"implement"},
		},
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorWorkflow {
		t.Error("expected selector kind not to be selectorWorkflow after esc")
	}
}

func TestHandleWorkflowSelectKey_EnterOpensPromptOrCreates(t *testing.T) {
	// A non-fully-spec workflow → should open viewPrompt
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 0,
			items:  []string{"implement"},
		},
		projectPath: "/tmp/test",
		prompt:      newPromptView(true, branchModeNew, ""),
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement", Description: ""},
				// Not IsFullySpec: no Worktree, no Branch/Checkout, no Target
			},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorWorkflow {
		t.Error("expected selector kind not to be selectorWorkflow after enter")
	}
	// Non-fully-spec workflow → prompt opens
	if updated.view != viewPrompt {
		t.Errorf("expected viewPrompt after selecting non-fully-spec workflow, got %d", updated.view)
	}
}

func TestViewRendersWorkflowSelection(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:         selectorWorkflow,
			title:        "Run Task",
			cursor:       0,
			items:        []string{"implement", "review"},
			descriptions: []string{"Implement features", "Review code"},
		},
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement", Description: "Implement features"},
				{Name: "review", Description: "Review code"},
			},
		},
	}

	output := m.View()

	if !strings.Contains(output, "Run Task") {
		t.Error("expected workflow selection screen to contain title 'Run Task'")
	}
	if !strings.Contains(output, "implement") {
		t.Error("expected workflow selection screen to contain 'implement'")
	}
	if !strings.Contains(output, "review") {
		t.Error("expected workflow selection screen to contain 'review'")
	}
}

// --- selectorWorkflow: vim navigation (gg, G, ctrl+d, ctrl+u) ---

func TestWorkflowSelect_GGGoesToTop(t *testing.T) {
	cfg := &config.Config{
		Workflows: make([]config.WorkflowConfig, 10),
	}
	items := make([]string, 10)
	for i := range cfg.Workflows {
		cfg.Workflows[i] = config.WorkflowConfig{Name: fmt.Sprintf("wf-%d", i+1), Description: fmt.Sprintf("desc %d", i+1)}
		items[i] = cfg.Workflows[i].Name
	}
	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 7,
			items:  items,
		},
	}

	gMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	result, _ := m.handleSelectorKey(gMsg)
	m = result.(Model)
	result, _ = m.handleSelectorKey(gMsg)
	m = result.(Model)

	if m.selector.cursor != 0 {
		t.Errorf("expected cursor at 0 after 'gg', got %d", m.selector.cursor)
	}
}

func TestWorkflowSelect_ShiftGGoesToBottom(t *testing.T) {
	cfg := &config.Config{
		Workflows: make([]config.WorkflowConfig, 10),
	}
	items := make([]string, 10)
	for i := range cfg.Workflows {
		cfg.Workflows[i] = config.WorkflowConfig{Name: fmt.Sprintf("wf-%d", i+1)}
		items[i] = cfg.Workflows[i].Name
	}
	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 0,
			items:  items,
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.cursor != 9 {
		t.Errorf("expected cursor at 9 after 'G', got %d", updated.selector.cursor)
	}
}

func TestWorkflowSelect_CtrlDPageDown(t *testing.T) {
	cfg := &config.Config{
		Workflows: make([]config.WorkflowConfig, 10),
	}
	items := make([]string, 10)
	for i := range cfg.Workflows {
		cfg.Workflows[i] = config.WorkflowConfig{Name: fmt.Sprintf("wf-%d", i+1)}
		items[i] = cfg.Workflows[i].Name
	}
	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 0,
			items:  items,
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 10/2 = 5
	if updated.selector.cursor != 5 {
		t.Errorf("expected cursor at 5 after ctrl+d, got %d", updated.selector.cursor)
	}
}

func TestWorkflowSelect_CtrlUPageUp(t *testing.T) {
	cfg := &config.Config{
		Workflows: make([]config.WorkflowConfig, 10),
	}
	items := make([]string, 10)
	for i := range cfg.Workflows {
		cfg.Workflows[i] = config.WorkflowConfig{Name: fmt.Sprintf("wf-%d", i+1)}
		items[i] = cfg.Workflows[i].Name
	}
	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorWorkflow,
			cursor: 8,
			items:  items,
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlU}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 10/2 = 5
	if updated.selector.cursor != 3 {
		t.Errorf("expected cursor at 3 after ctrl+u, got %d", updated.selector.cursor)
	}
}

// --- selectorPriority: vim navigation (gg, G, ctrl+d, ctrl+u) ---

func TestPrioritySelect_GGGoesToTop(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			cursor: 3,
			items:  []string{"low", "medium", "high", "urgent"},
		},
	}

	gMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}

	result, _ := m.handleSelectorKey(gMsg)
	m = result.(Model)
	result, _ = m.handleSelectorKey(gMsg)
	m = result.(Model)

	if m.selector.cursor != 0 {
		t.Errorf("expected cursor at 0 after 'gg', got %d", m.selector.cursor)
	}
}

func TestPrioritySelect_ShiftGGoesToBottom(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			cursor: 0,
			items:  []string{"low", "medium", "high", "urgent"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.cursor != 3 {
		t.Errorf("expected cursor at 3 after 'G', got %d", updated.selector.cursor)
	}
}

func TestPrioritySelect_CtrlDPageDown(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			cursor: 0,
			items:  []string{"low", "medium", "high", "urgent"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 4/2 = 2
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor at 2 after ctrl+d, got %d", updated.selector.cursor)
	}
}

func TestPrioritySelect_CtrlUPageUp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			cursor: 3,
			items:  []string{"low", "medium", "high", "urgent"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlU}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 4/2 = 2
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1 after ctrl+u, got %d", updated.selector.cursor)
	}
}

// --- selectorArtifact: navigation ---

func TestArtifactSelect_GGGoesToTop(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorArtifact,
			cursor: 5,
			items:  []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"},
		},
	}

	gMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	result, _ := m.handleSelectorKey(gMsg)
	m = result.(Model)
	result, _ = m.handleSelectorKey(gMsg)
	m = result.(Model)

	if m.selector.cursor != 0 {
		t.Errorf("expected cursor at 0 after 'gg', got %d", m.selector.cursor)
	}
}

func TestArtifactSelect_ShiftGGoesToBottom(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorArtifact,
			cursor: 0,
			items:  []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.cursor != 7 {
		t.Errorf("expected cursor at 7 after 'G', got %d", updated.selector.cursor)
	}
}

func TestArtifactSelect_CtrlDPageDown(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorArtifact,
			cursor: 0,
			items:  []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 8/2 = 4
	if updated.selector.cursor != 4 {
		t.Errorf("expected cursor at 4 after ctrl+d, got %d", updated.selector.cursor)
	}
}

func TestArtifactSelect_CtrlUPageUp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorArtifact,
			cursor: 6,
			items:  []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlU}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	// half = 8/2 = 4
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor at 2 after ctrl+u, got %d", updated.selector.cursor)
	}
}

func TestHandleListKey_POpensPrioritySelection(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 20, Title: "Test task", Status: "pending", Priority: "medium"},
	})

	// Single "p" opens priority selection immediately
	pMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
	result, _ := m.handleListKey(pMsg)
	updated := result.(Model)

	if updated.selector.kind != selectorPriority {
		t.Error("expected selector kind to be selectorPriority after 'p'")
	}
	if updated.selector.taskID != 20 {
		t.Errorf("expected selector taskID to be 20, got %d", updated.selector.taskID)
	}
}

func TestHandlePrioritySelectKey_EscCancels(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			items:  []string{"low", "medium", "high", "urgent"},
			taskID: 1,
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorPriority {
		t.Error("expected selector kind not to be selectorPriority after esc")
	}
}

func TestHandlePrioritySelectKey_Navigation(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorPriority,
			cursor: 0,
			items:  []string{"low", "medium", "high", "urgent"},
			taskID: 1,
		},
	}

	// Move down
	downMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ := m.handleSelectorKey(downMsg)
	updated := result.(Model)
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", updated.selector.cursor)
	}

	// Move up
	upMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	result, _ = updated.handleSelectorKey(upMsg)
	updated = result.(Model)
	if updated.selector.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", updated.selector.cursor)
	}
}

func TestHandleArtifactSelectKey_Navigation(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		selector: selector{
			kind:   selectorArtifact,
			cursor: 0,
			items:  []string{"implement", "review", "test"},
			action: "view",
		},
		taskSteps: []daemon.TaskStepDetail{
			{Name: "implement", Status: stepStatusCompleted, Context: "implement content"},
			{Name: "review", Status: stepStatusCompleted, Context: "review content"},
			{Name: "test", Status: stepStatusCompleted, Context: "test content"},
		},
	}

	// Move down
	jMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ := m.handleSelectorKey(jMsg)
	updated := result.(Model)
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", updated.selector.cursor)
	}

	// Move down again
	result, _ = updated.handleSelectorKey(jMsg)
	updated = result.(Model)
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor at 2, got %d", updated.selector.cursor)
	}

	// Move down at bottom — should stay
	result, _ = updated.handleSelectorKey(jMsg)
	updated = result.(Model)
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", updated.selector.cursor)
	}

	// Move up
	kMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	result, _ = updated.handleSelectorKey(kMsg)
	updated = result.(Model)
	if updated.selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", updated.selector.cursor)
	}
}

func TestHandleArtifactSelectKey_EscCancels(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		selector: selector{
			kind:   selectorArtifact,
			cursor: 1,
			items:  []string{"implement", "review"},
			action: "view",
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleSelectorKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorArtifact {
		t.Error("expected selector kind not to be selectorArtifact after esc")
	}
}

func TestHandleArtifactViewKey_QReturnsToList(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewArtifact,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, cmd := m.handleArtifactViewKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList, got %d", updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleArtifactViewKey_EscReturnsToList(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewArtifact,
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handleArtifactViewKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList, got %d", updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}
