package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Step-done sentinel convention.
//
// When a step runs inside tmux, whatever runs inside the session — a Claude
// Code Stop hook, the agent itself as its final action, a userland
// idle-watcher — signals "turn ended" by writing a file into the step-done
// directory sakusen exports to the agent as SAKUSEN_DONE_DIR, named
//
//	$SAKUSEN_DONE_PREFIX-<nanosecond timestamp>.json
//
// (e.g. `"$SAKUSEN_DONE_DIR/$SAKUSEN_DONE_PREFIX-$(date +%s%N).json"`). The
// daemon's tmuxMonitorLoop polls for these sentinels and uses them to
// auto-advance the workflow (unless the step is marked `human: true`). Agents
// that never write a sentinel are manual-advance: the task pauses at tmux
// status until the user acts.
//
// Storage layout (per worktree):
//
//	<worktree>/.sakusen/step-done/<prefix>-<ts>.json
//
// The file content MAY be a JSON object; two optional fields are understood by
// sakusen (StepSentinel below): "session_id" — an opaque, agent-native session
// identifier recorded in the chats table (enables `resume_command` restore and
// `chat_log_command` lookup) — and "transcript_path" — a chat transcript file
// passed to the agent's chat_log_command as SAKUSEN_TRANSCRIPT_PATH. A Claude
// Code Stop-hook payload carries both natively; other agents can write
// whatever subset they have (or an empty file).

// StepDoneDirName is the directory name (relative to a worktree's .sakusen/)
// that holds turn-end sentinel files for tmux step completion detection.
const StepDoneDirName = "step-done"

// StepDoneDir returns the absolute path to the directory holding turn-end
// sentinel files inside a worktree. Exported to tmux agents as SAKUSEN_DONE_DIR.
func StepDoneDir(worktreePath string) string {
	return filepath.Join(worktreePath, ".sakusen", StepDoneDirName)
}

// SentinelPrefix returns the filename prefix a sentinel for the given step
// must use (the sanitised step name). Exported to tmux agents as
// SAKUSEN_DONE_PREFIX.
func SentinelPrefix(stepName string) string {
	return shellSafeStepName(stepName)
}

// shellSafeStepName produces a sentinel-filename-safe version of a step name.
// Step names are user-configurable strings and may contain shell-significant
// characters (spaces, slashes, dollar signs) that would otherwise corrupt
// filenames or the commands that reference them. We strip anything outside
// [A-Za-z0-9_-] and substitute underscore; collisions across distinct step
// names are acceptable because the timestamp suffix still disambiguates
// per-turn sentinels.
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

// StepSentinel is the subset of a sentinel file's optional JSON payload that
// sakusen reads back. SessionID and TranscriptPath identify the agent session
// that actually ran the step, which is authoritative — the sentinel is
// written from inside that very session.
type StepSentinel struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

// sentinelMatchesStep reports whether filename is a turn-end sentinel written
// for the step whose sanitised name is stepPrefix. The hook names sentinels
// "<stepPrefix>-<timestamp>.json" where <timestamp> is `$(date +%s%N)` — a run
// of digits (with a trailing literal "N" when BSD date lacks %N support).
//
// Matching on the "<prefix>-<digits…>" shape rather than a bare prefix prevents
// a step from claiming a longer sibling's sentinels — e.g. step "reviewing"
// must not match "reviewing-tests-<ts>.json". The remainder after the prefix
// must start with a digit and contain no '-', which a longer step name's
// suffix ("tests-<ts>") never satisfies.
func sentinelMatchesStep(filename, stepPrefix string) bool {
	if filename == "" || filename[0] == '.' {
		return false
	}
	base, ok := strings.CutSuffix(filename, ".json")
	if !ok {
		return false
	}
	rest, ok := strings.CutPrefix(base, stepPrefix+"-")
	if !ok || rest == "" {
		return false
	}
	if rest[0] < '0' || rest[0] > '9' {
		return false
	}
	return !strings.Contains(rest, "-")
}

// latestStepSentinelFile returns the path of the most recent sentinel (by file
// mtime) written for the given step, or ok=false when none exists. mtime is
// used rather than the encoded timestamp so selection is robust regardless of
// whether the platform's date(1) supports %N.
func latestStepSentinelFile(worktreePath, stepName string) (string, bool) {
	if worktreePath == "" {
		return "", false
	}
	dir := StepDoneDir(worktreePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	prefix := shellSafeStepName(stepName)
	var bestPath string
	var bestFound bool
	var bestMod int64
	for _, e := range entries {
		if e.IsDir() || !sentinelMatchesStep(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); !bestFound || mod >= bestMod {
			bestFound = true
			bestMod = mod
			bestPath = filepath.Join(dir, e.Name())
		}
	}
	return bestPath, bestFound
}

// StepSentinelExists reports whether at least one turn-end sentinel for the
// given step is present. Read errors (missing dir, permission denied) are
// treated as "no sentinel" — the daemon's idle fallback remains the safety net.
func StepSentinelExists(worktreePath, stepName string) bool {
	_, ok := latestStepSentinelFile(worktreePath, stepName)
	return ok
}

// LatestStepSentinel parses the most recent turn-end sentinel for the given
// step and returns its payload. ok is false only when no sentinel exists; an
// unreadable or unparseable payload returns ok=true with a zero StepSentinel
// (the file's existence is the turn-end signal; payload fields are optional).
func LatestStepSentinel(worktreePath, stepName string) (StepSentinel, bool) {
	s, _, ok := LatestStepSentinelWithPath(worktreePath, stepName)
	return s, ok
}

// LatestStepSentinelWithPath is LatestStepSentinel plus the sentinel file's
// path, for callers that hand the raw file to an agent command
// (SAKUSEN_SENTINEL_FILE). A sentinel that exists but holds no parseable JSON
// still returns ok=true with a zero payload — the file's existence is the
// turn-end signal; the payload fields are optional.
func LatestStepSentinelWithPath(worktreePath, stepName string) (StepSentinel, string, bool) {
	path, ok := latestStepSentinelFile(worktreePath, stepName)
	if !ok {
		return StepSentinel{}, "", false
	}
	var s StepSentinel
	data, err := os.ReadFile(path)
	if err != nil {
		return StepSentinel{}, path, true
	}
	_ = json.Unmarshal(data, &s)
	return s, path, true
}

// ClearStepSentinels removes every turn-end sentinel for the given step from
// the worktree's step-done directory. Scoping to the step name leaves sentinels
// for other (concurrent or earlier) steps untouched. Errors are intentionally
// swallowed: a leftover sentinel triggers at most one redundant advance
// attempt, guarded by the daemon's per-task advancing flag.
func ClearStepSentinels(worktreePath, stepName string) {
	if worktreePath == "" {
		return
	}
	dir := StepDoneDir(worktreePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := shellSafeStepName(stepName)
	for _, e := range entries {
		if e.IsDir() || !sentinelMatchesStep(e.Name(), prefix) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
