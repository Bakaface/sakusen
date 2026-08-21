package action

import (
	"errors"
	"fmt"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
)

// EditArgs uses pointer fields so callers can distinguish "field not set" from
// "field cleared to empty string". Priority is validated up-front via
// task.IsValidPriority; the daemon would reject it later too, but failing in
// Validate() keeps the CLI/TUI from issuing the partial UpdateTaskField calls
// that precede it.
type EditArgs struct {
	ID       int64
	Title    *string
	Input    *string
	Context  *string
	Priority *string
}

func (a EditArgs) Validate() error {
	if a.ID <= 0 {
		return errors.New("task id is required")
	}
	if a.Title == nil && a.Input == nil && a.Context == nil && a.Priority == nil {
		return errors.New("at least one field must be set")
	}
	if a.Priority != nil && !task.IsValidPriority(*a.Priority) {
		return fmt.Errorf("invalid priority %q (allowed: low, medium, high, urgent)", *a.Priority)
	}
	return nil
}

// RunEdit composes up to four daemon calls. The TaskInfo returned reflects the
// last successful mutation; intermediate failures abort the chain so we never
// silently swallow a half-applied update. The plan tracks a future atomic
// UpdateTask RPC as follow-up work.
func RunEdit(ctx Ctx, args EditArgs) (Result, error) {
	if err := args.Validate(); err != nil {
		return Result{}, err
	}

	var (
		last    *daemon.TaskInfo
		updated int
	)

	apply := func(field string, value string) error {
		t, err := ctx.Client.UpdateTaskField(args.ID, field, value)
		if err != nil {
			return fmt.Errorf("update %s on task %d: %w", field, args.ID, err)
		}
		last = t
		updated++
		return nil
	}

	if args.Title != nil {
		if err := apply("title", *args.Title); err != nil {
			return Result{}, err
		}
	}
	if args.Input != nil {
		if err := apply("input", *args.Input); err != nil {
			return Result{}, err
		}
	}
	if args.Context != nil {
		if err := apply("context", *args.Context); err != nil {
			return Result{}, err
		}
	}
	if args.Priority != nil {
		t, err := ctx.Client.UpdateTaskPriority(args.ID, *args.Priority)
		if err != nil {
			return Result{}, fmt.Errorf("update priority on task %d: %w", args.ID, err)
		}
		last = t
		updated++
	}

	return Result{
		Message: fmt.Sprintf("Task #%d updated (%d field(s))", args.ID, updated),
		Task:    last,
	}, nil
}

func init() {
	Registry["edit"] = Action{
		ID:   "edit",
		Help: "Edit a task's title/input/context/priority",
		Run: func(ctx Ctx, a Args) (Result, error) {
			return RunEdit(ctx, a.(EditArgs))
		},
		// Palette form: "edit <id> <field>=<value>" with one field at a time.
		// CLI surfaces the richer multi-field form via Cobra flags directly.
		Parse: func(raw string) (Args, error) {
			parts := parseFields(raw)
			if len(parts) < 2 {
				return nil, errors.New("expected: edit <task-id> <field>=<value>")
			}
			id, err := parseInt64(parts[0])
			if err != nil {
				return nil, err
			}
			args := EditArgs{ID: id}
			for _, kv := range parts[1:] {
				eq := -1
				for i := 0; i < len(kv); i++ {
					if kv[i] == '=' {
						eq = i
						break
					}
				}
				if eq <= 0 {
					return nil, fmt.Errorf("expected field=value, got %q", kv)
				}
				key := kv[:eq]
				val := kv[eq+1:]
				switch key {
				case "title":
					args.Title = &val
				case "input":
					args.Input = &val
				case "context":
					args.Context = &val
				case "priority":
					args.Priority = &val
				default:
					return nil, fmt.Errorf("unknown field %q (allowed: title, input, context, priority)", key)
				}
			}
			return args, nil
		},
	}
}
