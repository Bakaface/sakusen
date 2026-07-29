package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/charmbracelet/lipgloss"
)

// TestApplyPins_CycleClearsStaleValues guards the cross-workflow leak fix:
// applyPins must undo pin-supplied values so a previously-pinned workflow's
// literal values don't survive when the next workflow pins fewer fields (the
// case that arises when cycling between workflows mid-prompt, where no Reset()
// runs first). Here the user typed nothing before the pins, so undoing the
// pins restores every input to empty.
func TestApplyPins_CycleClearsStaleValues(t *testing.T) {
	p := newPromptView(true, branchModeNew, "")
	p.SetSize(80, 24)

	// Workflow A pins description + a new-branch template.
	wfA := &config.WorkflowConfig{Name: "a", Input: "descA", Branch: "feat/a", Target: "main"}
	p.applyPins(wfA)
	if got := p.branchInput.Value(); got != "feat/a" {
		t.Fatalf("after applyPins(A): branch = %q, want %q", got, "feat/a")
	}
	if got := p.textarea.Value(); got != "descA" {
		t.Fatalf("after applyPins(A): description = %q, want %q", got, "descA")
	}
	if !p.pins.branch || !p.pins.input || !p.pins.target {
		t.Fatalf("after applyPins(A): expected branch/description/target pinned, got %+v", p.pins)
	}

	// Workflow B pins nothing — every previously-pinned input must be cleared and
	// every pin flag must be false, otherwise A's literals leak into B's task.
	wfB := &config.WorkflowConfig{Name: "b"}
	p.applyPins(wfB)
	if got := p.branchInput.Value(); got != "" {
		t.Errorf("after applyPins(B): branch = %q, want empty (stale value leaked)", got)
	}
	if got := p.textarea.Value(); got != "" {
		t.Errorf("after applyPins(B): description = %q, want empty (stale value leaked)", got)
	}
	if got := p.checkoutInput.Value(); got != "" {
		t.Errorf("after applyPins(B): checkout = %q, want empty", got)
	}
	if got := p.targetBranchInput.Value(); got != "" {
		t.Errorf("after applyPins(B): target = %q, want empty (stale value leaked)", got)
	}
	if p.pins != (promptPins{}) {
		t.Errorf("after applyPins(B): expected no pins, got %+v", p.pins)
	}
}

