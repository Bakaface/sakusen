package task

import (
	"regexp"
	"strings"
	"time"
)

type Status string

const (
	StatusPending            Status = "pending"
	StatusInit               Status = "init"
	StatusRunning            Status = "running"
	StatusAwaitingApproval   Status = "awaiting-approval"
	StatusAwaitingChildren   Status = "awaiting-children"
	StatusTmux               Status = "tmux"
	StatusFinalizing         Status = "finalizing"
	StatusSummarizing        Status = "summarizing"
	StatusSummarizingStep    Status = "summarizing_step"
	StatusMergeBlocked       Status = "merge-blocked"
	StatusResolvingConflicts Status = "resolving-conflicts"
	StatusCompleted          Status = "completed"
	StatusFailed             Status = "failed"
)

func (s Status) String() string {
	return string(s)
}

// IsTerminal reports whether the task has reached a final status (no further
// scheduling, recovery, or state transitions are expected). This is the
// single source of truth for "is this task done?" — daemon call sites that
// need "not yet done" (e.g. checkProjectTasksDone) should use !IsTerminal()
// rather than re-enumerating the non-terminal status list.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed
}

// IsAwaitingChildren reports whether the task is suspended mid-step waiting
// for spawned child tasks (recorded in task_waits_on) to reach terminal status.
func (s Status) IsAwaitingChildren() bool {
	return s == StatusAwaitingChildren
}

// IsActive reports whether the workflow engine has actually claimed and
// started this task's step execution — i.e. every non-terminal status
// except the two "not yet claimed/started" statuses (StatusPending,
// StatusInit). This is a narrower set than !IsTerminal(): a pending or
// initializing task is not yet "done", but it also has no agent driving it.
func (s Status) IsActive() bool {
	return s == StatusRunning || s == StatusAwaitingApproval || s == StatusAwaitingChildren || s == StatusTmux || s == StatusFinalizing || s == StatusSummarizing || s == StatusSummarizingStep || s == StatusMergeBlocked || s == StatusResolvingConflicts
}

// MayHaveDirtyRepoState reports whether an interrupted agent for a task in
// this status could have left the git worktree/repo mid-mutation (e.g.
// staged conflict markers on the base branch) — used by the daemon's
// startup recovery sweep (recoverOrphanedTasks) to decide which repos need
// a CleanRepoState() pass before any recovery agent restarts touch them.
//
// This is deliberately narrower than IsActive(): it excludes the pause
// statuses (StatusAwaitingApproval, StatusAwaitingChildren, StatusTmux)
// where no process was actively touching the repo when the daemon died, and
// excludes StatusSummarizing, whose work happens after the merge has
// already completed cleanly.
func (s Status) MayHaveDirtyRepoState() bool {
	switch s {
	case StatusRunning, StatusSummarizingStep, StatusFinalizing, StatusMergeBlocked, StatusResolvingConflicts:
		return true
	default:
		return false
	}
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

func (p Priority) String() string {
	return string(p)
}

// Value returns a numeric sort value for the priority (higher = more important).
func (p Priority) Value() int {
	switch p {
	case PriorityUrgent:
		return 4
	case PriorityHigh:
		return 3
	case PriorityMedium:
		return 2
	case PriorityLow:
		return 1
	default:
		return 2
	}
}

// ValidPriorities returns all valid priority levels in ascending order.
func ValidPriorities() []Priority {
	return []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent}
}

// IsValidPriority checks if a string is a valid priority value.
func IsValidPriority(s string) bool {
	switch Priority(s) {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

type Task struct {
	ID               int64
	ProjectID        int64
	TrackID          *int64 // nil = trackless
	Title            string
	Description      string
	Slug             string
	Workflow         string
	Status           Status
	Priority         Priority
	StepIndex        int
	CurrentStep      string
	LoopIteration    int
	BranchName       string // user-provided branch template (e.g. "feature/{{task.title}}")
	Branch           string // resolved branch name
	TargetBranch     string // per-task override for base/merge branch
	CheckoutBranch   string // use an existing branch instead of creating a new one
	Worktree         bool   // whether to use git worktree isolation (default true)
	WorktreePath     string
	WorktreeDetached bool
	ExitCode         *int
	ErrorMessage     string
	Context          string
	BlockedBy        []int64
	Images           []string
	Commits          []string
	CreatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	UpdatedAt        time.Time
}

// MaxTitleLength is the maximum allowed length for a task title.
const MaxTitleLength = 80

// SanitizeTitle cleans a title string by taking only the first line,
// collapsing whitespace, removing control characters, and truncating
// to MaxTitleLength.
func SanitizeTitle(title string) string {
	// Take only the first line (split on any line break)
	if idx := strings.IndexAny(title, "\n\r"); idx != -1 {
		title = title[:idx]
	}

	// Replace control characters: tabs become spaces, others are removed
	var b strings.Builder
	for _, r := range title {
		if r == '\t' {
			b.WriteByte(' ')
		} else if r >= ' ' {
			b.WriteRune(r)
		}
	}
	title = b.String()

	// Collapse multiple spaces and trim
	title = strings.Join(strings.Fields(title), " ")

	// Truncate to max length
	if len(title) > MaxTitleLength {
		title = title[:MaxTitleLength]
		// Avoid cutting in the middle of a word — trim back to last space
		if idx := strings.LastIndexByte(title, ' '); idx > MaxTitleLength/2 {
			title = title[:idx]
		}
	}

	return title
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(title string) string {
	s := strings.ToLower(title)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.TrimRight(s, "-")
	}
	return s
}
