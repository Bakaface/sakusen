package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// summarizerStub returns a summarizer command that appends "<purpose>\t<prompt>"
// (newlines squashed) to logPath for every call and answers per SAKUSEN_PURPOSE.
// A purpose absent from responses exits non-zero, simulating a failing call.
func summarizerStub(logPath string, responses map[string]string) string {
	var cases strings.Builder
	for purpose, answer := range responses {
		cases.WriteString(purpose + ") printf '%s\\n' " + shellQuote(answer) + ";; ")
	}
	return "p=$(cat); printf '%s\\t%s\\n' \"$SAKUSEN_PURPOSE\" \"$(printf '%s' \"$p\" | tr '\\n' ' ')\" >> " +
		shellQuote(logPath) + "; case \"$SAKUSEN_PURPOSE\" in " + cases.String() + "*) exit 1;; esac"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// summarizerCalls returns the logged (purpose, prompt) pairs in call order.
func summarizerCalls(t *testing.T, logPath string) [][2]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read summarizer log: %v", err)
	}
	var calls [][2]string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		purpose, prompt, _ := strings.Cut(line, "\t")
		calls = append(calls, [2]string{purpose, prompt})
	}
	return calls
}

func calledPurposes(calls [][2]string) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c[0]
	}
	return out
}

// setupSlugServer builds a server whose single project resolves to cfg, plus a
// task row in init status to refine. The projectContext is injected directly so
// no config file on disk (or the developer's global one) takes part.
func setupSlugServer(t *testing.T, cfg *config.Config) (*Server, *task.Task) {
	t.Helper()
	isolateGlobalConfig(t)

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	projectPath := t.TempDir()
	proj, err := database.GetOrCreateProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	s := NewServer(&config.Config{}, database)
	s.projectsMu.Lock()
	s.projects[proj.ID] = &projectContext{
		cfg:               cfg,
		repoRoot:          projectPath,
		tracksFingerprint: tracksFingerprint(projectPath),
	}
	s.projectsMu.Unlock()

	tk, err := database.CreateTask(proj.ID, "provisional", "input", "provisional", "default", "", task.StatusInit, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, tk
}

// refined runs refineTaskTitle and returns the resulting task row.
func refined(t *testing.T, s *Server, tk *task.Task, input, initialTitle, manualTitle, explicitSlug string) *task.Task {
	t.Helper()
	s.refineTaskTitle(tk.ID, tk.ProjectID, "", false, "", input, initialTitle, manualTitle, explicitSlug)
	got, err := s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return got
}

// TestRefineTaskTitle_SlugGeneration covers the slug decision tree: AI slug
// calls (alone or alongside the title call), explicit slugs winning, and the
// Slugify fallbacks when no AI answer is available.
func TestRefineTaskTitle_SlugGeneration(t *testing.T) {
	t.Run("manual title still gets an AI slug", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"title": "AI Title", "slug": "ai-slug"}),
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form with validation", "provisional", "Manual Title", "")

		if got.Title != "Manual Title" {
			t.Errorf("title = %q, want the manual title", got.Title)
		}
		if got.Slug != "ai-slug" {
			t.Errorf("slug = %q, want %q", got.Slug, "ai-slug")
		}
		if purposes := calledPurposes(summarizerCalls(t, logPath)); len(purposes) != 1 || purposes[0] != "slug" {
			t.Errorf("summarizer purposes = %v, want only [slug]", purposes)
		}
		if got.Status != task.StatusPending {
			t.Errorf("status = %q, want pending", got.Status)
		}
	})

	t.Run("auto title runs both calls", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"title": "AI Title", "slug": "ai-slug"}),
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form with validation", "provisional", "", "")

		if got.Title != "AI Title" {
			t.Errorf("title = %q, want %q", got.Title, "AI Title")
		}
		if got.Slug != "ai-slug" {
			t.Errorf("slug = %q, want %q", got.Slug, "ai-slug")
		}
		purposes := calledPurposes(summarizerCalls(t, logPath))
		if len(purposes) != 2 {
			t.Fatalf("summarizer purposes = %v, want one title and one slug call", purposes)
		}
		seen := map[string]bool{purposes[0]: true, purposes[1]: true}
		if !seen["title"] || !seen["slug"] {
			t.Errorf("summarizer purposes = %v, want one title and one slug call", purposes)
		}
	})

	t.Run("explicit slug wins and skips the slug call", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"title": "AI Title", "slug": "ai-slug"}),
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form with validation", "provisional", "", "My Custom Slug!")

		if got.Slug != "my-custom-slug" {
			t.Errorf("slug = %q, want the normalized explicit slug", got.Slug)
		}
		for _, purpose := range calledPurposes(summarizerCalls(t, logPath)) {
			if purpose == "slug" {
				t.Error("slug call was made despite an explicit slug")
			}
		}
	})

	t.Run("over-long explicit slug is capped", func(t *testing.T) {
		cfg := &config.Config{}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "", "provisional", "Manual Title", "put guardrails on task slug generation")

		if got.Slug != "put-guardrails-on-task" {
			t.Errorf("slug = %q, want the capped explicit slug", got.Slug)
		}
	})

	t.Run("failing slug call falls back to the slugified title", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"title": "AI Title"}), // no slug case → exit 1
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form with validation", "provisional", "Fix The Login Form", "")

		if got.Slug != task.Slugify("Fix The Login Form") {
			t.Errorf("slug = %q, want the slugified title", got.Slug)
		}
		if got.Status != task.StatusPending {
			t.Errorf("status = %q, want pending (a failed slug call must not stall the task)", got.Status)
		}
	})

	t.Run("empty slug answer falls back to the slugified title", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"slug": "  !!!  "}),
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form", "provisional", "Fix The Login Form", "")

		if got.Slug != task.Slugify("Fix The Login Form") {
			t.Errorf("slug = %q, want the slugified title", got.Slug)
		}
	})

	t.Run("failing title call keeps the generated slug", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"slug": "ai-slug"}), // no title case → exit 1
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "add a login form with validation", "Provisional Title", "", "")

		if got.Slug != "ai-slug" {
			t.Errorf("slug = %q, want the AI slug to survive the title failure", got.Slug)
		}
		if got.Title != "Provisional Title" {
			t.Errorf("title = %q, want the provisional title", got.Title)
		}
		if got.Status != task.StatusPending {
			t.Errorf("status = %q, want pending", got.Status)
		}
	})

	t.Run("empty input skips AI entirely", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cfg := &config.Config{Summarizer: config.SummarizerConfig{
			Command: summarizerStub(logPath, map[string]string{"title": "AI Title", "slug": "ai-slug"}),
		}}
		s, tk := setupSlugServer(t, cfg)

		got := refined(t, s, tk, "", "⎇ feature/login", "", "")

		if got.Slug != task.Slugify("⎇ feature/login") {
			t.Errorf("slug = %q, want the slugified initial title", got.Slug)
		}
		if purposes := calledPurposes(summarizerCalls(t, logPath)); len(purposes) != 0 {
			t.Errorf("summarizer purposes = %v, want none", purposes)
		}
	})

	t.Run("no summarizer falls back to the slugified title", func(t *testing.T) {
		s, tk := setupSlugServer(t, &config.Config{})

		got := refined(t, s, tk, "fix the stale daemon pid left behind by shutdown", "provisional", "", "")

		if got.Slug != task.Slugify(got.Title) {
			t.Errorf("slug = %q, want Slugify(title) = %q", got.Slug, task.Slugify(got.Title))
		}
		if len(got.Slug) > task.MaxSlugLength {
			t.Errorf("slug %q exceeds the %d-char cap", got.Slug, task.MaxSlugLength)
		}
	})
}

