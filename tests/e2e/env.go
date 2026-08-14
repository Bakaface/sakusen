//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sortieBinPath is set by TestMain once after building the binary.
var sortieBinPath string

// repoRoot is computed once by TestMain.
var repoRoot string

// Env holds the per-test environment: paths, daemon handle, and the testing.T.
type Env struct {
	t            *testing.T
	ProjectDir   string // isolated git repo for this test
	XDGDir       string // XDG_CONFIG_HOME for this test
	ResponsesDir string // $E2E_RESPONSES_DIR — points at testdata/<scenario>/
	StubLog      string // path to per-test stub invocation log
	StubPath     string // absolute path to stub-agent.sh
	PromptDir    string // $E2E_PROMPT_CAPTURE_DIR — per-invocation step-prompt capture
	daemonCmd    *exec.Cmd
	daemonLog    string // path to daemon stdout+stderr log
}

// setupE2E initializes a full Sortie environment for the given scenario name.
// It starts the daemon, initializes a git repo, and registers cleanup.
// Tests must call e.WriteSortieYAML before creating any tasks.
func setupE2E(t *testing.T, scenario string) *Env {
	t.Helper()

	// macOS unix sockets are limited to 104 bytes (sockaddr_un.sun_path).
	// t.TempDir paths include the full test name and can exceed the limit, e.g.
	// /var/folders/.../TestStepFailureAndRetry.../001/sortie/daemon.sock. Use a
	// short prefix under /tmp instead and register manual cleanup.
	xdgDir, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatalf("mkdir xdg temp: %v", err)
	}
	projectDir, err := os.MkdirTemp("/tmp", "p")
	if err != nil {
		t.Fatalf("mkdir project temp: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("KEEP_E2E_TMPDIR") != "" {
			t.Logf("KEEP_E2E_TMPDIR: XDGDir=%s ProjectDir=%s", xdgDir, projectDir)
			return
		}
		_ = os.RemoveAll(xdgDir)
		_ = os.RemoveAll(projectDir)
	})
	stubLog := filepath.Join(xdgDir, "stub.log")
	responsesDir := filepath.Join(repoRoot, "tests", "e2e", "testdata", scenario)
	stubPath := filepath.Join(repoRoot, "tests", "e2e", "stub-agent.sh")

	// Propagate test-specific env via t.Setenv (restored on cleanup automatically)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("HOME", xdgDir)
	t.Setenv("E2E_RESPONSES_DIR", responsesDir)
	t.Setenv("SORTIE_E2E_LOG", stubLog)
	promptDir := filepath.Join(xdgDir, "prompts")
	if err := os.MkdirAll(promptDir, 0755); err != nil {
		t.Fatalf("mkdir prompt capture dir: %v", err)
	}
	t.Setenv("E2E_PROMPT_CAPTURE_DIR", promptDir)
	// SORTIE_E2E_BIN lets stub hooks (e.g. awaiting_children's spawn hook)
	// shell out to the same sortie binary the daemon was launched from.
	// The hooks need to call back into the daemon via CLI commands like
	// `sortie create` / `sortie wait-for-tasks`, but the binary lives in
	// a one-off tmp dir built by main_test.go and is not on PATH.
	t.Setenv("SORTIE_E2E_BIN", sortieBinPath)

	e := &Env{
		t:            t,
		ProjectDir:   projectDir,
		XDGDir:       xdgDir,
		ResponsesDir: responsesDir,
		StubLog:      stubLog,
		StubPath:     stubPath,
		PromptDir:    promptDir,
		daemonLog:    filepath.Join(xdgDir, "daemon.log"),
	}

	// Initialize git repo with an initial commit on main.
	// The .gitignore excludes .sortie/ (daemon-managed worktree state) so that
	// git status on the project root remains clean — otherwise the workflow's
	// merge step refuses to run with "target branch has pending changes".
	e.mustRun(projectDir, "git", "init", "-b", "main")
	e.mustRun(projectDir, "git", "config", "user.email", "test@e2e.local")
	e.mustRun(projectDir, "git", "config", "user.name", "E2E Test")
	if err := os.WriteFile(filepath.Join(projectDir, ".gitkeep"), nil, 0644); err != nil {
		t.Fatalf("write .gitkeep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".gitignore"), []byte(".sortie/\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	e.mustRun(projectDir, "git", "add", ".gitkeep", ".gitignore")
	e.mustRun(projectDir, "git", "commit", "-m", "initial")

	// Verify we are on main.
	out := e.mustRunOut(projectDir, "git", "branch", "--show-current")
	if strings.TrimSpace(out) != "main" {
		t.Fatalf("expected git branch main, got %q", strings.TrimSpace(out))
	}

	// Start daemon and register stop cleanup immediately, so a failed
	// WaitDaemonReady does not leak the daemon process.
	e.startDaemon()
	t.Cleanup(func() {
		// Attempt graceful stop.
		stopCmd := exec.Command(sortieBinPath, "daemon", "stop")
		stopCmd.Env = os.Environ()
		stopCmd.Dir = projectDir
		_ = stopCmd.Run()

		// Give 500ms grace, then kill.
		timer := time.NewTimer(500 * time.Millisecond)
		done := make(chan struct{})
		go func() {
			if e.daemonCmd.Process != nil {
				_ = e.daemonCmd.Wait()
			}
			close(done)
		}()
		select {
		case <-done:
		case <-timer.C:
			if e.daemonCmd.Process != nil {
				_ = e.daemonCmd.Process.Kill()
			}
		}
		timer.Stop()
	})

	// Wait for daemon socket.
	socketPath := filepath.Join(xdgDir, "sortie", "daemon.sock")
	e.WaitDaemonReady(socketPath, 5*time.Second)

	return e
}

func (e *Env) startDaemon() {
	e.t.Helper()
	cmd := exec.Command(sortieBinPath, "daemon", "start")
	cmd.Env = os.Environ()
	cmd.Dir = e.ProjectDir

	logFile, err := os.Create(e.daemonLog)
	if err != nil {
		e.t.Fatalf("create daemon log: %v", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start daemon: %v", err)
	}
	e.daemonCmd = cmd
}

// RestartDaemon gracefully stops the running daemon and starts a fresh one.
//
// Needed when a test must change config that is read once at server
// construction (e.g. max_workers → agent.NewManager): setupE2E starts the
// daemon BEFORE the test calls WriteSortieYAML, so the initial daemon runs on
// defaults. Restarting after WriteSortieYAML makes the committed .sortie.yml
// take effect.
func (e *Env) RestartDaemon() {
	e.t.Helper()

	stopCmd := exec.Command(sortieBinPath, "daemon", "stop")
	stopCmd.Env = os.Environ()
	stopCmd.Dir = e.ProjectDir
	_ = stopCmd.Run()

	// Wait for the old process to exit so the new daemon does not race it on
	// the socket/pid files (the old process removes both on shutdown).
	if e.daemonCmd != nil && e.daemonCmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = e.daemonCmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = e.daemonCmd.Process.Kill()
			<-done
		}
	}

	e.startDaemon()
	e.WaitDaemonReady(filepath.Join(e.XDGDir, "sortie", "daemon.sock"), 5*time.Second)
}

