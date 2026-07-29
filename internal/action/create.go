package action

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/Bakaface/sortie/internal/task"
)

// CreateArgs collects every option supported by `sortie create` and the TUI
// new-task prompt. Empty Title is allowed: the daemon falls back to an
// AI-derived (or branch-derived) title when the input / checkout branch
// makes that meaningful.
type CreateArgs struct {
	Title    string
	Input    string
	Priority string
	Branch   string
	Workflow string
	Target   string
	Checkout string
	// Worktree is the explicit worktree choice. nil means "no explicit choice" —
	// the daemon then defers to the workflow's worktree pin, falling back to the
	// project default. A non-nil value (a real user choice) overrides both. Leaving
	// this nil is what lets a workflow's worktree:false pin actually take effect.
	Worktree *bool
	// Track attaches the task to a track (slug or numeric ID).
	Track       string
	DependsOn   []int64
	ProjectPath string // set by the caller from cfg.ProjectDir / m.projectPath
	Images      []string
	TmuxDirect  bool
	BranchMode  *int
}

func (a CreateArgs) Validate() error {
	if a.ProjectPath == "" {
		return errors.New("project path is required")
	}
	if a.Checkout != "" && a.Branch != "" {
		return errors.New("cannot specify both --checkout and --branch")
	}
	if a.Priority != "" && !task.IsValidPriority(a.Priority) {
		return fmt.Errorf("invalid priority %q (allowed: low, medium, high, urgent)", a.Priority)
	}
	return nil
}

func RunCreate(ctx Ctx, args CreateArgs) (Result, error) {
	if err := args.Validate(); err != nil {
		return Result{}, err
	}

	req := daemon.CreateTaskRequest{
		Title:          args.Title,
		Input:          strings.TrimSpace(args.Input),
		Workflow:       args.Workflow,
		Priority:       args.Priority,
		BranchName:     args.Branch,
		TargetBranch:   args.Target,
		CheckoutBranch: args.Checkout,
		ProjectPath:    args.ProjectPath,
		Worktree:       args.Worktree,
		Track:          args.Track,
		BranchMode:     args.BranchMode,
		TmuxDirect:     args.TmuxDirect,
		Images:         args.Images,
		BlockedBy:      args.DependsOn,
	}

	t, err := ctx.Client.CreateTaskWithOptions(req)
	if err != nil {
		return Result{}, fmt.Errorf("create task: %w", err)
	}

	return Result{
		Message: fmt.Sprintf("Task #%d created", t.ID),
		Task:    t,
	}, nil
}

func init() {
	Registry["create"] = Action{
		ID:   "create",
		Help: "Create a new task",
		Run: func(ctx Ctx, a Args) (Result, error) {
			return RunCreate(ctx, a.(CreateArgs))
		},
		// Palette form: ":create <input...>" — flag-bearing forms come
		// through Cobra in the CLI, not the palette.
		Parse: func(raw string) (Args, error) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return nil, errors.New("expected: create <input>")
			}
			return CreateArgs{Input: raw}, nil
		},
	}
}