// TestApplyPins_PreservesUserTypedValues guards the workflow-switch data-loss
// fix: cycling to another workflow must never wipe fields the user filled in.
// Only pin-supplied values are replaced; typed input survives the switch.
func TestApplyPins_PreservesUserTypedValues(t *testing.T) {
	t.Run("unpinned fields survive a switch", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)

		// User fills in the form.
		p.titleInput.SetValue("my title")
		p.textarea.SetValue("my prompt text")
		p.branchInput.SetValue("feat/mine")
		p.targetBranchInput.SetValue("develop")

		// Switch to a workflow that pins nothing (the reported bug: this used
		// to clear the description, branch, and target).
		p.applyPins(&config.WorkflowConfig{Name: "plain"})

		if got := p.titleInput.Value(); got != "my title" {
			t.Errorf("title = %q, want %q", got, "my title")
		}
		if got := p.textarea.Value(); got != "my prompt text" {
			t.Errorf("description = %q, want %q", got, "my prompt text")
		}
		if got := p.branchInput.Value(); got != "feat/mine" {
			t.Errorf("branch = %q, want %q", got, "feat/mine")
		}
		if got := p.targetBranchInput.Value(); got != "develop" {
			t.Errorf("target = %q, want %q", got, "develop")
		}
	})

	t.Run("pin displaces then restores typed value", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)

		p.textarea.SetValue("my prompt text")
		p.branchInput.SetValue("feat/mine")
		p.targetBranchInput.SetValue("develop")

		// Workflow A pins description + branch + target: pinned literals take over.
		wfA := &config.WorkflowConfig{Name: "a", Input: "descA", Branch: "feat/a", Target: "main"}
		p.applyPins(wfA)
		if got := p.textarea.Value(); got != "descA" {
			t.Fatalf("after applyPins(A): description = %q, want %q", got, "descA")
		}
		if got := p.branchInput.Value(); got != "feat/a" {
			t.Fatalf("after applyPins(A): branch = %q, want %q", got, "feat/a")
		}
		if got := p.targetBranchInput.Value(); got != "main" {
			t.Fatalf("after applyPins(A): target = %q, want %q", got, "main")
		}

		// Switch to workflow B (no pins): the user's original values come back.
		p.applyPins(&config.WorkflowConfig{Name: "b"})
		if got := p.textarea.Value(); got != "my prompt text" {
			t.Errorf("after applyPins(B): description = %q, want %q (typed value lost)", got, "my prompt text")
		}
		if got := p.branchInput.Value(); got != "feat/mine" {
			t.Errorf("after applyPins(B): branch = %q, want %q (typed value lost)", got, "feat/mine")
		}
		if got := p.targetBranchInput.Value(); got != "develop" {
			t.Errorf("after applyPins(B): target = %q, want %q (typed value lost)", got, "develop")
		}
		if p.pins != (promptPins{}) {
			t.Errorf("after applyPins(B): expected no pins, got %+v", p.pins)
		}
	})

	t.Run("typed value survives a chain of pinning workflows", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)

		p.textarea.SetValue("my prompt text")

		p.applyPins(&config.WorkflowConfig{Name: "a", Input: "descA"})
		p.applyPins(&config.WorkflowConfig{Name: "b", Input: "descB"})
		if got := p.textarea.Value(); got != "descB" {
			t.Fatalf("after applyPins(B): description = %q, want %q", got, "descB")
		}
		p.applyPins(&config.WorkflowConfig{Name: "c"})
		if got := p.textarea.Value(); got != "my prompt text" {
			t.Errorf("after A→B→C: description = %q, want %q (typed value lost)", got, "my prompt text")
		}
	})

	t.Run("worktree and branch mode restore after pin lifts", func(t *testing.T) {
		p := newPromptView(true, branchModeExisting, "")
		p.SetSize(80, 24)
		p.checkoutInput.SetValue("my-existing")

		worktreeFalse := false
		p.applyPins(&config.WorkflowConfig{Name: "a", Worktree: &worktreeFalse, Branch: "feat/a"})
		if p.worktree || p.branchMode != branchModeNew {
			t.Fatalf("after applyPins(A): worktree=%v mode=%v, want pinned false/new", p.worktree, p.branchMode)
		}

		p.applyPins(&config.WorkflowConfig{Name: "b"})
		if !p.worktree {
			t.Error("after applyPins(B): worktree = false, want true (user setting lost)")
		}
		if p.branchMode != branchModeExisting {
			t.Errorf("after applyPins(B): branchMode = %v, want branchModeExisting (user setting lost)", p.branchMode)
		}
		if got := p.checkoutInput.Value(); got != "my-existing" {
			t.Errorf("after applyPins(B): checkout = %q, want %q (typed value lost)", got, "my-existing")
		}
	})

	t.Run("focus stays on the focused field when still visible", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)
		p.focusInput(promptFieldTitle)

		p.applyPins(&config.WorkflowConfig{Name: "a", Branch: "feat/a"})
		if p.focusField != promptFieldTitle {
			t.Errorf("focusField = %v, want promptFieldTitle (focus reset by workflow switch)", p.focusField)
		}

		// Focused field pinned away → focus moves to the first visible field.
		p.focusInput(promptFieldInput)
		p.applyPins(&config.WorkflowConfig{Name: "b", Input: "pinned"})
		if p.focusField == promptFieldInput {
			t.Error("focusField still promptFieldInput after it was pinned away")
		}
	})
}

