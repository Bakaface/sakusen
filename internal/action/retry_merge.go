package action

import (
	"errors"
	"fmt"
)

type RetryMergeArgs struct {
	ID int64
}

func (a RetryMergeArgs) Validate() error {
	if a.ID <= 0 {
		return errors.New("task id is required")
	}
	return nil
}

func RunRetryMerge(ctx Ctx, args RetryMergeArgs) (Result, error) {
	if err := args.Validate(); err != nil {
		return Result{}, err
	}
	task, err := ctx.Client.RetryMerge(args.ID)
	if err != nil {
		return Result{}, fmt.Errorf("retry merge of task %d: %w", args.ID, err)
	}
	return Result{
		Message: fmt.Sprintf("Task #%d retrying merge", args.ID),
		Task:    task,
	}, nil
}

func init() {
	Registry["retry-merge"] = Action{
		ID:   "retry-merge",
		Help: "Retry only the merge of a merge-failed task",
		Run: func(ctx Ctx, a Args) (Result, error) {
			return RunRetryMerge(ctx, a.(RetryMergeArgs))
		},
		Parse: func(raw string) (Args, error) {
			id, err := parseInt64(raw)
			if err != nil {
				return nil, err
			}
			return RetryMergeArgs{ID: id}, nil
		},
	}
}
