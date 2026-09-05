package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjectYml writes a .sakusen.yml into a fresh project dir under an
// isolated HOME and returns the project dir.
func writeProjectYml(t *testing.T, yml string) string {
	t.Helper()
	isolateHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// roleTestAgents is the agents: block role tests point at — two headless
// records and one tmux record for the headless-only rejections.
const roleTestAgents = `
agents:
  claude:
    command: "run claude"
  codex:
    command: "run codex"
  interactive:
    mode: tmux
    command: "run tmux"
`

// TestMergeConflictsBlockParsed verifies the top-level merge_conflicts: block
// reaches the merged Config from the project tier, and that timeout defaults
// to DefaultMergeConflictTimeout when omitted.
func TestMergeConflictsBlockParsed(t *testing.T) {
	dir := writeProjectYml(t, roleTestAgents+`
merge_conflicts:
  agent: codex
  timeout: 25m
  prompt: |
    Fix {{conflict.files}}
`)

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.MergeConflicts.Agent != "codex" {
		t.Errorf("MergeConflicts.Agent = %q, want %q", cfg.MergeConflicts.Agent, "codex")
	}
	if got := cfg.MergeConflicts.EffectiveTimeout(); got != "25m" {
		t.Errorf("EffectiveTimeout() = %q, want %q", got, "25m")
	}
	if !strings.Contains(cfg.MergeConflicts.Prompt, "{{conflict.files}}") {
		t.Errorf("MergeConflicts.Prompt = %q, want the configured override", cfg.MergeConflicts.Prompt)
	}

	empty := MergeConflictsConfig{}
	if got := empty.EffectiveTimeout(); got != DefaultMergeConflictTimeout {
		t.Errorf("EffectiveTimeout() with no timeout = %q, want %q", got, DefaultMergeConflictTimeout)
	}
}

// TestMergeConflictsGlobalTierAndWholesaleMerge verifies the block loads from
// the global ~/.sakusen.yml and that a project-tier block replaces it
// entirely — an omitted field falls back to its default, never to the global
// value (same semantics as summarizer:).
func TestMergeConflictsGlobalTierAndWholesaleMerge(t *testing.T) {
	globalYml := roleTestAgents + "merge_conflicts:\n  agent: codex\n  timeout: 20m\n"

	t.Run("global tier alone", func(t *testing.T) {
		dir := setupGlobalAndProject(t, globalYml, nil, "max_workers: 2\n", nil)
		cfg, err := LoadForProject(dir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		if cfg.MergeConflicts.Agent != "codex" {
			t.Errorf("MergeConflicts.Agent = %q, want the global tier's %q", cfg.MergeConflicts.Agent, "codex")
		}
		if got := cfg.MergeConflicts.EffectiveTimeout(); got != "20m" {
			t.Errorf("EffectiveTimeout() = %q, want the global tier's %q", got, "20m")
		}
	})

	t.Run("project block replaces the global one wholesale", func(t *testing.T) {
		dir := setupGlobalAndProject(t, globalYml, nil, "merge_conflicts:\n  agent: claude\n", nil)
		cfg, err := LoadForProject(dir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		if cfg.MergeConflicts.Agent != "claude" {
			t.Errorf("MergeConflicts.Agent = %q, want the project tier's %q", cfg.MergeConflicts.Agent, "claude")
		}
		if got := cfg.MergeConflicts.EffectiveTimeout(); got != DefaultMergeConflictTimeout {
			t.Errorf("EffectiveTimeout() = %q, want the default %q (the project block replaces the global block wholesale)", got, DefaultMergeConflictTimeout)
		}
	})
}

// TestMergeConflictAgentRemovedKey verifies the removed top-level
// merge_conflict_agent: key fails the load with a migration error pointing at
// merge_conflicts.agent.
func TestMergeConflictAgentRemovedKey(t *testing.T) {
	dir := writeProjectYml(t, roleTestAgents+"merge_conflict_agent: codex\n")

	_, err := LoadForProject(dir)
	if err == nil {
		t.Fatal("expected a removed-key error for merge_conflict_agent:")
	}
	if !strings.Contains(err.Error(), "merge_conflicts:") {
		t.Errorf("error = %v, want it to point at the merge_conflicts: block", err)
	}
}

// TestRoleAgentRefValidation covers load-time rejection of role agent refs
// that don't resolve or aren't headless, across both role blocks.
func TestRoleAgentRefValidation(t *testing.T) {
	tests := []struct {
		name    string
		yml     string
		wantErr string
	}{
		{"merge_conflicts unknown slug", "merge_conflicts:\n  agent: nope\n", "unknown agent"},
		{"merge_conflicts tmux slug", "merge_conflicts:\n  agent: interactive\n", "must be a headless agent"},
		{"summarizer unknown slug", "summarizer:\n  agent: nope\n", "unknown agent"},
		{"summarizer tmux slug", "summarizer:\n  agent: interactive\n", "must be a headless agent"},
		{"slug_agent unknown slug", "summarizer:\n  slug_agent: nope\n", "unknown agent"},
		{"slug_agent tmux slug", "summarizer:\n  slug_agent: interactive\n", "must be a headless agent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeProjectYml(t, roleTestAgents+tt.yml)
			_, err := LoadForProject(dir)
			if err == nil {
				t.Fatalf("expected a load error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRoleBlockFileLocalValidation covers the rules decidable from a single
// file: the agent/command exclusions and the resolver timeout syntax. Each is
// checked through both the full load path and `sakusen validate`.
func TestRoleBlockFileLocalValidation(t *testing.T) {
	tests := []struct {
		name    string
		yml     string
		wantErr string
	}{
		{
			"summarizer agent and command",
			"summarizer:\n  agent: claude\n  command: \"summarize\"\n",
			"mutually exclusive",
		},
		{
			"summarizer slug_agent and slug_command",
			"summarizer:\n  slug_agent: claude\n  slug_command: \"slugify\"\n",
			"mutually exclusive",
		},
		{
			"merge_conflicts invalid timeout",
			"merge_conflicts:\n  timeout: 10 minutes\n",
			"invalid duration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yml := roleTestAgents + tt.yml
			dir := writeProjectYml(t, yml)

			_, err := LoadForProject(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("LoadForProject error = %v, want it to contain %q", err, tt.wantErr)
			}

			err = ValidateFile(filepath.Join(dir, ".sakusen.yml"))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateFile error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	t.Run("mixed agent and slug_command is legal", func(t *testing.T) {
		dir := writeProjectYml(t, roleTestAgents+"summarizer:\n  agent: claude\n  slug_command: \"slugify\"\n")
		cfg, err := LoadForProject(dir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		inv, ok := cfg.SummarizerSlugInvocation()
		if !ok {
			t.Fatal("SummarizerSlugInvocation() reported nothing configured")
		}
		if inv.UsesAgent() || inv.Command != "slugify" {
			t.Errorf("slug invocation = %+v, want the bare slug_command", inv)
		}
	})
}

// TestMergeConflictAgentForCascade pins the resolver's agent cascade:
// merge_conflicts.agent → workflow.agent → default_agent → "claude", with the
// tmux→headless-claude fallback applying to the lower tiers only.
func TestMergeConflictAgentForCascade(t *testing.T) {
	headless := AgentConfig{Command: "headless"}
	tmuxAgent := AgentConfig{Mode: AgentModeTmux, Command: "tmux"}
	registry := map[string]AgentConfig{
		"claude": headless,
		"codex":  headless,
		"wf":     headless,
		"tmuxy":  tmuxAgent,
	}
	wf := &WorkflowConfig{Name: "w", Agent: "wf"}

	t.Run("explicit role agent wins", func(t *testing.T) {
		cfg := &Config{Agents: registry, MergeConflicts: MergeConflictsConfig{Agent: "codex"}}
		slug, _, err := cfg.MergeConflictAgentFor(wf)
		if err != nil || slug != "codex" {
			t.Errorf("MergeConflictAgentFor() = (%q, %v), want codex", slug, err)
		}
	})

	t.Run("falls back to the workflow agent", func(t *testing.T) {
		cfg := &Config{Agents: registry}
		slug, _, err := cfg.MergeConflictAgentFor(wf)
		if err != nil || slug != "wf" {
			t.Errorf("MergeConflictAgentFor() = (%q, %v), want wf", slug, err)
		}
	})

	t.Run("falls back to default_agent then claude", func(t *testing.T) {
		cfg := &Config{Agents: registry, DefaultAgent: "codex"}
		slug, _, err := cfg.MergeConflictAgentFor(nil)
		if err != nil || slug != "codex" {
			t.Errorf("MergeConflictAgentFor() = (%q, %v), want codex", slug, err)
		}

		cfg = &Config{Agents: registry}
		slug, _, err = cfg.MergeConflictAgentFor(nil)
		if err != nil || slug != DefaultAgentSlug {
			t.Errorf("MergeConflictAgentFor() = (%q, %v), want %q", slug, err, DefaultAgentSlug)
		}
	})

	t.Run("tmux workflow agent falls back to headless claude", func(t *testing.T) {
		cfg := &Config{Agents: registry}
		slug, _, err := cfg.MergeConflictAgentFor(&WorkflowConfig{Name: "w", Agent: "tmuxy"})
		if err != nil || slug != DefaultAgentSlug {
			t.Errorf("MergeConflictAgentFor() = (%q, %v), want the %q fallback", slug, err, DefaultAgentSlug)
		}
	})

	t.Run("explicit tmux role agent errors instead of falling back", func(t *testing.T) {
		cfg := &Config{Agents: registry, MergeConflicts: MergeConflictsConfig{Agent: "tmuxy"}}
		_, _, err := cfg.MergeConflictAgentFor(wf)
		if err == nil || !strings.Contains(err.Error(), "requires a headless agent") {
			t.Errorf("error = %v, want the headless-required error", err)
		}
	})

	t.Run("explicit unknown role agent errors", func(t *testing.T) {
		cfg := &Config{Agents: registry, MergeConflicts: MergeConflictsConfig{Agent: "nope"}}
		_, _, err := cfg.MergeConflictAgentFor(wf)
		if err == nil || !strings.Contains(err.Error(), "unknown agent") {
			t.Errorf("error = %v, want the unknown-agent error", err)
		}
	})
}

// TestSummarizerAgentInvocation verifies that an `agent:`-selected summarizer
// reports as configured and resolves to the agent's command and env, and that
// alias / variant slugs are accepted as role targets.
func TestSummarizerAgentInvocation(t *testing.T) {
	dir := writeProjectYml(t, `
agents:
  claude:
    command: "run claude"
    env:
      BASE: keep
    variants:
      haiku:
        env:
          ANTHROPIC_MODEL: haiku
agent_aliases:
  cheap: claude:haiku
merge_conflicts:
  agent: cheap
summarizer:
  agent: claude:haiku
  slug_agent: cheap
  max_prompt_bytes: 4096
`)

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if !cfg.Summarizer.Configured() {
		t.Error("Configured() = false, want true with summarizer.agent set")
	}
	if !cfg.Summarizer.SlugConfigured() {
		t.Error("SlugConfigured() = false, want true with summarizer.slug_agent set")
	}

	inv, ok := cfg.SummarizerInvocation()
	if !ok {
		t.Fatal("SummarizerInvocation() reported nothing configured")
	}
	if !inv.UsesAgent() || inv.AgentSlug != "claude:haiku" {
		t.Errorf("invocation = %+v, want the claude:haiku agent", inv)
	}
	if inv.Command != "run claude" {
		t.Errorf("invocation command = %q, want the agent record's command", inv.Command)
	}
	if inv.Env["ANTHROPIC_MODEL"] != "haiku" || inv.Env["BASE"] != "keep" {
		t.Errorf("invocation env = %v, want the variant's merged env", inv.Env)
	}

	slugInv, ok := cfg.SummarizerSlugInvocation()
	if !ok {
		t.Fatal("SummarizerSlugInvocation() reported nothing configured")
	}
	if slugInv.AgentSlug != "cheap" {
		t.Errorf("slug invocation agent = %q, want the alias %q", slugInv.AgentSlug, "cheap")
	}

	if _, _, err := cfg.MergeConflictAgentFor(nil); err != nil {
		t.Errorf("MergeConflictAgentFor with an alias target: %v", err)
	}
}

// TestSummarizerSlugAgentFallsBackToAgent verifies the slug side inherits
// `agent:` when neither slug_agent nor slug_command is set.
func TestSummarizerSlugAgentFallsBackToAgent(t *testing.T) {
	s := SummarizerConfig{Agent: "claude"}
	if got := s.EffectiveSlugAgent(); got != "claude" {
		t.Errorf("EffectiveSlugAgent() = %q, want %q", got, "claude")
	}
	if got := s.EffectiveSlugCommand(); got != "" {
		t.Errorf("EffectiveSlugCommand() = %q, want empty (the slug side runs the agent)", got)
	}

	// A slug-side command redirect wins over the shared agent.
	s = SummarizerConfig{Agent: "claude", SlugCommand: "slugify"}
	if got := s.EffectiveSlugAgent(); got != "" {
		t.Errorf("EffectiveSlugAgent() = %q, want empty (slug_command redirects the slug call)", got)
	}
	if got := s.EffectiveSlugCommand(); got != "slugify" {
		t.Errorf("EffectiveSlugCommand() = %q, want %q", got, "slugify")
	}
}
