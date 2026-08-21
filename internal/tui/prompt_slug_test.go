package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

// TestPromptView_SlugFieldRendered verifies the Slug row is always present on
// the New Task screen, sits directly under Title, and shows the
// auto-generation placeholder while empty.
func TestPromptView_SlugFieldRendered(t *testing.T) {
	p := newPromptView(true, branchModeNew, "main")
	p.SetSize(80, 30)

	lines := strings.Split(p.View(), "\n")
	titleRow, slugRow := -1, -1
	for i, line := range lines {
		plain := ansiRegex.ReplaceAllString(line, "")
		if strings.Contains(plain, "Title:") && titleRow == -1 {
			titleRow = i
		}
		if strings.Contains(plain, "Slug:") && slugRow == -1 {
			slugRow = i
		}
	}
	if titleRow == -1 || slugRow == -1 {
		t.Fatalf("expected Title and Slug rows, got title=%d slug=%d in view:\n%s", titleRow, slugRow, p.View())
	}
	if slugRow != titleRow+2 {
		t.Errorf("slug row = %d, want %d (directly below Title with one blank line)", slugRow, titleRow+2)
	}
	if !strings.Contains(ansiRegex.ReplaceAllString(lines[slugRow], ""), "auto-generated if left blank") {
		t.Errorf("slug row %q should show the auto-generation placeholder", ansiRegex.ReplaceAllString(lines[slugRow], ""))
	}

	// The rendered slug row must fit the terminal width.
	if w := lipgloss.Width(lines[slugRow]); w > 80 {
		t.Errorf("slug row width = %d, want <= 80", w)
	}
}

// TestPromptView_SlugFieldShowsTypedValue verifies typed text replaces the
// placeholder and is returned by SlugValue (trimmed).
func TestPromptView_SlugFieldShowsTypedValue(t *testing.T) {
	p := newPromptView(true, branchModeNew, "main")
	p.SetSize(80, 30)
	p.focusInput(promptFieldSlug)
	p.slugInput.SetValue("  my-slug  ")

	if got := p.SlugValue(); got != "my-slug" {
		t.Errorf("SlugValue() = %q, want %q", got, "my-slug")
	}
	if !strings.Contains(ansiRegex.ReplaceAllString(p.View(), ""), "my-slug") {
		t.Errorf("view should render the typed slug:\n%s", p.View())
	}
}

// TestPromptView_ResetClearsSlug verifies the slug doesn't leak into the next
// task created in the same session.
func TestPromptView_ResetClearsSlug(t *testing.T) {
	p := newPromptView(true, branchModeNew, "main")
	p.SetSize(80, 30)
	p.slugInput.SetValue("stale-slug")
	p.Reset()

	if got := p.SlugValue(); got != "" {
		t.Errorf("SlugValue() after Reset = %q, want empty", got)
	}
}

// TestPromptSubmit_SlugReachesCreateRequest verifies the New Task form's slug
// value lands on the daemon request: blank stays blank (daemon auto-generates),
// typed is forwarded verbatim.
func TestPromptSubmit_SlugReachesCreateRequest(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  string
	}{
		{name: "blank slug is auto-generated", typed: "", want: ""},
		{name: "typed slug is forwarded", typed: "my-slug", want: "my-slug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured daemon.CreateTaskRequest
			fake := &fakeTaskService{
				createTask: func(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
					captured = req
					return &daemon.TaskInfo{ID: 1}, nil
				},
			}
			m := Model{
				keys:        newKeyMap(),
				client:      fake,
				list:        newListView(false, ""),
				detail:      newDetailView(),
				view:        viewPrompt,
				projectPath: "/tmp/proj",
				prompt:      newPromptView(true, branchModeNew, "main"),
			}
			m.prompt.SetSize(80, 30)
			m.prompt.textarea.SetValue("do the thing")
			m.prompt.titleInput.SetValue("Do The Thing")
			m.prompt.slugInput.SetValue(tt.typed)

			_, cmd := m.handlePromptSubmit()
			if cmd == nil {
				t.Fatal("expected a create command")
			}
			cmd() // executes the create through the fake client

			if captured.Slug != tt.want {
				t.Errorf("CreateTaskRequest.Slug = %q, want %q", captured.Slug, tt.want)
			}
			if captured.Title != "Do The Thing" {
				t.Errorf("CreateTaskRequest.Title = %q, want the typed title", captured.Title)
			}
		})
	}
}
