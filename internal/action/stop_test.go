package action_test

import (
	"errors"
	"io"
	"testing"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/daemon"
)

func TestStopArgs_Validate(t *testing.T) {
	cases := []struct {
		name    string
		id      int64
		wantErr bool
	}{
		{"zero", 0, true},
		{"negative", -1, true},
		{"valid", 7, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := action.StopArgs{ID: tc.id}.Validate()
			if tc.wantErr && got == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && got != nil {
				t.Fatalf("unexpected error: %v", got)
			}
		})
	}
}

func TestRunStop_Success(t *testing.T) {
	want := &daemon.TaskInfo{ID: 42, Status: "stopped"}
	fc := &fakeClient{stopTask: func(id int64) (*daemon.TaskInfo, error) {
		if id != 42 {
			t.Fatalf("expected id=42, got %d", id)
		}
		return want, nil
	}}
	res, err := action.RunStop(action.Ctx{Client: fc, Out: io.Discard}, action.StopArgs{ID: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Task != want {
		t.Errorf("Task not propagated: %+v", res.Task)
	}
	if res.Message == "" {
		t.Errorf("expected a message")
	}
}

func TestRunStop_ClientError(t *testing.T) {
	sentinel := errors.New("daemon down")
	fc := &fakeClient{stopTask: func(int64) (*daemon.TaskInfo, error) { return nil, sentinel }}
	_, err := action.RunStop(action.Ctx{Client: fc}, action.StopArgs{ID: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got %v", err)
	}
}
