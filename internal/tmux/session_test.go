package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
		{"no change needed", "sakusen", "sakusen"},
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
	s := NewSession("sakusen", "42", "my-task", "/tmp/work")
	if s.Name != "sakusen-42-my-task" {
		t.Errorf("expected name sakusen-42-my-task, got %s", s.Name)
	}
	if s.WorkDir != "/tmp/work" {
		t.Errorf("expected workdir /tmp/work, got %s", s.WorkDir)
	}
	if got := NewSession("sakusen", "42", "", "/tmp/work").Name; got != "sakusen-42" {
		t.Errorf("empty slug: expected name sakusen-42, got %s", got)
	}
}

func TestSessionName(t *testing.T) {
	tests := []struct {
		name    string
		project string
		taskID  string
		slug    string
		want    string
	}{
		{"plain slug", "intrum", "580", "rotate-northflank-tokens", "intrum-580-rotate-northflank-tokens"},
		{"empty slug", "intrum", "580", "", "intrum-580"},
		{"slug cleans to empty", "intrum", "580", "---", "intrum-580"},
		{"punctuation only", "intrum", "580", ".:!", "intrum-580"},
		{"uppercase and spaces", "intrum", "580", "Rotate Tokens", "intrum-580-rotate-tokens"},
		{"tmux-hostile characters", "intrum", "580", "fix.login:page", "intrum-580-fix-login-page"},
		{"leading and trailing dashes", "intrum", "580", "-fix-login-", "intrum-580-fix-login"},
		{"non-ascii", "intrum", "580", "café-menu", "intrum-580-caf-menu"},
		{"long slug truncated", "intrum", "580", "rotate-northflank-tokens-and-secrets", "intrum-580-rotate-northflank-tokens"},
		{"dot-prefixed project", ".docs", "7", "fix-login", "_docs-7-fix-login"},
		{"dot project, short slug", ".docs", "7", "fix", "_docs-7-fix"},
		{"punctuation only dots", "p", "1", "...", "p-1"},
		{"empty slug short project", "p", "1", "", "p-1"},
		{"mixed hostile", "intrum", "580", "Fix.Ünicode_Thing", "intrum-580-fix-nicode-thing"},
		{"60-char slug", "intrum", "580", strings.Repeat("abcde-", 10), "intrum-580-abcde-abcde-abcde-abcde"},
	}

	slugRe := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SessionName(tt.project, tt.taskID, tt.slug)
			if got != tt.want {
				t.Fatalf("SessionName(%q, %q, %q) = %q, want %q", tt.project, tt.taskID, tt.slug, got, tt.want)
			}
			bare := SessionPrefix(tt.project) + tt.taskID
			if got == bare {
				return
			}
			slugPart := strings.TrimPrefix(got, bare+"-")
			if !slugRe.MatchString(slugPart) {
				t.Errorf("slug part %q does not match %s", slugPart, slugRe)
			}
			if len(slugPart) > 24 {
				t.Errorf("slug part %q is %d chars, want <= 24", slugPart, len(slugPart))
			}
		})
	}
}

