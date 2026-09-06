package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePromptFile creates <dir>/<name>.md, creating dir as needed.
func writePromptFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatalf("write %s.md: %v", name, err)
	}
}

// TestPromptDirs pins the search order: project tier first, global tier second.
func TestPromptDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := t.TempDir()

	got := PromptDirs(projectDir)
	want := []string{
		filepath.Join(projectDir, ".sakusen", "prompts"),
		filepath.Join(home, ".sakusen", "prompts"),
	}
	if len(got) != len(want) {
		t.Fatalf("PromptDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PromptDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// An empty project dir leaves only the global tier — no accidental
	// resolution against the process's working directory.
	if got := PromptDirs(""); len(got) != 1 || got[0] != want[1] {
		t.Errorf(`PromptDirs("") = %v, want just %q`, got, want[1])
	}
}

// TestExpandPromptIncludes covers the tier precedence, newline handling and
// every failure mode of the include expander.
func TestExpandPromptIncludes(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "prompts")
	globalDir := filepath.Join(t.TempDir(), "prompts")
	dirs := []string{projectDir, globalDir}

	writePromptFile(t, globalDir, "craft", "GLOBAL")
	writePromptFile(t, globalDir, "only-global", "FALLBACK")
	writePromptFile(t, projectDir, "craft", "PROJECT")
	writePromptFile(t, projectDir, "trailing", "body\n")
	writePromptFile(t, projectDir, "vars", "task {{task.id}}")
	writePromptFile(t, projectDir, "outer", "a {{prompt.craft}} b")

	t.Run("expands", func(t *testing.T) {
		cases := []struct{ tmpl, want string }{
			{"no includes here", "no includes here"},
			{"x {{prompt.craft}} y", "x PROJECT y"},
			{"x {{prompt.only-global}} y", "x FALLBACK y"},
			{"x {{prompt.trailing}} y", "x body y"},
			// Inner placeholders are NOT resolved here — that is the template
			// resolver's job, over the combined text.
			{"{{prompt.vars}}", "task {{task.id}}"},
			{"{{prompt.craft}}+{{prompt.only-global}}", "PROJECT+FALLBACK"},
		}
		for _, tc := range cases {
			got, err := ExpandPromptIncludes(tc.tmpl, dirs)
			if err != nil {
				t.Fatalf("ExpandPromptIncludes(%q): %v", tc.tmpl, err)
			}
			if got != tc.want {
				t.Errorf("ExpandPromptIncludes(%q) = %q, want %q", tc.tmpl, got, tc.want)
			}
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name string
			tmpl string
			want []string
		}{
			{"missing file", "{{prompt.nope}}", []string{
				filepath.Join(projectDir, "nope.md"),
				filepath.Join(globalDir, "nope.md"),
			}},
			{"nested include", "{{prompt.outer}}", []string{"outer", "one level deep"}},
			{"bad name", "{{prompt.has space}}", []string{"invalid prompt include name"}},
			{"empty name", "{{prompt.}}", []string{"invalid prompt include name"}},
			{"path traversal", "{{prompt.../secrets}}", []string{"invalid prompt include name"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := ExpandPromptIncludes(tc.tmpl, dirs)
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				for _, want := range tc.want {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q missing %q", err, want)
					}
				}
			})
		}
	})
}

