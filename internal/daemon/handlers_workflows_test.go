package daemon

import (
	"testing"

	"github.com/Bakaface/sortie/internal/config"
)

func TestSummarizeWorkflows_PropagatesHiddenAndSource(t *testing.T) {
	in := []config.WorkflowConfig{
		{
			Name:        "active",
			Description: "Active workflow",
			Source:      "inline",
		},
		{
			Name:   "hidden-one",
			Hidden: true,
			Source: "/some/path/.sortie/workflows/tasks/hidden-one.yml",
		},
	}

	out := summarizeWorkflows(in)
	if len(out) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(out))
	}
	if out[0].Hidden {
		t.Errorf("active workflow should not be hidden in summary")
	}
	if out[0].Source != "inline" {
		t.Errorf("want source=inline, got %q", out[0].Source)
	}
	if !out[1].Hidden {
		t.Errorf("hidden workflow should have Hidden=true in summary")
	}
	if out[1].Source == "inline" || out[1].Source == "" {
		t.Errorf("want file path source, got %q", out[1].Source)
	}
}

// TestSummarizeWorkflows_PinFieldsAndFullySpec verifies the daemon-side mapping
// that feeds list_workflows: the worktree/branch/checkout/target pins must be
// copied verbatim and FullySpec must reflect IsFullySpec(). This is the data the
// TUI/MCP use to decide whether to skip the New Task screen, so a dropped field
// here would silently break the skip decision.
func TestSummarizeWorkflows_PinFieldsAndFullySpec(t *testing.T) {
	worktreeTrue := true
	in := []config.WorkflowConfig{
		{
			// Fully specified: input + worktree + new-branch + target.
			Name:     "pinned",
			Input:    "Pinned body",
			Worktree: &worktreeTrue,
			Branch:   "sortie/{{task_id}}",
			Target:   "main",
			Steps:    []config.StepConfig{{Name: "implement"}},
		},
		{
			// Not fully specified: only an input pin, no worktree decision.
			Name:  "partial",
			Input: "Partial body",
			Steps: []config.StepConfig{{Name: "implement"}},
		},
	}

	out := summarizeWorkflows(in)
	if len(out) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(out))
	}

	p := out[0]
	if p.Worktree == nil || !*p.Worktree {
		t.Errorf("pinned.Worktree: got %v, want non-nil true", p.Worktree)
	}
	if p.Branch != "sortie/{{task_id}}" {
		t.Errorf("pinned.Branch: got %q, want the pinned template", p.Branch)
	}
	if p.Target != "main" {
		t.Errorf("pinned.Target: got %q, want %q", p.Target, "main")
	}
	if !p.FullySpec {
		t.Errorf("pinned.FullySpec: got false, want true (all New Task fields pinned)")
	}

	if out[1].FullySpec {
		t.Errorf("partial.FullySpec: got true, want false (worktree not pinned)")
	}
	if out[1].Worktree != nil {
		t.Errorf("partial.Worktree: got %v, want nil (no worktree pin)", *out[1].Worktree)
	}
}
