package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/runner"
)

// scaffoldAgentFiles is the exact set of files `sakusen init` copies into
// .sakusen/agents/ (mirrors cmd/sakusen/scaffold/agents/).
var scaffoldAgentFiles = []string{
	"claude-chat-log.sh",
	"claude-headless.sh",
	"claude-settings.json",
	"claude-stream-format.sh",
	"claude-tmux.sh",
}

// setupInitRepo creates a fresh git repo in a temp dir, chdirs into it, and
// isolates HOME/XDG_CONFIG_HOME so the developer's real global config cannot
// leak into config loads. Skips when git is unavailable.
func setupInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateGlobalConfig(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Chdir(dir)
	return dir
}

// TestInitCmd_HappyPath verifies that `sakusen init` in a fresh git repo writes
// .sakusen.yml, scaffolds exactly the embedded agent files with the right
// permissions (0755 for scripts, 0644 otherwise), and adds the runtime
// artifacts to .gitignore.
func TestInitCmd_HappyPath(t *testing.T) {
	dir := setupInitRepo(t)

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".sakusen.yml")); err != nil {
		t.Errorf(".sakusen.yml not created: %v", err)
	}

	agentsDir := filepath.Join(dir, ".sakusen", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		t.Fatalf("read agents dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(scaffoldAgentFiles) {
		t.Errorf("agents dir = %v, want exactly %v", got, scaffoldAgentFiles)
	}
	for _, name := range scaffoldAgentFiles {
		info, err := os.Stat(filepath.Join(agentsDir, name))
		if err != nil {
			t.Errorf("missing scaffolded file %s: %v", name, err)
			continue
		}
		want := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			want = 0755
		}
		if mode := info.Mode() & 0777; mode != want {
			t.Errorf("%s mode = %o, want %o", name, mode, want)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		lines[l] = true
	}
	for _, want := range []string{".sakusen/", runner.OutputLogFileName} {
		if !lines[want] {
			t.Errorf(".gitignore missing line %q, got:\n%s", want, data)
		}
	}
}

// TestInitCmd_ScaffoldedConfigLoads verifies that the scaffolded .sakusen.yml
// is loadable and declares the agents and summarizer the scaffold template
// promises.
func TestInitCmd_ScaffoldedConfigLoads(t *testing.T) {
	dir := setupInitRepo(t)

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	cfg, err := config.LoadForProject(dir)
	if err != nil {
		t.Fatalf("scaffolded config failed to load: %v", err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("default_agent = %q, want %q", cfg.DefaultAgent, "claude")
	}

	claude, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatalf("agents missing %q, got %v", "claude", cfg.Agents)
	}
	if claude.Mode != "headless" {
		t.Errorf("claude mode = %q, want %q", claude.Mode, "headless")
	}
	if claude.Command != `"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-headless.sh"` {
		t.Errorf("claude command = %q", claude.Command)
	}

	tmuxAgent, ok := cfg.Agents["claude-tmux"]
	if !ok {
		t.Fatalf("agents missing %q, got %v", "claude-tmux", cfg.Agents)
	}
	if tmuxAgent.Mode != "tmux" {
		t.Errorf("claude-tmux mode = %q, want %q", tmuxAgent.Mode, "tmux")
	}
	if tmuxAgent.Command != `"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-tmux.sh"` {
		t.Errorf("claude-tmux command = %q", tmuxAgent.Command)
	}
	if tmuxAgent.ResumeCommand != `claude --dangerously-skip-permissions --resume "$SAKUSEN_SESSION_ID"` {
		t.Errorf("claude-tmux resume_command = %q", tmuxAgent.ResumeCommand)
	}
	if tmuxAgent.ChatLogCommand != `"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-chat-log.sh"` {
		t.Errorf("claude-tmux chat_log_command = %q", tmuxAgent.ChatLogCommand)
	}

	if cfg.Summarizer.Command != "claude -p --output-format text --model haiku --dangerously-skip-permissions" {
		t.Errorf("summarizer command = %q", cfg.Summarizer.Command)
	}
	if cfg.Summarizer.MaxPromptBytes != 380000 {
		t.Errorf("summarizer max_prompt_bytes = %d, want 380000", cfg.Summarizer.MaxPromptBytes)
	}
}

