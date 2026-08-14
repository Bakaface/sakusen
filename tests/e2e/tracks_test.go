//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trackWorkflowYAML defines a one-step workflow whose prompt interpolates the
// track template vars, so the captured stub prompt proves what the agent saw.
func trackWorkflowYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task.\nCHAIN-START\n{{track.context}}\nCHAIN-END\nOWN-START\n{{track.own_context}}\nOWN-END"
`, stubPath)
}

// TestTracks_ContextInterpolationAndAccumulation drives the full sprint loop:
// a global parent track + project child track, a task whose prompt renders the
// root-first chain, SORTIE_TRACK_ID in the step env, then a live context
// append followed by a second task that sees the accumulated context.
func TestTracks_ContextInterpolationAndAccumulation(t *testing.T) {
	e := setupE2E(t, "tracks")
	e.WriteSortieYAML(trackWorkflowYAML(e.StubPath))

	e.MustSortie("tracks", "create", "Sprint 12", "--global", "--context", "sprint goal")
	e.MustSortie("tracks", "create", "Payments API", "--parent", "sprint-12", "--context", "seed")

	e.MustSortie("create", "--track", "payments-api", "--title", "task one", "task one")
	e.WaitStatus(1, "completed", 10*time.Second)

	// The task row must carry the track id.
	trackID := e.DBQueryInt(`SELECT id FROM tracks WHERE slug = 'payments-api'`)
	if trackID == 0 {
		t.Fatal("payments-api track not found in DB")
	}
	if got := e.DBQueryInt(`SELECT track_id FROM tasks WHERE id = 1`); got != trackID {
		t.Errorf("tasks.track_id = %d, want %d", got, trackID)
	}

	// Prompt must contain the chain root-first with headers, and the leaf-only
	// own context between the OWN markers.
	prompt := e.CapturedPrompt(1, "implementing")
	rootIdx := strings.Index(prompt, "## Track: Sprint 12")
	leafIdx := strings.Index(prompt, "## Track: Payments API")
	if rootIdx < 0 || leafIdx < 0 || rootIdx > leafIdx {
		t.Errorf("expected root-first chain in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "sprint goal") || !strings.Contains(prompt, "seed") {
		t.Errorf("expected both contexts in prompt, got:\n%s", prompt)
	}
	// Slice the section strictly between the first OWN-START/OWN-END pair.
	ownSection := prompt
	if s := strings.Index(prompt, "OWN-START"); s >= 0 {
		ownSection = prompt[s:]
		if e := strings.Index(ownSection, "OWN-END"); e >= 0 {
			ownSection = ownSection[:e]
		}
	}
	if !strings.Contains(ownSection, "seed") || strings.Contains(ownSection, "sprint goal") {
		t.Errorf("own_context must be leaf-only, got:\n%s", ownSection)
	}

	// SORTIE_TRACK_ID must be exported to the step agent.
	steps := e.StubCalls("step")
	if len(steps) == 0 {
		t.Fatal("no step stub calls recorded")
	}
	if got := steps[0].Env["SORTIE_TRACK_ID"]; got != fmt.Sprintf("%d", trackID) {
		t.Errorf("SORTIE_TRACK_ID = %q, want %d", got, trackID)
	}

	// Live accumulation: append context, then a second task must see both the
	// original seed and the new fragment.
	e.MustSortie("tracks", "set-context", "payments-api", "--append", "endpoint A done")
	e.MustSortie("create", "--track", "payments-api", "--title", "task two", "task two")
	e.WaitStatus(2, "completed", 10*time.Second)

	prompt2 := e.CapturedPrompt(2, "implementing")
	if !strings.Contains(prompt2, "seed") || !strings.Contains(prompt2, "endpoint A done") {
		t.Errorf("second task must see accumulated context, got:\n%s", prompt2)
	}
}

// TestTracks_TrackWorkflowSelected proves the track→workflow association end
// to end: a namespaced track workflow written AFTER the daemon cached the
// project config (exercising the tracks-fingerprint cache invalidation) is
// selected for a task created without an explicit workflow.
func TestTracks_TrackWorkflowSelected(t *testing.T) {
	e := setupE2E(t, "tracks")
	e.WriteSortieYAML(trackWorkflowYAML(e.StubPath))

	// Warm the daemon's config cache for this project first: creating a task
	// routes through getProjectContext, which caches the resolved config.
	e.MustSortie("create", "--title", "warmup", "warmup task")
	e.WaitStatus(1, "completed", 10*time.Second)

	// Now drop a track workflow on disk — the fingerprint check must pick it up.
	wfDir := filepath.Join(e.ProjectDir, ".sortie", "tracks", "payments-api", "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatal(err)
	}
	wfYaml := "steps:\n  - name: implementing\n    prompt: \"Track workflow step\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, "impl.yml"), []byte(wfYaml), 0644); err != nil {
		t.Fatal(err)
	}

	e.MustSortie("tracks", "create", "Payments API", "--workflow", "payments-api:impl")
	e.MustSortie("create", "--track", "payments-api", "--title", "track wf task", "task using track workflow")

	e.WaitStatus(2, "completed", 10*time.Second)

	if got := e.TaskField(2, "workflow"); got != "payments-api:impl" {
		t.Errorf("task workflow = %q, want payments-api:impl", got)
	}
	// The track workflow's step prompt must have been used — that only happens
	// if the daemon reloaded the config after the workflow file appeared.
	prompt := e.CapturedPrompt(2, "implementing")
	if !strings.Contains(prompt, "Track workflow step") {
		t.Errorf("expected the track workflow's prompt to run, got:\n%s", prompt)
	}
}
