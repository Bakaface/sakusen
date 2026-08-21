package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// TestStatusText_TmuxRealStatusMatrix exhaustively verifies the rendered
// status string for every combination that matters in the new "real status"
// model: tmux vs non-tmux, human vs non-human, detached vs not, and whether
// the tmux session is actually live on the system.
//
// Lives next to the integration-style View() smoke tests in app_test.go but
// drives statusText directly so the assertions describe the *intent* of the
// rendering rule, not just whether a token appears somewhere in the table.
func TestStatusText_TmuxRealStatusMatrix(t *testing.T) {
	tests := []struct {
		name      string
		task      daemon.TaskInfo
		sessions  map[int64]bool
		wantIcon  string
		wantLabel string
	}{
		{
			name:      "non-human tmux step renders as running with [wip]",
			task:      daemon.TaskInfo{ID: 1, Status: "tmux", CurrentStep: "implement", StepHuman: false},
			sessions:  map[int64]bool{1: true},
			wantIcon:  "●",
			wantLabel: "implement [wip]",
		},
		{
			name:      "human tmux step renders as awaiting-approval with [T]",
			task:      daemon.TaskInfo{ID: 2, Status: "tmux", CurrentStep: "review", StepHuman: true},
			sessions:  map[int64]bool{2: true},
			wantIcon:  "◷",
			wantLabel: "review [T]",
		},
		{
			name:      "detached worktree wins over any tmux postfix",
			task:      daemon.TaskInfo{ID: 3, Status: "tmux", CurrentStep: "dev", StepHuman: true, WorktreeDetached: true},
			sessions:  map[int64]bool{3: true},
			wantIcon:  "◷",
			wantLabel: "dev [detached]",
		},
		{
			name:      "tmux status without live session reads as stale [T]",
			task:      daemon.TaskInfo{ID: 4, Status: "tmux", CurrentStep: "implement"},
			sessions:  nil,
			wantIcon:  "●",
			wantLabel: "implement [T]",
		},
		{
			name:      "running status with live tmux session gets [wip] postfix",
			task:      daemon.TaskInfo{ID: 5, Status: "running", CurrentStep: "implement"},
			sessions:  map[int64]bool{5: true},
			wantIcon:  "●",
			wantLabel: "implement [wip]",
		},
		{
			name:      "running status without tmux session: no postfix",
			task:      daemon.TaskInfo{ID: 6, Status: "running", CurrentStep: "implement"},
			sessions:  nil,
			wantIcon:  "●",
			wantLabel: "implement",
		},
		{
			name:      "tmux_direct (no step name) falls back to status label with [T]",
			task:      daemon.TaskInfo{ID: 7, Status: "tmux", StepHuman: true},
			sessions:  map[int64]bool{7: true},
			wantIcon:  "◷",
			wantLabel: "awaiting-approval [T]",
		},
		{
			name:      "loop iteration counter survives the new tmux mapping",
			task:      daemon.TaskInfo{ID: 8, Status: "tmux", CurrentStep: "implement", StepHuman: false, LoopIteration: 2},
			sessions:  map[int64]bool{8: true},
			wantIcon:  "●",
			wantLabel: "implement [L2] [wip]",
		},
		{
			name:      "human tmux step flips to [T] when monitor reports idle",
			task:      daemon.TaskInfo{ID: 9, Status: "tmux", CurrentStep: "implement", StepHuman: true, TmuxActivity: "idle"},
			sessions:  map[int64]bool{9: true},
			wantIcon:  "◷",
			wantLabel: "implement [T]",
		},
		{
			name:      "human tmux step stays [wip] when monitor reports wip",
			task:      daemon.TaskInfo{ID: 10, Status: "tmux", CurrentStep: "implement", StepHuman: true, TmuxActivity: "wip"},
			sessions:  map[int64]bool{10: true},
			wantIcon:  "◷",
			wantLabel: "implement [wip]",
		},
		{
			name:      "non-human tmux step flips to [wip] when monitor reports wip",
			task:      daemon.TaskInfo{ID: 11, Status: "tmux", CurrentStep: "implement", StepHuman: false, TmuxActivity: "wip"},
			sessions:  map[int64]bool{11: true},
			wantIcon:  "●",
			wantLabel: "implement [wip]",
		},
		{
			name:      "unknown activity falls back to StepHuman default ([T] for human)",
			task:      daemon.TaskInfo{ID: 12, Status: "tmux", CurrentStep: "review", StepHuman: true, TmuxActivity: "unknown"},
			sessions:  map[int64]bool{12: true},
			wantIcon:  "◷",
			wantLabel: "review [T]",
		},
		{
			// Regression for sakusen#95: when the coordinator transitions to
			// resolving-conflicts mid-finalization, the previous tmux step's
			// lingering session must NOT cause us to render "implement [wip]" —
			// we should show the resolving-conflicts status explicitly.
			name:      "resolving-conflicts status suppresses tmux postfix",
			task:      daemon.TaskInfo{ID: 13, Status: "resolving-conflicts", CurrentStep: "implementing", StepHuman: true},
			sessions:  map[int64]bool{13: true},
			wantIcon:  "◉",
			wantLabel: "resolving-conflicts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newListView(false, "")
			l.tmuxSessions = tt.sessions
			got := l.statusText(tt.task)
			want := tt.wantIcon + " " + tt.wantLabel
			if got != want {
				t.Errorf("statusText() = %q, want %q", got, want)
			}
		})
	}
}

// TestStatusText_NoTmuxIconForTmuxStatus guards against regressing back to
// the old behavior where tmux tasks always rendered with the ▣ icon. The
// real status is mapped from StepHuman, so the tmux icon should never
// appear in normal rendering anymore.
func TestStatusText_NoTmuxIconForTmuxStatus(t *testing.T) {
	cases := []daemon.TaskInfo{
		{ID: 1, Status: "tmux", CurrentStep: "implement"},
		{ID: 2, Status: "tmux", CurrentStep: "review", StepHuman: true},
	}
	for _, task := range cases {
		l := newListView(false, "")
		l.tmuxSessions = map[int64]bool{task.ID: true}
		out := l.statusText(task)
		if strings.Contains(out, "▣") {
			t.Errorf("statusText(%+v) contains ▣ icon: %q — tmux tasks should render the real status icon", task, out)
		}
	}
}
