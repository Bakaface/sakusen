package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestNewSakusenAnimation(t *testing.T) {
	positions := [][2]int{{2, 0}, {2, 1}}
	a := newSakusenAnimation(positions, 40, 10, 1500)

	if len(a.planes) != 2 {
		t.Fatalf("expected 2 planes, got %d", len(a.planes))
	}
	if a.width != 40 || a.height != 10 {
		t.Errorf("expected width=40 height=10, got width=%d height=%d", a.width, a.height)
	}
	if a.speed < 1 {
		t.Errorf("expected speed >= 1, got %d", a.speed)
	}
	if a.done {
		t.Error("expected animation not done initially")
	}
}

func TestSakusenAnimationUpdate(t *testing.T) {
	positions := [][2]int{{2, 0}}
	a := newSakusenAnimation(positions, 20, 5, 500)

	initialX := a.planes[0].x
	// Advance past start delay
	for i := 0; i < a.planes[0].startDelay+1; i++ {
		a = a.Update()
	}
	if a.planes[0].x <= initialX {
		t.Errorf("plane should have moved right, was %d now %d", initialX, a.planes[0].x)
	}
}

func TestSakusenAnimationDone(t *testing.T) {
	positions := [][2]int{{2, 0}}
	a := newSakusenAnimation(positions, 10, 3, 100)

	for i := 0; i < 500 && !a.done; i++ {
		a = a.Update()
	}
	if !a.done {
		t.Error("animation should eventually complete")
	}
}

func TestSakusenAnimationViewDimensions(t *testing.T) {
	positions := [][2]int{{2, 0}, {2, 2}}
	a := newSakusenAnimation(positions, 30, 5, 1500)

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestSakusenAnimationViewEmpty(t *testing.T) {
	a := sakusenAnimation{width: 0, height: 0}
	if a.View() != "" {
		t.Error("expected empty view for zero dimensions")
	}
}

func TestSakusenAnimationPlaneColor(t *testing.T) {
	// The animated plane should use promptColor (#E8E8E8), not highlight (blue).
	positions := [][2]int{{5, 0}}
	a := newSakusenAnimation(positions, 30, 1, 1500)

	view := a.View()
	// The plane character should be styled with promptColor
	expectedStyle := lipgloss.NewStyle().Foreground(promptColor)
	styledPlane := expectedStyle.Render("✈")
	if !strings.Contains(view, styledPlane) {
		t.Errorf("plane should be styled with promptColor (%s), view: %q", promptColor, view)
	}
}

func TestSakusenAnimationTrailColors(t *testing.T) {
	// After the plane moves, it should leave grey trails behind.
	positions := [][2]int{{2, 0}}
	a := newSakusenAnimation(positions, 40, 1, 1500)

	// Advance until plane has moved enough to show trails
	for i := 0; i < 20; i++ {
		a = a.Update()
	}

	view := a.View()

	// Solid trail uses grey (#888888)
	solidStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	solidTrail := solidStyle.Render("─")
	if !strings.Contains(view, solidTrail) {
		t.Error("expected solid grey trail (─) in view after plane moves")
	}

	// Fade trail uses darker grey (#555555)
	fadeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	fadeTrail := fadeStyle.Render("·")
	if !strings.Contains(view, fadeTrail) {
		t.Error("expected fading grey trail (·) in view after plane moves")
	}
}

func TestSakusenAnimationPlaneMatchesPromptColor(t *testing.T) {
	// Verify the plane in animation uses the same color as the prompt textarea.
	positions := [][2]int{{5, 0}}
	a := newSakusenAnimation(positions, 30, 1, 1500)
	view := a.View()

	p := newPromptView(true, branchModeNew, "")
	promptStyled := p.textarea.FocusedStyle.Prompt.Render("✈")
	planeStyled := lipgloss.NewStyle().Foreground(promptColor).Render("✈")

	// Both the animation plane and the prompt ✈ should render identically.
	if promptStyled != planeStyled {
		t.Errorf("prompt and animation plane colors should match:\n  prompt: %q\n  animation: %q", promptStyled, planeStyled)
	}
	if !strings.Contains(view, planeStyled) {
		t.Errorf("animation view should contain plane styled with promptColor")
	}
}

func TestPromptViewUsesPromptColor(t *testing.T) {
	p := newPromptView(true, branchModeNew, "")

	// Verify that the prompt style renders with the same color as promptColor.
	expectedStyle := lipgloss.NewStyle().Foreground(promptColor)
	expected := expectedStyle.Render("test")
	focusedResult := p.textarea.FocusedStyle.Prompt.Render("test")
	blurredResult := p.textarea.BlurredStyle.Prompt.Render("test")

	if focusedResult != expected {
		t.Errorf("FocusedStyle.Prompt color mismatch:\n  got:  %q\n  want: %q", focusedResult, expected)
	}
	if blurredResult != expected {
		t.Errorf("BlurredStyle.Prompt color mismatch:\n  got:  %q\n  want: %q", blurredResult, expected)
	}
}
