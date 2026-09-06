package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// writePrompt creates <dir>/<name>.md, making dir if needed.
func writePrompt(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// promptTiers returns an empty (project, global) pair of prompt dirs plus the
// PromptDirs slice a TemplateContext would carry for them.
func promptTiers(t *testing.T) (projectDir, globalDir string, dirs []string) {
	t.Helper()
	projectDir = filepath.Join(t.TempDir(), "project-prompts")
	globalDir = filepath.Join(t.TempDir(), "global-prompts")
	return projectDir, globalDir, []string{projectDir, globalDir}
}

// TestResolveTemplatePromptIncludeTiers covers the two-tier lookup: the project
// tier wins when both define a passage, and the global tier is the fallback.
func TestResolveTemplatePromptIncludeTiers(t *testing.T) {
	t.Run("project overrides global", func(t *testing.T) {
		projectDir, globalDir, dirs := promptTiers(t)
		writePrompt(t, globalDir, "craft", "GLOBAL CRAFT")
		writePrompt(t, projectDir, "craft", "PROJECT CRAFT")

		got := mustResolveTemplate(t, "before {{prompt.craft}} after", &TemplateContext{PromptDirs: dirs})
		if got != "before PROJECT CRAFT after" {
			t.Errorf("got %q, want the project-tier passage", got)
		}
	})

	t.Run("global fallback", func(t *testing.T) {
		_, globalDir, dirs := promptTiers(t)
		writePrompt(t, globalDir, "craft", "GLOBAL CRAFT")

		got := mustResolveTemplate(t, "before {{prompt.craft}} after", &TemplateContext{PromptDirs: dirs})
		if got != "before GLOBAL CRAFT after" {
			t.Errorf("got %q, want the global-tier passage", got)
		}
	})
}

// TestResolveTemplatePromptIncludeResolvesInnerPlaceholders pins the "same
// pass" semantics: an included passage may itself use {{task.*}} / {{steps.*}}.
func TestResolveTemplatePromptIncludeResolvesInnerPlaceholders(t *testing.T) {
	projectDir, _, dirs := promptTiers(t)
	writePrompt(t, projectDir, "header", "Task #{{task.id}} ({{task.title}})\nPlan: {{steps.planning.context}}")

	got := mustResolveTemplate(t, "{{prompt.header}}\n---\ngo", &TemplateContext{
		Task:       TaskVars{ID: 7, Title: "fix the widget"},
		Steps:      map[string]string{"planning": "do the thing"},
		PromptDirs: dirs,
	})
	want := "Task #7 (fix the widget)\nPlan: do the thing\n---\ngo"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveTemplatePromptIncludeTrailingNewline verifies that exactly one
// trailing newline is stripped, so inline placement doesn't inject a blank
// line while an intentional blank line at the end of a file survives.
func TestResolveTemplatePromptIncludeTrailingNewline(t *testing.T) {
	projectDir, _, dirs := promptTiers(t)
	writePrompt(t, projectDir, "one", "body\n")
	writePrompt(t, projectDir, "two", "body\n\n")
	writePrompt(t, projectDir, "indent", "  body  ")

	cases := []struct{ tmpl, want string }{
		{"Foo {{prompt.one}} bar", "Foo body bar"},
		{"Foo {{prompt.two}} bar", "Foo body\n bar"},
		{"Foo {{prompt.indent}} bar", "Foo   body   bar"},
	}
	for _, tc := range cases {
		if got := mustResolveTemplate(t, tc.tmpl, &TemplateContext{PromptDirs: dirs}); got != tc.want {
			t.Errorf("ResolveTemplate(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

// TestResolveTemplatePromptIncludeErrors covers every way an include can fail.
// None of them may fall back to leaving the placeholder verbatim.
func TestResolveTemplatePromptIncludeErrors(t *testing.T) {
	projectDir, globalDir, dirs := promptTiers(t)
	writePrompt(t, projectDir, "outer", "start {{prompt.inner}} end")
	writePrompt(t, projectDir, "inner", "INNER")

	cases := []struct {
		name string
		tmpl string
		want []string // substrings the error must contain
	}{
		{
			name: "missing file names both searched paths",
			tmpl: "{{prompt.nope}}",
			want: []string{
				filepath.Join(projectDir, "nope.md"),
				filepath.Join(globalDir, "nope.md"),
			},
		},
		{
			name: "nested include is rejected",
			tmpl: "{{prompt.outer}}",
			want: []string{"outer", "one level deep"},
		},
		{
			name: "malformed name is rejected",
			tmpl: "{{prompt.bad name!}}",
			want: []string{"invalid prompt include name"},
		},
		{
			name: "no prompt dirs configured",
			tmpl: "{{prompt.craft}}",
			want: []string{"not found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &TemplateContext{PromptDirs: dirs}
			if tc.name == "no prompt dirs configured" {
				ctx = &TemplateContext{}
			}
			got, err := ResolveTemplate(tc.tmpl, ctx)
			if err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
			if got != "" {
				t.Errorf("a failed resolution must return \"\", got %q", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

// TestStepPromptIncludeFailsStep verifies the runtime contract on the step
// path: an unresolvable include fails the step (and records the reason on the
// task) rather than handing an agent a prompt with a literal placeholder.
func TestStepPromptIncludeFailsStep(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{{
			Name:  "wf",
			Steps: []config.StepConfig{{Name: "implementing", Prompt: "{{prompt.nope}}"}},
		}},
	}
	store := newFakeTaskStore()
	e := &Engine{cfg: newEngineConfig(cfg, repoRoot), database: store, repoRoot: repoRoot}

	dirs := e.cfg.PromptDirs
	if len(dirs) == 0 || dirs[0] != filepath.Join(repoRoot, ".sakusen", "prompts") {
		t.Fatalf("PromptDirs = %v, want the repo root's .sakusen/prompts first", dirs)
	}

	_, err := ResolveTemplate(cfg.Workflows[0].Steps[0].Prompt, &TemplateContext{PromptDirs: dirs})
	if err == nil {
		t.Fatal("expected the missing include to error")
	}
	if !strings.Contains(err.Error(), filepath.Join(repoRoot, ".sakusen", "prompts", "nope.md")) {
		t.Errorf("error %q should name the project-tier search path", err)
	}
}

// TestSummarizationPromptInclude covers a step's summarization_prompt going
// through the same include expansion as its prompt.
func TestSummarizationPromptInclude(t *testing.T) {
	repoRoot := t.TempDir()
	writePrompt(t, filepath.Join(repoRoot, ".sakusen", "prompts"), "summary-rules",
		"Summarize task {{task.id}} tersely.")

	e := &Engine{cfg: newEngineConfig(&config.Config{}, repoRoot), database: newFakeTaskStore()}
	tk := &task.Task{ID: 7, Title: "fix the widget"}

	got, err := e.buildSummarizePrompt(tk, "implement", "{{prompt.summary-rules}}\n{{chat}}", "THE CHAT LOG")
	if err != nil {
		t.Fatalf("buildSummarizePrompt: %v", err)
	}
	want := "Summarize task 7 tersely.\nTHE CHAT LOG"
	if got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}

	if _, err := e.buildSummarizePrompt(tk, "implement", "{{prompt.nope}}", "THE CHAT LOG"); err == nil {
		t.Error("expected a missing summarization_prompt include to error")
	}
}

// TestSummarizerPromptInclude covers the workflow-level summarizer_prompt: the
// include is expanded before the summarizer command sees the prompt.
func TestSummarizerPromptInclude(t *testing.T) {
	repoRoot := t.TempDir()
	writePrompt(t, filepath.Join(repoRoot, ".sakusen", "prompts"), "wrap-up",
		"Wrap up task {{task.id}}.")

	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	cmd := fmt.Sprintf(`cat > %q; echo THE-SUMMARY`, promptFile)
	cfg := &config.Config{Summarizer: config.SummarizerConfig{Command: cmd}}
	store := newFakeTaskStore()
	e := &Engine{cfg: newEngineConfig(cfg, repoRoot), database: store}

	tk := &task.Task{ID: 7, Title: "fix the widget", WorktreePath: t.TempDir()}
	store.stepContexts[7] = map[string]string{"implementing": "did the thing"}
	wf := &config.WorkflowConfig{
		Name:             "wf",
		Steps:            []config.StepConfig{{Name: "implementing"}},
		SummarizerPrompt: "{{prompt.wrap-up}} Context: {{steps.implementing.context}}",
	}

	if err := e.runSummarizer(context.Background(), tk, wf, "", nil); err != nil {
		t.Fatalf("runSummarizer: %v", err)
	}
	data, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read recorded prompt: %v", err)
	}
	want := "Wrap up task 7. Context: did the thing"
	if strings.TrimSpace(string(data)) != want {
		t.Errorf("summarizer prompt = %q, want %q", string(data), want)
	}

	wf.SummarizerPrompt = "{{prompt.nope}}"
	if err := e.runSummarizer(context.Background(), tk, wf, "", nil); err == nil {
		t.Error("expected a missing summarizer_prompt include to fail the summarizer")
	}
}
