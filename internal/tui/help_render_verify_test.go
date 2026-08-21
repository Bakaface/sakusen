package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// TestHelpOverlay_NonEditableArtifactOmitsEdit verifies the artifact help
// overlay hides the "Actions" group entirely when the step is non-editable.
// This guards against showing an empty "Actions" section header.
func TestHelpOverlay_NonEditableArtifactOmitsEdit(t *testing.T) {
	m := Model{
		keys:  newKeyMap(),
		list:  newListView(false, ""),
		view:  viewArtifact,
		width: 120, height: 30,
	}
	m.artifactView.SetSize(120, 30)
	m.artifactView.SetContent("implement", "test content")
	m.artifactView.editable = false
	m.artifactView.showHelp = true

	output := m.View()
	if strings.Contains(output, "edit context") {
		t.Error("non-editable artifact help should not show 'edit context'")
	}
	// Even without Actions, the overlay must still be navigable.
	for _, want := range []string{"Step Context Help", "Navigation", "General", "ctrl+h"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected non-editable artifact help to contain %q", want)
		}
	}
}

// TestHelpOverlay_DetailIncludesBothModes verifies the detail help overlay
// surfaces bindings from both follow- and normal-mode keymaps, so a user
// reading the help can see everything regardless of which mode triggered it.
func TestHelpOverlay_DetailIncludesBothModes(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
		width:  120, height: 30,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test"}
	m.detail.SetTask(&task)
	m.detail.SetSize(120, 30)
	m.detail.showHelp = true

	output := m.View()
	// follow-only binding
	if !strings.Contains(output, "normal mode") {
		t.Error("expected detail help to surface follow-mode 'normal mode' binding")
	}
	// normal-only binding
	if !strings.Contains(output, "follow") {
		t.Error("expected detail help to surface normal-mode 'follow' binding")
	}
}
