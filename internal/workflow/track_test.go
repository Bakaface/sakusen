package workflow

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

func TestResolveTemplateTrackVars(t *testing.T) {
	ctx := &TemplateContext{
		Track: TrackVars{
			ID:         7,
			Name:       "Payments API",
			Context:    "## Track: Payments API\n\nchain context",
			OwnContext: "own context",
		},
	}
	tmpl := "id={{track.id}} name={{track.name}}\n{{track.context}}\nown:{{track.own_context}}"
	got := ResolveTemplate(tmpl, ctx)
	want := "id=7 name=Payments API\n## Track: Payments API\n\nchain context\nown:own context"
	if got != want {
		t.Errorf("resolved = %q, want %q", got, want)
	}
}

func TestResolveTemplateTrackVarsTrackless(t *testing.T) {
	// Zero-value TrackVars (trackless task) resolves every {{track.*}} var to
	// "" — including {{track.id}}, which must not render "0".
	got := ResolveTemplate("[{{track.id}}][{{track.name}}][{{track.context}}][{{track.own_context}}]", &TemplateContext{})
	if got != "[][][][]" {
		t.Errorf("resolved = %q, want %q", got, "[][][][]")
	}
}

func TestFormatTrackChain(t *testing.T) {
	tests := []struct {
		name  string
		chain []*task.Track
		want  string
	}{
		{
			name:  "empty chain",
			chain: nil,
			want:  "",
		},
		{
			name:  "single track",
			chain: []*task.Track{{Name: "Sprint 12", Context: "sprint goal"}},
			want:  "## Track: Sprint 12\n\nsprint goal",
		},
		{
			name: "root-first with blank-line joins",
			chain: []*task.Track{
				{Name: "Sprint 12", Context: "sprint goal"},
				{Name: "Payments API", Context: "endpoint conventions"},
			},
			want: "## Track: Sprint 12\n\nsprint goal\n\n## Track: Payments API\n\nendpoint conventions",
		},
		{
			name: "empty-context segments skipped entirely",
			chain: []*task.Track{
				{Name: "Grouping Only", Context: "  \n "},
				{Name: "Leaf", Context: "leaf ctx"},
			},
			want: "## Track: Leaf\n\nleaf ctx",
		},
		{
			name: "context trimmed",
			chain: []*task.Track{
				{Name: "T", Context: "\n  padded  \n"},
			},
			want: "## Track: T\n\npadded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatTrackChain(tt.chain); got != tt.want {
				t.Errorf("FormatTrackChain = %q, want %q", got, tt.want)
			}
		})
	}
}

// newTrackedFakeRunnerTestEngine mirrors newFakeRunnerTestEngine but attaches
// the task to a real DB-backed track chain (global root + project leaf).
func newTrackedFakeRunnerTestEngine(t *testing.T, wf config.WorkflowConfig) (*Engine, *task.Task, *fakeAgentRunner, *db.DB, *task.Track) {
	t.Helper()
	dir := t.TempDir()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	proj, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	root, err := database.CreateTrack(nil, "Sprint 12", "sprint-12", "", "sprint goal", "", nil)
	if err != nil {
		t.Fatalf("CreateTrack root: %v", err)
	}
	leaf, err := database.CreateTrack(&proj.ID, "Payments API", "payments-api", "", "leaf seed", "", &root.ID)
	if err != nil {
		t.Fatalf("CreateTrack leaf: %v", err)
	}
	tk, err := database.CreateTaskWithPriority(proj.ID, "tracked task", "desc", "slug", wf.Name, "", "", "", "", task.StatusRunning, task.PriorityMedium, false, nil, &leaf.ID)
	if err != nil {
		t.Fatalf("CreateTaskWithPriority: %v", err)
	}
	tk.WorktreePath = dir

	cfg := &config.Config{
		OnComplete: "none",
		Workflows:  []config.WorkflowConfig{wf},
	}
	engine := NewEngine(cfg, database, nil, dir)
	runner := newFakeAgentRunner()
	engine.runner = runner

	return engine, tk, runner, database, leaf
}

// TestRunTaskTrackContextInterpolatedAndEnvSet drives a real engine (fake
// agent runner, in-memory DB) over a tracked task: the step's resolved prompt
// must contain the root-first formatted chain and the leaf-only own context,
// and the step env must carry SORTIE_TRACK_ID.
func TestRunTaskTrackContextInterpolatedAndEnvSet(t *testing.T) {
	wf := config.WorkflowConfig{
		Name:  "default",
		Print: true,
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "CHAIN:\n{{track.context}}\nOWN:{{track.own_context}} NAME:{{track.name}} ID:{{track.id}}"},
		},
	}
	engine, tk, runner, _, leaf := newTrackedFakeRunnerTestEngine(t, wf)
	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "done"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	calls := runner.callsFor("implement")
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	prompt := calls[0].prompt
	rootIdx := strings.Index(prompt, "## Track: Sprint 12")
	leafIdx := strings.Index(prompt, "## Track: Payments API")
	if rootIdx < 0 || leafIdx < 0 || rootIdx > leafIdx {
		t.Errorf("expected root-first chain in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "sprint goal") || !strings.Contains(prompt, "leaf seed") {
		t.Errorf("expected both chain contexts in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "OWN:leaf seed") {
		t.Errorf("expected own_context to be leaf-only, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "NAME:Payments API") {
		t.Errorf("expected leaf name, got:\n%s", prompt)
	}

	if got, want := calls[0].env["SORTIE_TRACK_ID"], strconv.FormatInt(leaf.ID, 10); got != want {
		t.Errorf("SORTIE_TRACK_ID = %q, want %q", got, want)
	}
}

// TestRunTaskTracklessEnvOmitsTrackID: trackless tasks must not export
// SORTIE_TRACK_ID at all.
func TestRunTaskTracklessEnvOmitsTrackID(t *testing.T) {
	wf := config.WorkflowConfig{
		Name:  "default",
		Print: true,
		Steps: []config.StepConfig{{Name: "implement", Prompt: "do it"}},
	}
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, wf)
	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "done"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	calls := runner.callsFor("implement")
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if _, ok := calls[0].env["SORTIE_TRACK_ID"]; ok {
		t.Error("trackless task must not export SORTIE_TRACK_ID")
	}
}

// TestRunTaskTrackChainErrorFailsStep: a broken track reference fails the step
// with a wrapped error instead of silently rendering an empty context.
func TestRunTaskTrackChainErrorFailsStep(t *testing.T) {
	wf := config.WorkflowConfig{
		Name:  "default",
		Print: true,
		Steps: []config.StepConfig{{Name: "implement", Prompt: "{{track.context}}"}},
	}
	engine, tk, _, _ := newFakeRunnerTestEngine(t, wf)
	missing := int64(9999)
	tk.TrackID = &missing

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil {
		t.Fatal("expected error for missing track chain")
	}
	if !strings.Contains(err.Error(), "track chain") {
		t.Errorf("expected wrapped track chain error, got %v", err)
	}
}
