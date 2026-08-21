package daemon

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

// interactiveAgent picks the agent record for ad-hoc tmux sessions (continue,
// tmux_direct, restore fallback). Resolution rule: the top-level default agent
// when it is tmux-mode, else the conventional "claude-tmux" slug, else error.

func TestInteractiveAgent_UsesTmuxModeDefaultAgent(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "pair",
		Agents: map[string]config.AgentConfig{
			"pair":        {Mode: config.AgentModeTmux, Command: "run-pair"},
			"claude-tmux": {Mode: config.AgentModeTmux, Command: "run-claude"},
		},
	}

	slug, agent, err := interactiveAgent(cfg)
	if err != nil {
		t.Fatalf("interactiveAgent failed: %v", err)
	}
	if slug != "pair" {
		t.Errorf("expected tmux-mode default agent %q to win, got %q", "pair", slug)
	}
	if agent.Command != "run-pair" {
		t.Errorf("expected the default agent's command, got %q", agent.Command)
	}
}

func TestInteractiveAgent_FallsBackToClaudeTmuxSlug(t *testing.T) {
	// The default agent resolves headless, so the conventional "claude-tmux"
	// slug must be used instead.
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"claude":      {Command: "claude -p"}, // headless implicit default
			"claude-tmux": {Mode: config.AgentModeTmux, Command: "claude"},
		},
	}

	slug, agent, err := interactiveAgent(cfg)
	if err != nil {
		t.Fatalf("interactiveAgent failed: %v", err)
	}
	if slug != interactiveAgentSlug {
		t.Errorf("expected fallback slug %q, got %q", interactiveAgentSlug, slug)
	}
	if agent.Command != "claude" {
		t.Errorf("expected claude-tmux record's command, got %q", agent.Command)
	}
}

// TestInteractiveAgent_NonexistentDefaultFallsThroughToClaudeTmux verifies
// that a default_agent slug with NO record (possible on hand-built configs;
// load-time validation isn't in play here) falls through the chain to the
// conventional "claude-tmux" record when one exists.
func TestInteractiveAgent_NonexistentDefaultFallsThroughToClaudeTmux(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "ghost",
		Agents: map[string]config.AgentConfig{
			"claude-tmux": {Mode: config.AgentModeTmux, Command: "claude"},
		},
	}

	slug, agent, err := interactiveAgent(cfg)
	if err != nil {
		t.Fatalf("interactiveAgent failed: %v", err)
	}
	if slug != interactiveAgentSlug {
		t.Errorf("expected fall-through to %q for a nonexistent default slug, got %q", interactiveAgentSlug, slug)
	}
	if agent.Command != "claude" {
		t.Errorf("expected claude-tmux record's command, got %q", agent.Command)
	}
}

func TestInteractiveAgent_ErrorsWhenNoTmuxAgent(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "no agents at all",
			cfg:  &config.Config{},
		},
		{
			name: "only headless agents",
			cfg: &config.Config{
				DefaultAgent: "worker",
				Agents: map[string]config.AgentConfig{
					"worker": {Command: "run-worker"},
				},
			},
		},
		{
			// End of the chain: the default slug has no record and there is no
			// claude-tmux record to fall through to.
			name: "nonexistent default slug and no claude-tmux record",
			cfg: &config.Config{
				DefaultAgent: "ghost",
				Agents: map[string]config.AgentConfig{
					"worker": {Command: "run-worker"},
				},
			},
		},
		{
			// The slug alone is not enough — the record's mode decides.
			name: "claude-tmux record exists but is headless",
			cfg: &config.Config{
				Agents: map[string]config.AgentConfig{
					"claude-tmux": {Mode: config.AgentModeHeadless, Command: "claude -p"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := interactiveAgent(tt.cfg)
			if err == nil {
				t.Fatal("expected an error when no tmux-mode agent is configured")
			}
			if !strings.Contains(err.Error(), interactiveAgentSlug) {
				t.Errorf("error should name the conventional slug %q for guidance, got: %v", interactiveAgentSlug, err)
			}
		})
	}
}
