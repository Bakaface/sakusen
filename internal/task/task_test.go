package task

import (
	"strings"
	"testing"
)

func TestStatus_IsActive(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusInit, false},
		{StatusRunning, true},
		{StatusAwaitingApproval, true},
		{StatusAwaitingChildren, true},
		{StatusTmux, true},
		{StatusFinalizing, true},
		{StatusSummarizing, true},
		{StatusSummarizingStep, true},
		{StatusMergeBlocked, true},
		{StatusResolvingConflicts, true},
		{StatusCompleted, false},
		{StatusFailed, false},
		{StatusMergeFailed, false},
	}

	for _, tt := range tests {
		if got := tt.status.IsActive(); got != tt.want {
			t.Errorf("Status(%q).IsActive() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusInit, false},
		{StatusRunning, false},
		{StatusAwaitingApproval, false},
		{StatusAwaitingChildren, false},
		{StatusTmux, false},
		{StatusFinalizing, false},
		{StatusSummarizing, false},
		{StatusSummarizingStep, false},
		{StatusMergeBlocked, false},
		{StatusResolvingConflicts, false},
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusMergeFailed, true},
	}

	for _, tt := range tests {
		if got := tt.status.IsTerminal(); got != tt.want {
			t.Errorf("Status(%q).IsTerminal() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestStatusMergeFailed_DistinctFromCompletedAndFailed(t *testing.T) {
	// The wire value is persisted in SQLite and filtered on by list_tasks, so
	// it is part of the external contract, not an internal detail.
	if StatusMergeFailed != "merge-failed" {
		t.Errorf("StatusMergeFailed = %q, want %q", StatusMergeFailed, "merge-failed")
	}
	if StatusMergeFailed == StatusCompleted || StatusMergeFailed == StatusFailed {
		t.Fatalf("StatusMergeFailed must be distinct, got %q", StatusMergeFailed)
	}
	// The worktree is preserved for recovery and the coordinator already
	// cleaned the base repo — nothing for the startup sweep to reset.
	if StatusMergeFailed.MayHaveDirtyRepoState() {
		t.Error("StatusMergeFailed.MayHaveDirtyRepoState() = true, want false")
	}
}

func TestStatus_IsAwaitingChildren(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusRunning, false},
		{StatusAwaitingApproval, false},
		{StatusTmux, false},
		{StatusAwaitingChildren, true},
		{StatusCompleted, false},
	}
	for _, tt := range tests {
		if got := tt.status.IsAwaitingChildren(); got != tt.want {
			t.Errorf("Status(%q).IsAwaitingChildren() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestPriority_Value(t *testing.T) {
	tests := []struct {
		priority Priority
		want     int
	}{
		{PriorityLow, 1},
		{PriorityMedium, 2},
		{PriorityHigh, 3},
		{PriorityUrgent, 4},
		{Priority("unknown"), 2}, // default to medium
	}

	for _, tt := range tests {
		if got := tt.priority.Value(); got != tt.want {
			t.Errorf("Priority(%q).Value() = %d, want %d", tt.priority, got, tt.want)
		}
	}
}

func TestPriority_Ordering(t *testing.T) {
	// Verify that urgent > high > medium > low
	if PriorityUrgent.Value() <= PriorityHigh.Value() {
		t.Error("expected urgent > high")
	}
	if PriorityHigh.Value() <= PriorityMedium.Value() {
		t.Error("expected high > medium")
	}
	if PriorityMedium.Value() <= PriorityLow.Value() {
		t.Error("expected medium > low")
	}
}

func TestIsValidPriority(t *testing.T) {
	valid := []string{"low", "medium", "high", "urgent"}
	for _, s := range valid {
		if !IsValidPriority(s) {
			t.Errorf("expected %q to be a valid priority", s)
		}
	}
	invalid := []string{"", "critical", "none", "URGENT"}
	for _, s := range invalid {
		if IsValidPriority(s) {
			t.Errorf("expected %q to be an invalid priority", s)
		}
	}
}

func TestValidPriorities(t *testing.T) {
	priorities := ValidPriorities()
	if len(priorities) != 4 {
		t.Fatalf("expected 4 priorities, got %d", len(priorities))
	}
	if priorities[0] != PriorityLow || priorities[3] != PriorityUrgent {
		t.Error("priorities should be in ascending order: low, medium, high, urgent")
	}
}

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple title unchanged",
			input: "Fix the login bug",
			want:  "Fix the login bug",
		},
		{
			name:  "strips trailing newline",
			input: "Fix the login bug\n",
			want:  "Fix the login bug",
		},
		{
			name:  "takes first line only",
			input: "Fix the login bug\nThis is a longer description\nwith multiple lines",
			want:  "Fix the login bug",
		},
		{
			name:  "handles carriage return",
			input: "Fix the login bug\r\nSecond line",
			want:  "Fix the login bug",
		},
		{
			name:  "removes tab characters",
			input: "Fix\tthe\tlogin\tbug",
			want:  "Fix the login bug",
		},
		{
			name:  "collapses multiple spaces",
			input: "Fix   the    login   bug",
			want:  "Fix the login bug",
		},
		{
			name:  "trims leading and trailing whitespace",
			input: "   Fix the login bug   ",
			want:  "Fix the login bug",
		},
		{
			name:  "removes control characters",
			input: "Fix the \x00login \x01bug",
			want:  "Fix the login bug",
		},
		{
			name:  "truncates long titles at word boundary",
			input: "This is a very long task title that exceeds the maximum allowed length of eighty characters and should be truncated",
			want:  "This is a very long task title that exceeds the maximum allowed length of",
		},
		{
			name:  "exactly 80 chars is not truncated",
			input: "This is a very long task title that exceeds the maximum allowed length of eighty",
			want:  "This is a very long task title that exceeds the maximum allowed length of eighty",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only whitespace",
			input: "   \n\t  ",
			want:  "",
		},
		{
			name:  "only newlines",
			input: "\n\n\n",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTitle(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeTitle_MaxLength(t *testing.T) {
	// Any sanitized title should never exceed MaxTitleLength
	long := strings.Repeat("a", 200)
	got := SanitizeTitle(long)
	if len(got) > MaxTitleLength {
		t.Errorf("SanitizeTitle produced title of length %d, want <= %d", len(got), MaxTitleLength)
	}
}

func TestStatusValues(t *testing.T) {
	// Verify the actual string values of status constants
	tests := []struct {
		status Status
		value  string
	}{
		{StatusPending, "pending"},
		{StatusInit, "init"},
		{StatusRunning, "running"},
		{StatusAwaitingApproval, "awaiting-approval"},
		{StatusAwaitingChildren, "awaiting-children"},
		{StatusTmux, "tmux"},
		{StatusFinalizing, "finalizing"},
		{StatusSummarizing, "summarizing"},
		{StatusSummarizingStep, "summarizing_step"},
		{StatusMergeBlocked, "merge-blocked"},
		{StatusResolvingConflicts, "resolving-conflicts"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.value {
			t.Errorf("Status constant has value %q, want %q", tt.status, tt.value)
		}
	}
}