// TestLoadForProjectPromptIncludes checks the loader hook: every reachable
// {{prompt.<name>}} must resolve, and the project tier shadows the global one.
func TestLoadForProjectPromptIncludes(t *testing.T) {
	const projectYml = `
workflows:
  - name: build
    summarizer_prompt: "{{prompt.wrap-up}}"
    steps:
      - name: implementing
        prompt: "{{prompt.craft}}"
        summarization_prompt: "{{prompt.summary-rules}}"
`

	t.Run("resolves with project overriding global", func(t *testing.T) {
		projectDir := setupGlobalAndProject(t, "", map[string]string{
			".sakusen/prompts/craft.md":         "GLOBAL CRAFT",
			".sakusen/prompts/wrap-up.md":       "WRAP",
			".sakusen/prompts/summary-rules.md": "RULES",
		}, projectYml, map[string]string{
			".sakusen/prompts/craft.md": "PROJECT CRAFT",
		})

		cfg, err := LoadForProject(projectDir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		// The loader validates includes; it does NOT bake them into the config
		// (the engine re-reads the files per step).
		wf := cfg.GetWorkflow("build")
		if wf == nil {
			t.Fatal(`workflow "build" missing`)
		}
		if wf.Steps[0].Prompt != "{{prompt.craft}}" {
			t.Errorf("loader must leave the placeholder in place, got %q", wf.Steps[0].Prompt)
		}
		// Precedence is verified through the shared expander with the same dirs
		// the loader used.
		got, err := ExpandPromptIncludes(wf.Steps[0].Prompt, PromptDirs(projectDir))
		if err != nil {
			t.Fatalf("ExpandPromptIncludes: %v", err)
		}
		if got != "PROJECT CRAFT" {
			t.Errorf("got %q, want the project-tier passage", got)
		}
	})

	t.Run("global fallback resolves", func(t *testing.T) {
		projectDir := setupGlobalAndProject(t, "", map[string]string{
			".sakusen/prompts/craft.md":         "GLOBAL CRAFT",
			".sakusen/prompts/wrap-up.md":       "WRAP",
			".sakusen/prompts/summary-rules.md": "RULES",
		}, projectYml, nil)

		if _, err := LoadForProject(projectDir); err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
	})

	t.Run("missing file fails the load", func(t *testing.T) {
		projectDir := setupGlobalAndProject(t, "", nil, `
workflows:
  - name: build
    steps:
      - name: implementing
        prompt: "{{prompt.craft}}"
`, nil)

		_, err := LoadForProject(projectDir)
		if err == nil {
			t.Fatal("expected the missing include to fail the load")
		}
		for _, want := range []string{`workflow "build"`, `step "implementing"`, "prompt",
			filepath.Join(projectDir, ".sakusen", "prompts", "craft.md")} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err, want)
			}
		}
	})
}

// TestDiagnosePromptIncludes verifies `sakusen validate` reports missing
// includes for every templated field, naming the searched paths.
func TestDiagnosePromptIncludes(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want []string
	}{
		{
			name: "step prompt",
			yml: `
workflows:
  - name: build
    steps:
      - name: implementing
        prompt: "{{prompt.craft}}"
`,
			want: []string{`workflow "build"`, `step "implementing"`, "prompt", "craft.md"},
		},
		{
			name: "summarization_prompt",
			yml: `
workflows:
  - name: build
    steps:
      - name: implementing
        prompt: "do it"
        summarization_strategy: summarize_chat
        summarization_prompt: "{{prompt.rules}}"
`,
			want: []string{"summarization_prompt", "rules.md"},
		},
		{
			name: "summarizer_prompt",
			yml: `
workflows:
  - name: build
    summarizer_prompt: "{{prompt.wrap-up}}"
    steps:
      - name: implementing
        prompt: "do it"
`,
			want: []string{"summarizer_prompt", "wrap-up.md"},
		},
		{
			name: "nested include",
			yml: `
workflows:
  - name: build
    steps:
      - name: implementing
        prompt: "{{prompt.outer}}"
`,
			want: []string{"one level deep"},
		},
		{
			name: "bad name",
			yml: `
workflows:
  - name: build
    steps:
      - name: implementing
        prompt: "{{prompt.not ok}}"
`,
			want: []string{"invalid prompt include name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			path := filepath.Join(dir, ".sakusen.yml")
			if err := os.WriteFile(path, []byte(tc.yml), 0644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			// `outer` exists but itself includes another passage — the depth-1
			// rule must reject it rather than recursing.
			writePromptFile(t, filepath.Join(dir, ".sakusen", "prompts"), "outer", "a {{prompt.inner}} b")
			writePromptFile(t, filepath.Join(dir, ".sakusen", "prompts"), "inner", "INNER")

			err := ValidateFile(path)
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}

	t.Run("present files validate cleanly", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := t.TempDir()
		path := filepath.Join(dir, ".sakusen.yml")
		yml := `
merge_conflicts:
  prompt: "{{prompt.conflicts}}"
workflows:
  - name: build
    summarizer_prompt: "{{prompt.wrap-up}}"
    steps:
      - name: implementing
        prompt: "{{prompt.craft}} and {{task.input}}"
`
		if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		promptsDir := filepath.Join(dir, ".sakusen", "prompts")
		writePromptFile(t, promptsDir, "craft", "CRAFT")
		writePromptFile(t, promptsDir, "wrap-up", "WRAP")
		writePromptFile(t, promptsDir, "conflicts", "FIX {{conflict.files}}")

		if err := ValidateFile(path); err != nil {
			t.Fatalf("ValidateFile: %v", err)
		}
	})
}
