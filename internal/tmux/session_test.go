package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no dots", "myproject", "myproject"},
		{"leading dot", ".docs", "_docs"},
		{"multiple dots", "my.project.name", "my_project_name"},
		{"only dot", ".", "_"},
		{"no change needed", "sortie", "sortie"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSessionPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain project", "myproject", "myproject-"},
		{"dot-prefixed project", ".docs", "_docs-"},
		{"project with dots", "my.app", "my_app-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SessionPrefix(tt.input)
			if got != tt.expected {
				t.Errorf("SessionPrefix(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNewSession(t *testing.T) {
	s := NewSession("sortie", "42", "/tmp/work")
	if s.Name != "sortie-42" {
		t.Errorf("expected name sortie-42, got %s", s.Name)
	}
	if s.WorkDir != "/tmp/work" {
		t.Errorf("expected workdir /tmp/work, got %s", s.WorkDir)
	}
}

func TestNewSessionDotPrefix(t *testing.T) {
	s := NewSession(".docs", "7", "/tmp/work")
	if s.Name != "_docs-7" {
		t.Errorf("expected name _docs-7, got %s", s.Name)
	}
}

func TestSetupCommandControlsAgent(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected bool
	}{
		{"empty", "", false},
		{"plain setup", "tmux new-window -t {{session_name}}:1 -n bash", false},
		{"with run_agent", "tmux send-keys -t {{session_name}}:0 'bash {{run_agent}}' C-m", true},
		{"with agent_command", "tmux send-keys -t {{session_name}}:1 '{{agent_command}}' C-m", true},
		{"both vars", "{{run_agent}} and {{agent_command}}", true},
		{"only session_name", "tmux split-window -t {{session_name}}", false},
		{"removed claude_command is inert", "tmux send-keys -t {{session_name}}:0 '{{claude_command}}' C-m", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetupCommandControlsAgent(tt.command)
			if got != tt.expected {
				t.Errorf("SetupCommandControlsAgent(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

// TestRunSetupCommand_SubstitutesVars verifies that RunSetupCommand resolves
// the {{session_name}}, {{worktree_path}}, {{agent_command}}, and
// {{run_agent}} template variables before running the command via `sh -c` in
// the session's WorkDir, that nil vars leave the agent placeholders literal,
// and that an empty command is a no-op.
func TestRunSetupCommand_SubstitutesVars(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	t.Run("all vars substituted", func(t *testing.T) {
		s := NewSession("proj", "42", t.TempDir())
		vars := &SetupVars{AgentCommand: "AGENTCMD", RunAgent: "RUNAGENT"}
		cmd := `echo "{{agent_command}}|{{run_agent}}|{{session_name}}|{{worktree_path}}" > out.txt`
		if err := s.RunSetupCommand(cmd, vars); err != nil {
			t.Fatalf("RunSetupCommand failed: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(s.WorkDir, "out.txt"))
		if err != nil {
			t.Fatalf("read out.txt: %v", err)
		}
		want := "AGENTCMD|RUNAGENT|" + s.Name + "|" + s.WorkDir + "\n"
		if string(data) != want {
			t.Errorf("out.txt = %q, want %q", string(data), want)
		}
	})

	t.Run("nil vars leave agent placeholders literal", func(t *testing.T) {
		s := NewSession("proj", "42", t.TempDir())
		cmd := `echo "{{agent_command}}|{{run_agent}}|{{session_name}}" > out.txt`
		if err := s.RunSetupCommand(cmd, nil); err != nil {
			t.Fatalf("RunSetupCommand failed: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(s.WorkDir, "out.txt"))
		if err != nil {
			t.Fatalf("read out.txt: %v", err)
		}
		want := "{{agent_command}}|{{run_agent}}|" + s.Name + "\n"
		if string(data) != want {
			t.Errorf("out.txt = %q, want %q", string(data), want)
		}
	})

	t.Run("empty command is a no-op", func(t *testing.T) {
		s := NewSession("proj", "42", t.TempDir())
		if err := s.RunSetupCommand("", &SetupVars{}); err != nil {
			t.Fatalf("empty command should be a nil-error no-op, got: %v", err)
		}
		entries, err := os.ReadDir(s.WorkDir)
		if err != nil {
			t.Fatalf("read workdir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("empty command should not write files, found %d entries", len(entries))
		}
	})
}

func TestExtractTaskID(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		input       string
		expected    string
	}{
		{"basic", "sortie", "sortie-42", "42"},
		{"no prefix match", "sortie", "other-42", "other-42"},
		{"prefix only", "sortie", "sortie-", ""},
		{"dot-prefixed project", ".docs", "_docs-42", "42"},
		{"dot-prefixed no match", ".docs", ".docs-42", ".docs-42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTaskID(tt.projectName, tt.input)
			if got != tt.expected {
				t.Errorf("ExtractTaskID(%q, %q) = %q, want %q", tt.projectName, tt.input, got, tt.expected)
			}
		})
	}
}