// TestRefineTaskTitle_PromptOverrides verifies that configured title_prompt /
// slug_prompt replace the built-in prompts, with {{input}} substituted.
func TestRefineTaskTitle_PromptOverrides(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	cfg := &config.Config{Summarizer: config.SummarizerConfig{
		Command:     summarizerStub(logPath, map[string]string{"title": "AI Title", "slug": "ai-slug"}),
		TitlePrompt: "CUSTOM TITLE for {{input}}",
		SlugPrompt:  "CUSTOM SLUG for {{input}}",
	}}
	s, tk := setupSlugServer(t, cfg)

	refined(t, s, tk, "add a login form", "provisional", "", "")

	prompts := map[string]string{}
	for _, call := range summarizerCalls(t, logPath) {
		prompts[call[0]] = call[1]
	}
	if want := "CUSTOM TITLE for add a login form"; prompts["title"] != want {
		t.Errorf("title prompt = %q, want %q", prompts["title"], want)
	}
	if want := "CUSTOM SLUG for add a login form"; prompts["slug"] != want {
		t.Errorf("slug prompt = %q, want %q", prompts["slug"], want)
	}
}

// TestSummarizerPrompt covers prompt rendering: overrides win, {{input}} is
// substituted, and an override without the placeholder gets the input appended.
func TestSummarizerPrompt(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{name: "empty override uses the default", override: "", want: "default: task input"},
		{name: "override with placeholder", override: "custom {{input}} here", want: "custom task input here"},
		{name: "whitespace-only override uses the default", override: "   ", want: "default: task input"},
		{name: "override without placeholder appends the input", override: "just do it", want: "just do it\n\ntask input"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizerPrompt(tt.override, "default: {{input}}", "task input")
			if got != tt.want {
				t.Errorf("summarizerPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCreateTaskFromRequest_ExplicitSlug verifies the provisional slug written
// at create time: normalized request slug when given, else title-derived.
func TestCreateTaskFromRequest_ExplicitSlug(t *testing.T) {
	s, projID := setupServerWithProject(t)
	proj, err := s.database.GetProject(projID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("explicit slug is normalized", func(t *testing.T) {
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: proj.Path,
			Input:       "do the thing",
			Title:       "Some Long Manual Title Here",
			Slug:        "My Custom SLUG!",
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Slug != "my-custom-slug" {
			t.Errorf("slug = %q, want %q", tk.Slug, "my-custom-slug")
		}
	})

	t.Run("no explicit slug falls back to the title", func(t *testing.T) {
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: proj.Path,
			Input:       "do the thing",
			Title:       "Some Long Manual Title Here",
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Slug != task.Slugify("Some Long Manual Title Here") {
			t.Errorf("slug = %q, want the slugified title", tk.Slug)
		}
	})
}