// TestCyclePane_ReachesWorkflowWhenGitFullyPinned guards the navigation fix:
// when worktree is on but every git field is pinned (no focusable git field),
// CyclePane(forward) from the main fields must skip the empty git section and
// land on the workflow pane rather than getting stuck on the main fields.
func TestCyclePane_ReachesWorkflowWhenGitFullyPinned(t *testing.T) {
	p := newPromptView(true, branchModeNew, "") // worktree on
	p.SetSize(80, 24)
	p.workflows = []string{"a", "b"}
	// Pin every git field (and the worktree toggle) — visibleFields() then
	// contains no git field, so FocusGitSection() is a no-op.
	p.pins.worktree = true
	p.pins.branch = true
	p.pins.target = true

	if p.hasVisibleGitField() {
		t.Fatalf("precondition: expected no visible git field, visibleFields=%v", p.visibleFields())
	}

	// Focus starts on the description (a main field).
	p.CyclePane(true)
	if p.activePane != paneWorkflow {
		t.Errorf("CyclePane(forward) with all git fields pinned: activePane = %v, want paneWorkflow (stuck on main pane)", p.activePane)
	}
}

// frameBorderWidths returns the rendered widths of every line containing a
// box-drawing border glyph. Within one View all framed-section lines share a
// single outer width, so these must all be equal.
func frameBorderWidths(view string) []int {
	var ws []int
	for _, ln := range strings.Split(view, "\n") {
		if strings.ContainsAny(ln, "╭│╰") {
			ws = append(ws, lipgloss.Width(ln))
		}
	}
	return ws
}

func assertUniformFrameWidths(t *testing.T, label, view string) {
	t.Helper()
	ws := frameBorderWidths(view)
	if len(ws) == 0 {
		t.Fatalf("%s: no framed lines found in view:\n%s", label, view)
	}
	for i, w := range ws {
		if w != ws[0] {
			t.Errorf("%s: frame line %d width = %d, want %d (non-uniform frame)\n%s", label, i, w, ws[0], view)
		}
	}
}

// TestPromptView_PinnedGitLayoutRenders verifies the two pin-driven layout
// branches render with structurally sound (uniform-width) frames and hide the
// pinned rows.
func TestPromptView_PinnedGitLayoutRenders(t *testing.T) {
	// Case 1: every git row pinned away → Git frame omitted, only Workflow pane.
	t.Run("git frame omitted", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)
		p.workflows = []string{"a", "b"}
		p.pins.worktree = true
		p.pins.branch = true
		p.pins.target = true

		view := p.View()
		if strings.Contains(view, "Branch:") || strings.Contains(view, "Worktree:") {
			t.Errorf("pinned git rows should be hidden, view:\n%s", view)
		}
		if !strings.Contains(view, "Workflow") {
			t.Errorf("expected Workflow pane to still render, view:\n%s", view)
		}
		assertUniformFrameWidths(t, "git-omitted", view)
	})

	// Case 2: partial git pin (target pinned) → Git + Workflow side-by-side.
	// Note: side-by-side frames are intentionally NOT uniform width — the taller
	// frame's overflow lines are left-frame-only (verified to match the no-pin
	// baseline). Here we assert the pinned row is hidden, both frames render, and
	// the spanning top border is the full inner width.
	t.Run("git and workflow side by side", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)
		p.workflows = []string{"a", "b"}
		p.pins.target = true // hides only the Target row

		view := p.View()
		if strings.Contains(view, "Target:") {
			t.Errorf("pinned Target row should be hidden, view:\n%s", view)
		}
		if !strings.Contains(view, "Branch:") {
			t.Errorf("unpinned Branch row should render, view:\n%s", view)
		}
		if !strings.Contains(view, "╭─ Git") || !strings.Contains(view, "╭─ Workflow") {
			t.Errorf("expected both Git and Workflow frames, view:\n%s", view)
		}
		max := 0
		for _, w := range frameBorderWidths(view) {
			if w > max {
				max = w
			}
		}
		if max != 80 { // innerWidth (width-1=79) + 1-space indent
			t.Errorf("side-by-side spanning width = %d, want 80\n%s", max, view)
		}
	})

	// Case 3: description pinned → textarea omitted from the main section.
	t.Run("description textarea omitted", func(t *testing.T) {
		p := newPromptView(true, branchModeNew, "")
		p.SetSize(80, 24)
		p.applyPins(&config.WorkflowConfig{Name: "a", Input: "pinned body"})

		view := p.View()
		if strings.Contains(view, "pinned body") {
			t.Errorf("pinned description should not render as an editable textarea, view:\n%s", view)
		}
		assertUniformFrameWidths(t, "desc-pinned", view)
	})
}
