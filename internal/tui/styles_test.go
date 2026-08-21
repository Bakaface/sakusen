package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestAppTitleIsJapaneseSakusen(t *testing.T) {
	if !strings.Contains(AppTitle, "作戦") {
		t.Errorf("AppTitle should contain 作戦, got %q", AppTitle)
	}
}

func TestPromptPrefixContainsAirplane(t *testing.T) {
	if !strings.Contains(PromptPrefix, "✈") {
		t.Errorf("PromptPrefix should contain airplane (✈), got %q", PromptPrefix)
	}
}

func TestProjectIndicatorStyleIsGreyWithNoBackground(t *testing.T) {
	fg := projectIndicatorStyle.GetForeground()
	if fg == nil {
		t.Fatal("projectIndicatorStyle should have a foreground color")
	}
	// Should be grey (#6B6B6B), not the title style highlight color
	bg := projectIndicatorStyle.GetBackground()
	if bg != nil {
		// lipgloss returns NoColor{} for unset backgrounds; ensure it's not a real color
		if _, ok := bg.(lipgloss.NoColor); !ok {
			t.Errorf("projectIndicatorStyle should have no background, got %v", bg)
		}
	}
	if projectIndicatorStyle.GetBold() {
		t.Error("projectIndicatorStyle should not be bold")
	}
}