// TestInitCmd_NeverOverwritesUserFiles verifies that a pre-existing agent
// script survives init untouched while the remaining files are scaffolded.
func TestInitCmd_NeverOverwritesUserFiles(t *testing.T) {
	dir := setupInitRepo(t)

	agentsDir := filepath.Join(dir, ".sakusen", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := "# user-owned sentinel\n"
	userFile := filepath.Join(agentsDir, "claude-headless.sh")
	if err := os.WriteFile(userFile, []byte(sentinel), 0755); err != nil {
		t.Fatal(err)
	}

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	data, err := os.ReadFile(userFile)
	if err != nil {
		t.Fatalf("read user file: %v", err)
	}
	if string(data) != sentinel {
		t.Errorf("user-owned file was overwritten: %q", data)
	}
	for _, name := range scaffoldAgentFiles {
		if name == "claude-headless.sh" {
			continue
		}
		if _, err := os.Stat(filepath.Join(agentsDir, name)); err != nil {
			t.Errorf("expected %s to be scaffolded: %v", name, err)
		}
	}
}

// TestInitCmd_ExistingConfig verifies that init is a nil-error no-op when
// .sakusen.yml already exists: it must not scaffold the agents directory.
func TestInitCmd_ExistingConfig(t *testing.T) {
	dir := setupInitRepo(t)

	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte("max_workers: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init with existing config should return nil, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".sakusen")); !os.IsNotExist(err) {
		t.Errorf(".sakusen dir should not be created on early return, stat err = %v", err)
	}
}

// TestInitCmd_NotGitRepo verifies that init refuses to run outside a git
// repository.
func TestInitCmd_NotGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateGlobalConfig(t)
	t.Chdir(t.TempDir())

	err := initCmd.RunE(initCmd, nil)
	if err == nil {
		t.Fatal("expected error outside a git repo")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %v, want 'not a git repository'", err)
	}
}

// TestEnsureGitignoreEntries verifies the .gitignore edit semantics: a missing
// file is created with the entries, a file without a trailing newline gets one
// before entries are appended, fully-present entries leave the file
// byte-identical, and partial presence appends only the missing entries.
func TestEnsureGitignoreEntries(t *testing.T) {
	tests := []struct {
		name     string
		existing *string // nil = no .gitignore
		entries  []string
		want     string
	}{
		{
			name:    "missing file created with entries",
			entries: []string{".sakusen/", "X"},
			want:    ".sakusen/\nX\n",
		},
		{
			name:     "no trailing newline gets newline before append",
			existing: strPtr("node_modules"),
			entries:  []string{".sakusen/"},
			want:     "node_modules\n.sakusen/\n",
		},
		{
			name:     "entries already present leaves file byte-identical",
			existing: strPtr("a\n.sakusen/\nX\n"),
			entries:  []string{".sakusen/", "X"},
			want:     "a\n.sakusen/\nX\n",
		},
		{
			name:     "partial presence appends only missing",
			existing: strPtr(".sakusen/\n"),
			entries:  []string{".sakusen/", "X"},
			want:     ".sakusen/\nX\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			if tt.existing != nil {
				if err := os.WriteFile(path, []byte(*tt.existing), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensureGitignoreEntries(dir, tt.entries); err != nil {
				t.Fatalf("ensureGitignoreEntries: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read .gitignore: %v", err)
			}
			if string(data) != tt.want {
				t.Errorf(".gitignore = %q, want %q", data, tt.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestInitCmd_ScaffoldedConfigPassesStrictValidation runs the scaffolded
// .sakusen.yml through config.ValidateFile — the strict (KnownFields) path
// `sakusen validate` uses. LoadForProject silently drops unknown keys, so this
// is the only check that would catch a typo'd key shipped in the scaffold
// template.
func TestInitCmd_ScaffoldedConfigPassesStrictValidation(t *testing.T) {
	dir := setupInitRepo(t)

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	if err := config.ValidateFile(filepath.Join(dir, ".sakusen.yml")); err != nil {
		t.Errorf("scaffolded .sakusen.yml failed strict validation: %v", err)
	}
}
