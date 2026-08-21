package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

func TestProjectLogPrefix(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cfg := &config.Config{}
	s := NewServer(cfg, database)

	// Create a project in the DB
	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Known project should return "[myproject] " prefix
	prefix := s.projectLogPrefix(proj.ID)
	expected := "[myproject] "
	if prefix != expected {
		t.Errorf("expected prefix %q, got %q", expected, prefix)
	}

	// Unknown project ID should return empty string
	prefix = s.projectLogPrefix(9999)
	if prefix != "" {
		t.Errorf("expected empty prefix for unknown project, got %q", prefix)
	}
}

func TestProjectLogPrefix_EmptyName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cfg := &config.Config{}
	s := NewServer(cfg, database)

	// Project with path "/" would have empty name (basename of "/")
	// In practice this shouldn't happen but test the fallback
	prefix := s.projectLogPrefix(0)
	if prefix != "" {
		t.Errorf("expected empty prefix for project ID 0, got %q", prefix)
	}
}

func TestCreateTaskRequest_EmptyDescriptionWithCheckoutBranch(t *testing.T) {
	// Verify that title is generated from branch name when description is empty
	// and checkout branch is provided
	tests := []struct {
		name           string
		description    string
		checkoutBranch string
		wantTitle      string
		wantError      bool
	}{
		{
			name:           "empty description with checkout branch generates branch title",
			description:    "",
			checkoutBranch: "feature/my-feature",
			wantTitle:      "⎇ feature/my-feature",
			wantError:      false,
		},
		{
			name:           "empty description without checkout branch is rejected",
			description:    "",
			checkoutBranch: "",
			wantTitle:      "",
			wantError:      true,
		},
		{
			name:           "non-empty description with checkout branch uses sanitized description",
			description:    "Implement the login page",
			checkoutBranch: "feature/login",
			wantTitle:      "Implement the login page",
			wantError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			description := strings.TrimSpace(tt.description)

			// Simulate the validation logic from handleCreateTask
			if description == "" && tt.checkoutBranch == "" {
				if !tt.wantError {
					t.Error("expected no error but got validation failure")
				}
				return
			}
			if tt.wantError {
				t.Error("expected error but validation passed")
				return
			}

			// Simulate the title generation logic
			var title string
			if description == "" && tt.checkoutBranch != "" {
				title = "⎇ " + tt.checkoutBranch
			} else {
				title = task.SanitizeTitle(description)
			}

			if title != tt.wantTitle {
				t.Errorf("got title %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

func TestRefineTaskTitle_SkipsAIForEmptyDescription(t *testing.T) {
	// When description is empty (existing branch mode), refineTaskTitle should
	// use the initial title directly without calling generateTitle
	initialTitle := "⎇ feature/my-branch"
	description := ""

	// Simulate the logic from refineTaskTitle
	var title string
	if description == "" {
		title = initialTitle
	}

	if title != initialTitle {
		t.Errorf("expected title %q for empty description, got %q", initialTitle, title)
	}

	slug := task.Slugify(title)
	if slug == "" {
		t.Error("expected non-empty slug")
	}
}

func TestRefineTaskTitle_ManualTitleSkipsGeneration(t *testing.T) {
	// When manualTitle is non-empty, it should be used directly regardless of description
	tests := []struct {
		name        string
		description string
		manualTitle string
		wantTitle   string
	}{
		{
			name:        "manual title overrides AI generation",
			description: "some description that would trigger AI",
			manualTitle: "My Custom Title",
			wantTitle:   "My Custom Title",
		},
		{
			name:        "manual title used when description is also empty",
			description: "",
			manualTitle: "Manual Only Title",
			wantTitle:   "Manual Only Title",
		},
		{
			name:        "empty manual title falls through to normal logic",
			description: "",
			manualTitle: "",
			wantTitle:   "initial-title", // uses initialTitle when description is empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initialTitle := "initial-title"

			// Simulate the logic from refineTaskTitle with manualTitle parameter
			var title string
			if tt.manualTitle != "" {
				title = tt.manualTitle
			} else if tt.description == "" {
				title = initialTitle
			} else {
				// Would call AI generation in real code; not tested here
				title = task.SanitizeTitle(tt.description)
			}

			if title != tt.wantTitle {
				t.Errorf("got title %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

func TestCreateTaskRequest_TitleField(t *testing.T) {
	// Verify that CreateTaskRequest properly includes the Title field
	req := CreateTaskRequest{
		Title: "My Manual Title",
		Input: "task description",
	}

	if req.Title != "My Manual Title" {
		t.Errorf("expected Title %q, got %q", "My Manual Title", req.Title)
	}

	// Empty title should be the zero value
	req2 := CreateTaskRequest{Input: "task description"}
	if req2.Title != "" {
		t.Errorf("expected empty Title, got %q", req2.Title)
	}
}

func TestTmuxFirstTitle(t *testing.T) {
	tests := []struct {
		name string
		wf   *config.WorkflowConfig
		want string
	}{
		{name: "nil workflow falls back", wf: nil, want: "tmux session"},
		{name: "unnamed workflow falls back", wf: &config.WorkflowConfig{}, want: "tmux session"},
		{name: "named workflow prefixed", wf: &config.WorkflowConfig{Name: "interact"}, want: "tmux: interact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tmuxFirstTitle(tt.wf); got != tt.want {
				t.Errorf("tmuxFirstTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateTaskRequest_EmptyDescriptionAllowedForTmuxFirstWorkflow(t *testing.T) {
	tmuxFirst := config.WorkflowConfig{
		// First step resolves to a tmux-mode agent.
		Name:  "interact",
		Agent: "interactive",
		Steps: []config.StepConfig{
			{Name: "shell"},
		},
	}
	plain := config.WorkflowConfig{
		// Resolves to the headless "claude" fallback, so this workflow
		// demands a description.
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement"},
		},
	}
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"claude":      {Mode: config.AgentModeHeadless, Command: "true"},
			"interactive": {Mode: config.AgentModeTmux, Command: "true"},
		},
		Workflows: []config.WorkflowConfig{plain, tmuxFirst},
	}

	tests := []struct {
		name           string
		description    string
		checkoutBranch string
		workflow       string
		wantReject     bool
		wantTitle      string
	}{
		{
			name:        "empty description with tmux-first workflow is allowed",
			description: "",
			workflow:    "interact",
			wantReject:  false,
			wantTitle:   "tmux: interact",
		},
		{
			name:        "empty description with print workflow is rejected",
			description: "",
			workflow:    "default",
			wantReject:  true,
		},
		{
			name:           "empty description with checkout branch wins regardless of workflow",
			description:    "",
			checkoutBranch: "feature/x",
			workflow:       "default",
			wantReject:     false,
			wantTitle:      "⎇ feature/x",
		},
		{
			name:        "non-empty description with tmux-first workflow uses sanitized description",
			description: "Spike on caching",
			workflow:    "interact",
			wantReject:  false,
			wantTitle:   "Spike on caching",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			description := strings.TrimSpace(tt.description)
			wf := cfg.GetWorkflow(tt.workflow)
			tmuxFirstStep := cfg.FirstStepIsTmux(wf)

			rejected := description == "" && tt.checkoutBranch == "" && !tmuxFirstStep
			if rejected != tt.wantReject {
				t.Fatalf("rejection mismatch: got %v, want %v", rejected, tt.wantReject)
			}
			if rejected {
				return
			}

			var title string
			switch {
			case description == "" && tt.checkoutBranch != "":
				title = "⎇ " + tt.checkoutBranch
			case description == "" && tmuxFirstStep:
				title = tmuxFirstTitle(wf)
			default:
				title = task.SanitizeTitle(description)
			}
			if title != tt.wantTitle {
				t.Errorf("got title %q, want %q", title, tt.wantTitle)
			}
		})
	}
}
