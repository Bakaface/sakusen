package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

// stepFixture returns a representative mix of step states for tests.
func stepFixture() []daemon.TaskStepDetail {
	completed := time.Now().Add(-3 * time.Minute)
	return []daemon.TaskStepDetail{
		{Name: "plan", Status: stepStatusCompleted, Context: "plan output goes here", CompletedAt: &completed},
		{Name: "implement", Status: stepStatusCompleted, Context: strings.Repeat("x", 1400), CompletedAt: &completed},
		{Name: "review", Status: stepStatusRunning},
		{Name: "verify", Status: stepStatusPending},
		{Name: "summarize", Status: stepStatusPending},
	}
}

// loadFixture pushes a taskStepsLoadedMsg through Update so the selector is populated.
func loadFixture(t *testing.T, action string) Model {
	t.Helper()
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewTaskInfo,
	}
	m.artifactView.SetSize(80, 24)
	result, _ := m.Update(taskStepsLoadedMsg{
		taskID: 7,
		steps:  stepFixture(),
		action: action,
	})
	return result.(Model)
}

func TestStepSelector_RendersAllStepsWithGlyphs(t *testing.T) {
	m := loadFixture(t, "view")

	if m.selector.kind != selectorArtifact {
		t.Fatalf("expected selectorArtifact, got %d", m.selector.kind)
	}
	if got, want := len(m.selector.items), 5; got != want {
		t.Fatalf("expected %d items, got %d", want, got)
	}

	want := []string{
		"✓ plan",
		"✓ implement",
		"⟳ review (running)",
		"· verify (pending)",
		"· summarize (pending)",
	}
	for i, w := range want {
		if m.selector.items[i] != w {
			t.Errorf("item %d: got %q, want %q", i, m.selector.items[i], w)
		}
	}

	if !m.selector.disabled[3] || !m.selector.disabled[4] {
		t.Error("pending rows should be disabled")
	}
	if m.selector.disabled[0] || m.selector.disabled[1] || m.selector.disabled[2] {
		t.Errorf("non-pending rows should not be disabled, got %v", m.selector.disabled)
	}
}

func TestStepSelector_HintAdvertisesEdit(t *testing.T) {
	m := loadFixture(t, "view")
	view := m.selector.View()
	if !strings.Contains(view, "e: edit") {
		t.Errorf("expected hint to mention 'e: edit', got:\n%s", view)
	}
	if !strings.Contains(view, "enter: view") {
		t.Errorf("expected hint to mention 'enter: view', got:\n%s", view)
	}
}

func TestStepSelector_CursorStartsOnFirstActionable(t *testing.T) {
	m := loadFixture(t, "view")
	if m.selector.cursor != 0 {
		t.Errorf("expected cursor on first actionable row (0), got %d", m.selector.cursor)
	}
}

func TestStepSelector_JKSkipsPendingRows(t *testing.T) {
	m := loadFixture(t, "view")
	m.selector.cursor = 2 // running row

	jMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ := m.handleSelectorKey(jMsg)
	m = result.(Model)

	if m.selector.cursor != 2 {
		t.Errorf("j on row 2 with no actionable row below should stay at 2, got %d", m.selector.cursor)
	}

	// From the top (row 0), pressing j a few times should end at row 2 (last actionable).
	m.selector.cursor = 0
	for i := 0; i < 5; i++ {
		result, _ = m.handleSelectorKey(jMsg)
		m = result.(Model)
	}
	if m.selector.cursor != 2 {
		t.Errorf("repeated j should land on last actionable (2), got %d", m.selector.cursor)
	}
}

func TestStepSelector_EKeySetsEditAction(t *testing.T) {
	m := loadFixture(t, "view")
	m.selector.cursor = 0
	if m.selector.action != "view" {
		t.Fatalf("precondition: action should be 'view', got %q", m.selector.action)
	}

	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.selector.HandleKey(eMsg.String()), tea.Cmd(nil)
	_ = result
	if m.selector.action != "edit" {
		t.Errorf("pressing 'e' should set action to 'edit', got %q", m.selector.action)
	}
}

func TestStepSelector_EnterOnDisabledRowDoesNothing(t *testing.T) {
	m := loadFixture(t, "view")
	m.selector.cursor = 3 // pending

	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	result, _ := m.handleSelectorKey(enterMsg)
	m = result.(Model)

	if m.view == viewArtifact {
		t.Error("enter on disabled row should not open the artifact view")
	}
	if !m.selector.IsActive() {
		t.Error("enter on disabled row should keep the selector active")
	}
}

func TestStepSelector_NumberKeyOnDisabledRowIgnored(t *testing.T) {
	m := loadFixture(t, "view")
	// Row 4 is pending — pressing "4" should not select it.
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}}
	result, _ := m.handleSelectorKey(keyMsg)
	m = result.(Model)

	if !m.selector.IsActive() {
		t.Error("number key on disabled row should not select it")
	}
}

func TestArtifactView_EditKeyInHelpWhenEditable(t *testing.T) {
	v := artifactViewState{}
	v.SetSize(80, 24)
	v.SetContent("plan", "body")
	v.editable = true

	if !strings.Contains(v.View(), "edit") {
		t.Errorf("editable artifact view should show 'edit' in help, got:\n%s", v.View())
	}
}

func TestArtifactView_NoEditKeyWhenNotEditable(t *testing.T) {
	v := artifactViewState{}
	v.SetSize(80, 24)
	v.SetContent("verify", "· Step has not started yet.")
	v.editable = false

	help := v.View()
	// "edit" should not appear as a help binding; check the help line specifically.
	helpLine := ""
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "scroll") {
			helpLine = line
			break
		}
	}
	if strings.Contains(helpLine, "edit") {
		t.Errorf("non-editable artifact view should not advertise 'edit', got help:\n%s", helpLine)
	}
}

func TestRenderStepBody_PlaceholderForPending(t *testing.T) {
	body := renderStepBody(daemon.TaskStepDetail{Name: "verify", Status: stepStatusPending})
	if !strings.Contains(body, "not started") {
		t.Errorf("expected pending placeholder, got %q", body)
	}
}

func TestRenderStepBody_PlaceholderForRunning(t *testing.T) {
	body := renderStepBody(daemon.TaskStepDetail{Name: "review", Status: stepStatusRunning})
	if !strings.Contains(body, "in progress") {
		t.Errorf("expected running placeholder, got %q", body)
	}
}

func TestRenderStepBody_PlaceholderForCompletedEmpty(t *testing.T) {
	body := renderStepBody(daemon.TaskStepDetail{Name: "plan", Status: stepStatusCompleted, Context: ""})
	if !strings.Contains(body, "no context") {
		t.Errorf("expected empty-context placeholder, got %q", body)
	}
}

// TestStepSelector_VisualLayout renders the selector and asserts each row is
// present with its glyph. Prints the rendered view to test output for visual
// inspection via `go test -v`.
func TestStepSelector_VisualLayout(t *testing.T) {
	m := loadFixture(t, "view")
	out := m.selector.View()
	t.Logf("Step selector view:\n%s", out)

	wantSubstrings := []string{
		"Step Context",       // title
		"✓ plan",             // completed
		"✓ implement",        // completed
		"⟳ review (running)", // running
		"· verify (pending)", // pending
		"j/k: navigate",      // hint
		"e: edit",            // hint
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(out, w) {
			t.Errorf("expected output to contain %q, missing", w)
		}
	}
}
