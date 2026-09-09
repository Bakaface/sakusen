package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// promptIncludeOpen is the literal prefix of a {{prompt.<name>}} placeholder.
// Used for the cheap "does this string contain an include at all" test and for
// the nested-include check, which must also catch malformed names.
const promptIncludeOpen = "{{prompt."

// promptIncludePattern matches a {{prompt.<name>}} placeholder. The name is
// captured loosely (anything that isn't a brace) so a malformed name produces
// an explicit error instead of silently passing through — unlike the generic
// template pattern, an unresolved prompt include is never acceptable.
var promptIncludePattern = regexp.MustCompile(`\{\{prompt\.([^{}]*)\}\}`)

// validPromptName matches the accepted {{prompt.<name>}} names: letters,
// digits, dashes and underscores. Kebab-case is the convention.
var validPromptName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// PromptDirs returns the ordered search roots for {{prompt.<name>}} includes:
// <projectDir>/.sakusen/prompts first, then ~/.sakusen/prompts. Mirrors the
// project-then-global precedence of workflow lookup, so a project can override
// a globally-shared passage by dropping a same-named file in its own tree.
func PromptDirs(projectDir string) []string {
	var dirs []string
	if projectDir != "" {
		dirs = append(dirs, filepath.Join(projectDir, ".sakusen", "prompts"))
	}
	if globalDir, err := globalSakusenDir(); err == nil {
		global := filepath.Join(globalDir, "prompts")
		// Validating ~/.sakusen.yml itself makes both tiers the same
		// directory; listing it twice would only make error messages noisier.
		if len(dirs) == 0 || dirs[0] != global {
			dirs = append(dirs, global)
		}
	}
	return dirs
}

// ExpandPromptIncludes replaces every {{prompt.<name>}} placeholder in text
// with the contents of <name>.md, looked up in dirs (first hit wins). The
// substitution happens before any other template resolution, so an included
// passage may itself use {{task.*}}, {{steps.*}} and friends.
//
// Includes are one level deep: an included file containing {{prompt.*}} is an
// error, not a recursion. A missing file or a malformed name is an error too —
// a half-resolved prompt reaching an agent is the worst failure mode, so this
// never falls back to leaving the placeholder verbatim.
func ExpandPromptIncludes(text string, dirs []string) (string, error) {
	if !strings.Contains(text, promptIncludeOpen) {
		return text, nil
	}
	var firstErr error
	out := promptIncludePattern.ReplaceAllStringFunc(text, func(match string) string {
		if firstErr != nil {
			return match
		}
		name := match[len(promptIncludeOpen) : len(match)-len("}}")]
		body, err := loadPromptInclude(name, dirs)
		if err != nil {
			firstErr = err
			return match
		}
		if strings.Contains(body, promptIncludeOpen) {
			firstErr = fmt.Errorf("prompt include %q: included files must not contain {{prompt.*}} placeholders (includes are one level deep)", name)
			return match
		}
		return body
	})
	if firstErr != nil {
		return "", firstErr
	}
	// A leftover placeholder means the pattern didn't match a "{{prompt."
	// occurrence — e.g. an unterminated or brace-containing form. Reject it
	// rather than shipping it to an agent.
	if strings.Contains(out, promptIncludeOpen) {
		return "", fmt.Errorf("malformed prompt include near %q", promptIncludeOpen)
	}
	return out, nil
}

// loadPromptInclude reads <name>.md from the first dir that has it, stripping a
// single trailing newline so an inline `Foo {{prompt.x}} bar` doesn't inject a
// stray blank line. Other leading/trailing whitespace is preserved.
func loadPromptInclude(name string, dirs []string) (string, error) {
	if !validPromptName.MatchString(name) {
		return "", fmt.Errorf("invalid prompt include name %q in {{prompt.%s}} (letters, digits, dashes and underscores only)", name, name)
	}
	searched := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		path := filepath.Join(dir, name+".md")
		searched = append(searched, path)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		return strings.TrimSuffix(string(data), "\n"), nil
	}
	return "", fmt.Errorf("prompt include {{prompt.%s}} not found (searched: %s)", name, strings.Join(searched, ", "))
}

// validatePromptIncludes resolves every {{prompt.<name>}} placeholder reachable
// from the resolved config and reports the first failure, naming the workflow,
// step and field so the fix is obvious. Expansion is deliberately NOT persisted
// onto the config: the engine re-reads the files at step-launch time (like
// track context), so editing a shared passage takes effect without a reload.
func validatePromptIncludes(cfg *Config, dirs []string) error {
	check := func(text, where string) error {
		if !strings.Contains(text, promptIncludeOpen) {
			return nil
		}
		if _, err := ExpandPromptIncludes(text, dirs); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		return nil
	}

	if cfg.MergeConflicts.Prompt != "" {
		if err := check(cfg.MergeConflicts.Prompt, "merge_conflicts.prompt"); err != nil {
			return err
		}
	}
	for i := range cfg.Workflows {
		wf := &cfg.Workflows[i]
		if err := check(wf.SummarizerPrompt, fmt.Sprintf("workflow %q: summarizer_prompt", wf.Name)); err != nil {
			return err
		}
		for j := range wf.Steps {
			s := &wf.Steps[j]
			if err := check(s.Prompt, fmt.Sprintf("workflow %q: step %q: prompt", wf.Name, s.Name)); err != nil {
				return err
			}
			if err := check(s.SummarizationPrompt, fmt.Sprintf("workflow %q: step %q: summarization_prompt", wf.Name, s.Name)); err != nil {
				return err
			}
			if !s.IsParallel() {
				continue
			}
			for k := range s.Parallel.Branches {
				b := &s.Parallel.Branches[k]
				if err := check(b.Prompt, fmt.Sprintf("workflow %q: step %q: branch %q: prompt", wf.Name, s.Name, b.Name)); err != nil {
					return err
				}
				if err := check(b.SummarizationPrompt, fmt.Sprintf("workflow %q: step %q: branch %q: summarization_prompt", wf.Name, s.Name, b.Name)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