// WriteSortieYAML writes the given YAML string as .sortie.yml in the project
// directory and commits it so the working tree remains clean before tasks run.
func (e *Env) WriteSortieYAML(yaml string) {
	e.t.Helper()
	path := filepath.Join(e.ProjectDir, ".sortie.yml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		e.t.Fatalf("write .sortie.yml: %v", err)
	}
	e.mustRun(e.ProjectDir, "git", "add", ".sortie.yml")
	e.mustRun(e.ProjectDir, "git", "commit", "-m", "add .sortie.yml")
}

// SwapResponses writes newSubdir as the active subdir pointer in ResponsesDir.
// The stub reads this file at each invocation and appends the subdir to the base
// ResponsesDir path. This allows changing responses without restarting the daemon.
func (e *Env) SwapResponses(newSubdir string) {
	e.t.Helper()
	ptr := filepath.Join(e.ResponsesDir, ".current-subdir")
	if err := os.WriteFile(ptr, []byte(newSubdir), 0644); err != nil {
		e.t.Fatalf("SwapResponses: %v", err)
	}
}

// Sortie runs the sortie binary with the given args in the project directory.
// Returns combined stdout+stderr output and any error.
func (e *Env) Sortie(args ...string) (string, error) {
	cmd := exec.Command(sortieBinPath, args...)
	cmd.Env = os.Environ()
	cmd.Dir = e.ProjectDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// MustSortie runs sortie and calls t.Fatalf on error.
func (e *Env) MustSortie(args ...string) string {
	e.t.Helper()
	out, err := e.Sortie(args...)
	if err != nil {
		e.t.Fatalf("sortie %v: %v\noutput: %s", args, err, out)
	}
	return out
}

// AddProject creates a SECOND isolated git repo served by the same daemon and
// returns its absolute path. The daemon socket and tasks.db live under
// $XDG_CONFIG_HOME/sortie/, so `sortie create` run with cwd = the returned dir
// talks to the already-running daemon; the daemon builds that project's context
// lazily from its own committed .sortie.yml.
//
// The .sortie.yml MUST be committed — cmd/sortie's PersistentPreRunE requires
// cfg.ProjectConfigFound for `create`, and an uncommitted file would also leave
// the working tree dirty for finalization.
func (e *Env) AddProject(yaml string) string {
	e.t.Helper()

	dir, err := os.MkdirTemp("/tmp", "p")
	if err != nil {
		e.t.Fatalf("mkdir second project temp: %v", err)
	}
	e.t.Cleanup(func() {
		if e.t.Failed() && os.Getenv("KEEP_E2E_TMPDIR") != "" {
			e.t.Logf("KEEP_E2E_TMPDIR: extra ProjectDir=%s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})

	e.mustRun(dir, "git", "init", "-b", "main")
	e.mustRun(dir, "git", "config", "user.email", "test@e2e.local")
	e.mustRun(dir, "git", "config", "user.name", "E2E Test")
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0644); err != nil {
		e.t.Fatalf("write .gitkeep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".sortie/\n"), 0644); err != nil {
		e.t.Fatalf("write .gitignore: %v", err)
	}
	e.mustRun(dir, "git", "add", ".gitkeep", ".gitignore")
	e.mustRun(dir, "git", "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, ".sortie.yml"), []byte(yaml), 0644); err != nil {
		e.t.Fatalf("write .sortie.yml for second project: %v", err)
	}
	e.mustRun(dir, "git", "add", ".sortie.yml")
	e.mustRun(dir, "git", "commit", "-m", "add .sortie.yml")

	return dir
}

// WriteHookFile writes content to $HOME/<name> so stub hooks can read
// per-test values as "$HOME/<name>".
//
// HOME is set to e.XDGDir before the daemon starts and the daemon passes
// os.Environ() through to the agent command, so this is the ONLY reliable way
// to pass per-test values into a hook: a t.Setenv after setupE2E never reaches
// the already-started daemon.
func (e *Env) WriteHookFile(name, content string) {
	e.t.Helper()
	path := filepath.Join(e.XDGDir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		e.t.Fatalf("write hook file %s: %v", path, err)
	}
}

// SortieJSON runs sortie with --json appended and decodes JSON output.
func (e *Env) SortieJSON(args ...string) (map[string]any, error) {
	argsWithJSON := append(args, "--json")
	out, err := e.Sortie(argsWithJSON...)
	if err != nil {
		return nil, fmt.Errorf("sortie %v: %w\noutput: %s", argsWithJSON, err, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, fmt.Errorf("decode JSON: %w\nraw: %s", err, out)
	}
	return m, nil
}

// TaskStatus returns the status string for the given task ID by calling sortie tasks.
func (e *Env) TaskStatus(id int64) string {
	e.t.Helper()
	m, err := e.SortieJSON("tasks", fmt.Sprintf("%d", id))
	if err != nil {
		return ""
	}
	s, _ := m["status"].(string)
	return s
}

// TaskField returns the named field from sortie tasks <id> --json as a string.
func (e *Env) TaskField(id int64, key string) string {
	e.t.Helper()
	m, err := e.SortieJSON("tasks", fmt.Sprintf("%d", id))
	if err != nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// CapturedPrompt returns the resolved step prompt the stub saw for the given
// task's step (a copy of $SORTIE_PROMPT_FILE written to E2E_PROMPT_CAPTURE_DIR
// — the TSV log deliberately omits the prompt). Waits briefly for the file,
// then t.Fatal on missing.
func (e *Env) CapturedPrompt(taskID int64, step string) string {
	e.t.Helper()
	path := filepath.Join(e.PromptDir, fmt.Sprintf("prompt-%d-%s.txt", taskID, step))
	e.WaitFile(path, 5*time.Second)
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read captured prompt %s: %v", path, err)
	}
	return string(data)
}

// mustRun runs cmd in dir and calls t.Fatal on failure.
func (e *Env) mustRun(dir string, name string, args ...string) {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("run %s %v in %s: %v\n%s", name, args, dir, err, out)
	}
}

// mustRunOut runs cmd in dir, returns stdout, and calls t.Fatal on failure.
func (e *Env) mustRunOut(dir string, name string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("run %s %v in %s: %v\n%s", name, args, dir, err, out)
	}
	return string(out)
}

// KillDaemon sends SIGKILL to the daemon process. Used for orphan-recovery tests.
func (e *Env) KillDaemon() {
	e.t.Helper()
	if e.daemonCmd != nil && e.daemonCmd.Process != nil {
		if err := e.daemonCmd.Process.Kill(); err != nil {
			e.t.Logf("KillDaemon: %v", err)
		}
	}
}

// DaemonLogPath returns the path to the daemon's stdout+stderr log.
func (e *Env) DaemonLogPath() string {
	return e.daemonLog
}

// formatElapsed returns a human-readable elapsed duration string for failure messages.
func formatElapsed(start time.Time) string {
	return fmt.Sprintf("%.1fs", time.Since(start).Seconds())
}
