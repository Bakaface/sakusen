package action

import (
	"errors"
	"fmt"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// DependsOnArgs declares (or removes) a "TaskID is blocked by BlockedByID"
// edge. Direction discriminates the operation; pointer-patch isn't needed
// because both operations always require both IDs.
type DependsOnArgs struct {
	TaskID      int64
	BlockedByID int64
	Direction   string // "add" or "remove"
}

func (a DependsOnArgs) Validate() error {
	if a.TaskID <= 0 {
		return errors.New("task id is required")
	}
	if a.BlockedByID <= 0 {
		return errors.New("blocked-by id is required")
	}
	if a.TaskID == a.BlockedByID {
		return errors.New("a task cannot depend on itself")
	}
	switch a.Direction {
	case "add", "remove":
	default:
		return fmt.Errorf("invalid direction %q (allowed: add, remove)", a.Direction)
	}
	return nil
}

func RunDependsOn(ctx Ctx, args DependsOnArgs) (Result, error) {
	if err := args.Validate(); err != nil {
		return Result{}, err
	}
	var (
		task *daemon.TaskInfo
		err  error
	)
	switch args.Direction {
	case "add":
		task, err = ctx.Client.AddTaskDependency(args.TaskID, args.BlockedByID)
	case "remove":
		task, err = ctx.Client.RemoveTaskDependency(args.TaskID, args.BlockedByID)
	}
	if err != nil {
		return Result{}, fmt.Errorf("update dependency for task %d: %w", args.TaskID, err)
	}
	var msg string
	if args.Direction == "add" {
		msg = fmt.Sprintf("Task #%d now blocked by #%d", args.TaskID, args.BlockedByID)
	} else {
		msg = fmt.Sprintf("Task #%d no longer blocked by #%d", args.TaskID, args.BlockedByID)
	}
	return Result{Message: msg, Task: task}, nil
}

func init() {
	Registry["depends-on"] = Action{
		ID:   "depends-on",
		Help: "Manage task dependencies (depends-on <task> <blocked-by> [add|remove])",
		Run: func(ctx Ctx, a Args) (Result, error) {
			return RunDependsOn(ctx, a.(DependsOnArgs))
		},
		Parse: func(raw string) (Args, error) {
			parts := parseFields(raw)
			if len(parts) < 2 {
				return nil, errors.New("expected: depends-on <task-id> <blocked-by-id> [add|remove]")
			}
			id, err := parseInt64(parts[0])
			if err != nil {
				return nil, err
			}
			dep, err := parseInt64(parts[1])
			if err != nil {
				return nil, err
			}
			direction := "add"
			if len(parts) > 2 {
				direction = parts[2]
			}
			return DependsOnArgs{TaskID: id, BlockedByID: dep, Direction: direction}, nil
		},
	}
}
