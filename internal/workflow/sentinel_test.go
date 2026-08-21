package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSentinel writes a sentinel file into a worktree's step-done dir and
// returns the worktree root.
func writeSentinel(t *testing.T, worktree, name, body string) {
	t.Helper()
	dir := StepDoneDir(worktree)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir step-done: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write sentinel %q: %v", name, err)
	}
}

func TestStepSentinelExists_MatchesStepByName(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "implementing-1234567890.json", `{}`)

	if !StepSentinelExists(worktree, "implementing") {
		t.Errorf("expected implementing sentinel to be detected")
	}
	if StepSentinelExists(worktree, "reviewing") {
		t.Errorf("a sentinel for a different step must not match")
	}
}

func TestStepSentinelExists_NoDir(t *testing.T) {
	if StepSentinelExists(t.TempDir(), "implementing") {
		t.Errorf("expected no sentinel when step-done dir is missing")
	}
}

func TestStepSentinelExists_IgnoresDotfiles(t *testing.T) {
	worktree := t.TempDir()
	// The hook writes its in-flight temp file as `.<pid>.<ts>.tmp` before the
	// atomic rename; it must never read as a completed sentinel.
	writeSentinel(t, worktree, ".999.123.tmp", "x")
	if StepSentinelExists(worktree, "implementing") {
		t.Errorf("dotfile temp must be ignored")
	}
}

// TestSentinelMatchesStep_PrefixSibling guards the disambiguation between a step
// and a longer sibling that shares its prefix: "reviewing" must not claim
// "reviewing-tests" sentinels.
func TestSentinelMatchesStep_PrefixSibling(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "reviewing-tests-1234567890.json", `{}`)

	if StepSentinelExists(worktree, "reviewing") {
		t.Errorf("step %q must not match sibling %q sentinel", "reviewing", "reviewing-tests")
	}
	if !StepSentinelExists(worktree, "reviewing-tests") {
		t.Errorf("step %q must match its own sentinel", "reviewing-tests")
	}
}

// TestSentinelMatchesStep_BsdTimestamp covers the BSD-date case where %N is
// unsupported and the timestamp carries a trailing literal "N".
func TestSentinelMatchesStep_BsdTimestamp(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "implementing-1718721960N.json", `{}`)
	if !StepSentinelExists(worktree, "implementing") {
		t.Errorf("expected sentinel with BSD-style timestamp to match")
	}
}

func TestLatestStepSentinel_ParsesPayloadAndPicksNewest(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "implementing-100.json", `{"session_id":"old","transcript_path":"/old.jsonl"}`)
	// Make the second sentinel strictly newer by mtime.
	writeSentinel(t, worktree, "implementing-200.json", `{"session_id":"new","transcript_path":"/new.jsonl","cwd":"/wt"}`)
	newer := filepath.Join(StepDoneDir(worktree), "implementing-200.json")
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, ok := LatestStepSentinel(worktree, "implementing")
	if !ok {
		t.Fatalf("expected a sentinel")
	}
	if got.SessionID != "new" {
		t.Errorf("expected newest session id %q, got %q", "new", got.SessionID)
	}
	if got.TranscriptPath != "/new.jsonl" {
		t.Errorf("expected transcript path %q, got %q", "/new.jsonl", got.TranscriptPath)
	}
}

func TestLatestStepSentinel_NoneOrUnparseable(t *testing.T) {
	worktree := t.TempDir()
	if _, ok := LatestStepSentinel(worktree, "implementing"); ok {
		t.Errorf("expected ok=false when no sentinel exists")
	}
	// An unparseable sentinel still signals turn-end (existence = signal); the
	// optional JSON payload simply stays zero.
	writeSentinel(t, worktree, "implementing-1.json", `not json`)
	got, path, ok := LatestStepSentinelWithPath(worktree, "implementing")
	if !ok {
		t.Fatalf("expected ok=true for an unparseable sentinel (existence is the signal)")
	}
	if got.SessionID != "" || got.TranscriptPath != "" || got.Cwd != "" {
		t.Errorf("expected zero payload for unparseable sentinel, got %+v", got)
	}
	if path != filepath.Join(StepDoneDir(worktree), "implementing-1.json") {
		t.Errorf("unexpected sentinel path %q", path)
	}
}

// TestClearStepSentinels_ScopedToStep verifies only the named step's sentinels
// are removed; other steps' markers survive.
func TestClearStepSentinels_ScopedToStep(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "implementing-100.json", `{}`)
	writeSentinel(t, worktree, "implementing-200.json", `{}`)
	writeSentinel(t, worktree, "grilling-100.json", `{}`)

	ClearStepSentinels(worktree, "implementing")

	if StepSentinelExists(worktree, "implementing") {
		t.Errorf("expected implementing sentinels to be cleared")
	}
	if !StepSentinelExists(worktree, "grilling") {
		t.Errorf("grilling sentinel must survive a scoped clear of another step")
	}
}

func TestClearStepSentinels_NoDirIsHarmless(t *testing.T) {
	ClearStepSentinels(t.TempDir(), "implementing") // must not panic
}

// TestSentinelPrefixSanitizationRoundTrip verifies SentinelPrefix's
// shell-safe sanitization AND that the sanitized prefix round-trips: a
// sentinel written under the prefix sakusen exports as SAKUSEN_DONE_PREFIX
// (i.e. `<SentinelPrefix(name)>-<ts>.json`) is found again when production
// code matches by the ORIGINAL step name. An empty step name falls back to
// the "step" prefix.
func TestSentinelPrefixSanitizationRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		stepName   string
		wantPrefix string
	}{
		{"plain name unchanged", "implement", "implement"},
		{"space becomes underscore", "run tests", "run_tests"},
		{"slash becomes underscore", "a/b", "a_b"},
		{"dollar becomes underscore", "we$rd", "we_rd"},
		{"empty name falls back to step", "", "step"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SentinelPrefix(tt.stepName)
			if got != tt.wantPrefix {
				t.Errorf("SentinelPrefix(%q) = %q, want %q", tt.stepName, got, tt.wantPrefix)
			}

			// Round-trip: a sentinel named the way the exported contract
			// prescribes must be detected under the original step name.
			worktree := t.TempDir()
			writeSentinel(t, worktree, got+"-1234567890.json", `{}`)
			if !StepSentinelExists(worktree, tt.stepName) {
				t.Errorf("StepSentinelExists(%q) = false after writing %q sentinel", tt.stepName, got)
			}
		})
	}
}