func TestNewSessionDotPrefix(t *testing.T) {
	s := NewSession(".docs", "7", "", "/tmp/work")
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
		s := NewSession("proj", "42", "", t.TempDir())
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
		s := NewSession("proj", "42", "", t.TempDir())
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
		s := NewSession("proj", "42", "", t.TempDir())
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

// TestTaskIDFromSessionName pins the guard that keeps destructive sweeps off
// unrelated sessions: sharing a project's "<name>-" prefix is not enough, the
// suffix must parse as a numeric task ID (sakusen#357).
func TestTaskIDFromSessionName(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		sessionName string
		wantID      int64
		wantOK      bool
	}{
		{"task session", "nexus", "nexus-42", 42, true},
		{"slugged task session", "nexus", "nexus-42-implement", 42, true},
		{"long slugged task session", "intrum", "intrum-580-rotate-northflank-tokens", 580, true},
		{"unrelated session sharing prefix", "nexus", "nexus-em", 0, false},
		{"unrelated multi-segment session", "nexus", "nexus-personal-notes", 0, false},
		{"different project", "nexus", "other-42", 0, false},
		{"prefix only", "nexus", "nexus-", 0, false},
		{"bare project name", "nexus", "nexus", 0, false},
		{"dot-prefixed project", ".docs", "_docs-7", 7, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotOK := TaskIDFromSessionName(tt.projectName, tt.sessionName)
			if gotID != tt.wantID || gotOK != tt.wantOK {
				t.Errorf("TaskIDFromSessionName(%q, %q) = (%d, %t), want (%d, %t)",
					tt.projectName, tt.sessionName, gotID, gotOK, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestExtractTaskID(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		input       string
		expected    string
	}{
		{"basic", "sakusen", "sakusen-42", "42"},
		{"no prefix match", "sakusen", "other-42", "other-42"},
		{"prefix only", "sakusen", "sakusen-", ""},
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

// Attached reports 0 clients for a detached session, and — because its target
// is an EXACT name match — refuses to answer for a session name that does not
// exist even when a longer name shares its prefix. Callers read that error as
// "attached", which is what stops hibernation from acting on the wrong
// session.
func TestSessionAttached(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	base := "sakusen-attached-test"
	live := &Session{Name: base + "10", WorkDir: t.TempDir()}
	if err := live.Create("sleep", "600"); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	t.Cleanup(func() { live.Kill() })

	attached, err := live.Attached()
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if attached {
		t.Error("a detached session must report no attached clients")
	}

	// "…-1" does not exist; only "…-10" does. A prefix-matching query would
	// happily answer for the latter.
	missing := &Session{Name: base + "1"}
	if _, err := missing.Attached(); err == nil {
		t.Error("expected Attached to fail for a name that only matches by prefix")
	}
}

func TestSwitchClientCommandExactTarget(t *testing.T) {
	got := SwitchClientCommand("sakusen-proj-5").Args
	want := []string{"tmux", "switch-client", "-t", "=sakusen-proj-5"}
	if !slices.Equal(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

// createTaskSessions starts a detached session for each task ID under project
// and kills them all when the test ends.
func createTaskSessions(t *testing.T, project string, taskIDs ...string) {
	t.Helper()
	for _, id := range taskIDs {
		s := NewSession(project, id, "", t.TempDir())
		if err := s.Create("sleep", "600"); err != nil {
			t.Fatalf("failed to create session %s: %v", s.Name, err)
		}
		t.Cleanup(func() { s.Kill() })
	}
}

// liveSessionNames scans every session by name, independent of tmux's
// target resolution.
func liveSessionNames(t *testing.T) map[string]bool {
	t.Helper()
	sessions, err := ListSessions("")
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	names := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		names[s.Name] = true
	}
	return names
}

func TestSessionExistsIsExact(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-exists-test"
	createTaskSessions(t, project, "560")

	if NewSession(project, "56", "", "").Exists() {
		t.Error("Exists must not match a session that only shares the name as a prefix")
	}

	createTaskSessions(t, project, "56")
	if !NewSession(project, "56", "", "").Exists() {
		t.Error("Exists must report a session with the exact name")
	}
}

func TestSessionKillIsExact(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-kill-test"
	createTaskSessions(t, project, "560")

	if err := NewSession(project, "56", "", "").Kill(); err == nil {
		t.Error("expected Kill to fail for a name that only matches by prefix")
	}
	if !liveSessionNames(t)[project+"-560"] {
		t.Error("Kill of a missing session must not kill a prefix-matching one")
	}
}

func TestPaneTargetsAreExact(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-pane-test"
	createTaskSessions(t, project, "560")

	missing := NewSession(project, "56", "", "")
	if missing.IsAlive() {
		t.Error("IsAlive must not report on a session that only matches by prefix")
	}
	if _, err := missing.CapturePane(10); err == nil {
		t.Error("expected CapturePane to fail for a name that only matches by prefix")
	}
	if err := missing.SendKeys("echo hi"); err == nil {
		t.Error("expected SendKeys to fail for a name that only matches by prefix")
	}
	if err := missing.PipePane(filepath.Join(t.TempDir(), "pane.log")); err == nil {
		t.Error("expected PipePane to fail for a name that only matches by prefix")
	}

	// The exact form must still resolve a session that does exist.
	live := NewSession(project, "560", "", "")
	if !live.IsAlive() {
		t.Error("IsAlive must report a live session with the exact name")
	}
	if _, err := live.CapturePane(10); err != nil {
		t.Errorf("CapturePane on the exact session: %v", err)
	}
}

// startSession starts a detached session with the exact name and kills it when
// the test ends.
func startSession(t *testing.T, name string) *Session {
	t.Helper()
	s := &Session{Name: name, WorkDir: t.TempDir()}
	if err := s.Create("sleep", "600"); err != nil {
		t.Fatalf("failed to create session %s: %v", name, err)
	}
	t.Cleanup(func() { s.Kill() })
	return s
}

func TestFindSession(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	t.Run("full name live", func(t *testing.T) {
		project := "sakusen-find-full-test"
		startSession(t, project+"-56-x")
		startSession(t, project+"-56")
		if got := FindSession(project, "56", "x", "").Name; got != project+"-56-x" {
			t.Errorf("FindSession = %q, want the full name", got)
		}
	})

	t.Run("only legacy live", func(t *testing.T) {
		project := "sakusen-find-legacy-test"
		startSession(t, project+"-56")
		startSession(t, project+"-560-x")
		if got := FindSession(project, "56", "x", "").Name; got != project+"-56" {
			t.Errorf("FindSession = %q, want the legacy name", got)
		}
	})

	t.Run("neither live", func(t *testing.T) {
		project := "sakusen-find-none-test"
		startSession(t, project+"-560")
		startSession(t, project+"-56-xy")
		s := FindSession(project, "56", "x", "")
		if s.Name != project+"-56-x" {
			t.Errorf("FindSession = %q, want the full name", s.Name)
		}
		if s.Exists() {
			t.Errorf("FindSession(%q).Exists() = true, want false", s.Name)
		}
	})
}

func TestKillSessionsForTaskIsExact(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-killtask-test"
	for _, suffix := range []string{"56-x", "56", "560", "560-x", "569"} {
		startSession(t, project+"-"+suffix)
	}

	if err := KillSessionsForTask(project, "56", "x"); err != nil {
		t.Fatalf("KillSessionsForTask: %v", err)
	}

	names := liveSessionNames(t)
	for _, suffix := range []string{"56-x", "56"} {
		if names[project+"-"+suffix] {
			t.Errorf("session %s-%s must be killed", project, suffix)
		}
	}
	for _, suffix := range []string{"560", "560-x", "569"} {
		if !names[project+"-"+suffix] {
			t.Errorf("session %s-%s must survive killing task 56", project, suffix)
		}
	}
}

func TestKillSessionsForTaskMissingSession(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-killtask-missing-test"
	createTaskSessions(t, project, "560")

	if err := KillSessionsForTask(project, "56", "x"); err != nil {
		t.Errorf("a missing session must not be an error, got %v", err)
	}
	if !liveSessionNames(t)[project+"-560"] {
		t.Error("session for task 560 must survive killing task 56")
	}
}

func TestListTaskSessionsFullName(t *testing.T) {
	if !IsAvailable() {
		t.Skip("tmux is not installed")
	}

	project := "sakusen-listtask-test"
	name := SessionName(project, "580", "rotate-northflank-tokens")
	startSession(t, name)

	sessions, err := ListTaskSessions(project)
	if err != nil {
		t.Fatalf("ListTaskSessions: %v", err)
	}
	if s := sessions[580]; s == nil || s.Name != name {
		t.Errorf("sessions[580] = %v, want %q", s, name)
	}
}

func TestAttachCommandsTargetExactSession(t *testing.T) {
	cmds := map[string]*exec.Cmd{
		"AttachCommand":       AttachCommand("proj-56"),
		"SwitchClientCommand": SwitchClientCommand("proj-56"),
		"NestedAttachCommand": NestedAttachCommand("proj-56"),
	}
	for name, cmd := range cmds {
		if got := cmd.Args[len(cmd.Args)-1]; got != "=proj-56" {
			t.Errorf("%s target = %q, want %q", name, got, "=proj-56")
		}
	}
}
