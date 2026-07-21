package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Claude Code Stop hook integration.
//
// When a step runs inside tmux we install a project-scoped Claude Code Stop
// hook that writes a sentinel JSON file every time the Claude agent finishes
// a turn. The daemon's tmuxMonitorLoop polls for these sentinels and uses
// them to auto-advance the workflow once the user-driven session has
// quiesced (and the step is not marked `human: true`).
//
// Storage layout (per worktree):
//   <worktree>/.sortie/claude-settings/settings.json   ← hook definition
//   <worktree>/.sortie/step-done/<step>-<turn>.json   ← sentinel per turn end
//
// Both live under .sortie/ which is gitignored project-wide. The settings
// file is layered onto the running Claude Code process via the `--settings`
// CLI flag (see buildTmuxClaudeCmd) so the worktree's Stop hook merges on top
// of the user's global ~/.claude/ config. We deliberately avoid
// CLAUDE_CONFIG_DIR, which is a *full* redirection of the entire config
// directory — using it would hide OAuth credentials, onboarding state, and
// per-project trust acceptance from the spawned agent and trigger re-auth on
// every launch.
//
// The hook itself is a tiny POSIX shell command that catches the JSON
// payload on stdin and dumps it to a uniquely-named file. Claude Code
// passes session_id, transcript_path, cwd, and last_assistant_message in
// that payload, which is exactly what we need to advance the workflow.

// SortieSettingsDirName is the directory name (relative to a worktree's
// .sortie/) that holds the Claude Code settings.json layered onto the
// spawned agent via the `--settings` CLI flag.
const SortieSettingsDirName = "claude-settings"

// StepDoneDirName is the directory name (relative to a worktree's .sortie/)
// that holds Stop-hook sentinel files for tmux step completion detection.
const StepDoneDirName = "step-done"

// SortieSettingsDir returns the absolute path to the Claude settings directory
// inside a worktree. The Stop hook is installed at <dir>/settings.json and
// loaded via `claude --settings <dir>/settings.json`.
func SortieSettingsDir(worktreePath string) string {
	return filepath.Join(worktreePath, ".sortie", SortieSettingsDirName)
}

// StepDoneDir returns the absolute path to the directory holding Stop-hook
// sentinel files inside a worktree.
func StepDoneDir(worktreePath string) string {
	return filepath.Join(worktreePath, ".sortie", StepDoneDirName)
}

// claudeHookCommand is the Stop hook command body. It reads the JSON payload
// from stdin and writes it (atomically, via a temp file + rename) into the
// step-done directory under a unique filename derived from the step name and
// nanosecond timestamp. Failing silently is intentional: the hash-stability
// fallback in tmux_monitor will rescue the workflow if the sentinel never
// lands.
//
// The command is written into settings.json verbatim. It deliberately avoids
// any reliance on shell rc files (Claude Code v2.1.139+ runs hooks without a
// controlling terminal and can't open /dev/tty) and uses POSIX-only utilities
// so it works under both bash and the system /bin/sh.
//
// Template parameters: %s = single-quoted step-done dir, %s = sentinel filename
// prefix (the sanitised step name). The shell-level `d=...` indirection avoids
// the awkwardness of nesting single quotes inside double-quoted strings.
const claudeHookCommandTemplate = `d=%s; mkdir -p "$d" && tmp="$d/.$$.$(date +%%s%%N).tmp" && cat > "$tmp" && mv "$tmp" "$d/%s-$(date +%%s%%N).json"`

// claudeSettings is the minimal subset of Claude Code's settings.json schema
// that we need to install a Stop hook. The structure mirrors what the user's
// own ~/.claude/settings.json would look like for project-scoped hooks.
type claudeSettings struct {
	Hooks claudeHooks `json:"hooks"`
}

type claudeHooks struct {
	Stop []claudeHookMatcher `json:"Stop"`
}

type claudeHookMatcher struct {
	Hooks []claudeHook `json:"hooks"`
}

type claudeHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// InstallStopHook writes a Claude Code settings.json into the worktree's
// .sortie/claude-settings/ directory wiring the Stop hook to drop a sentinel
// file under .sortie/step-done/ on every turn end. It is idempotent: calling
// it again after a step has already installed hooks rewrites the same files
// with current values (the hook command depends only on absolute paths in
// the worktree, so identical runs produce identical output).
//
// The stepName parameter scopes sentinel filenames so multiple concurrent
// steps in the same worktree (e.g. a loop replaying earlier steps) don't
// collide. Sentinel files include a nanosecond timestamp suffix so a single
// step that fires the Stop hook multiple times (e.g. user-driven follow-up
// turns) still produces unique files; the daemon ignores subsequent
// sentinels once the workflow has advanced past the step.
func InstallStopHook(worktreePath, stepName string) error {
	settingsDir := SortieSettingsDir(worktreePath)
	stepDoneDir := StepDoneDir(worktreePath)

	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return fmt.Errorf("failed to create claude settings dir: %w", err)
	}
	if err := os.MkdirAll(stepDoneDir, 0755); err != nil {
		return fmt.Errorf("failed to create step-done dir: %w", err)
	}

	// shellQuote wraps a literal path in single quotes so the embedded shell
	// command treats it as a single argument even when the path contains
	// spaces. The %% escapes are for fmt.Sprintf below; date(1) consumes
	// single-% format specifiers at runtime.
	command := fmt.Sprintf(
		claudeHookCommandTemplate,
		shellSingleQuotePath(stepDoneDir),
		shellSafeStepName(stepName),
	)

	settings := claudeSettings{
		Hooks: claudeHooks{
			Stop: []claudeHookMatcher{
				{
					Hooks: []claudeHook{
						{Type: "command", Command: command},
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal claude settings: %w", err)
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write claude settings: %w", err)
	}

	return nil
}

// shellSingleQuotePath wraps p in POSIX-safe single quotes, escaping any
// embedded single quotes via the standard close-escape-reopen sequence (see
// escapeSingleQuotes).
func shellSingleQuotePath(p string) string {
	return "'" + escapeSingleQuotes(p) + "'"
}

func escapeSingleQuotes(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, []byte(`'\''`)...)
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// shellSafeStepName produces a sentinel-filename-safe version of a step name.
// Step names are user-configurable strings and may contain shell-significant
// characters (spaces, slashes, dollar signs) that would otherwise corrupt the
// hook command literal. We strip anything outside [A-Za-z0-9_-] and substitute
// underscore; collisions across distinct step names are acceptable because the
// timestamp suffix still disambiguates per-turn sentinels.
func shellSafeStepName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "step"
	}
	return string(out)
}
