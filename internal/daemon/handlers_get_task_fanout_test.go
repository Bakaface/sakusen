package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

func getTaskInfo(t *testing.T, s *Server, id int64) (TaskInfo, string) {
	t.Helper()
	clientConn, serverConn := pipeForHandler(t)
	go s.handleGetTask(serverConn, GetTaskRequest{TaskID: id})
	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgGetTask {
		t.Fatalf("got %s: %s", msg.Type, msg.Payload)
	}
	var resp GetTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	return resp.Task, string(msg.Payload)
}

func TestGetTaskFanoutFields(t *testing.T) {
	s, projID := setupServerWithProject(t)
	database := s.database.(*db.DB)

	parent, err := database.CreateTask(projID, "parent", "desc", "parent", "planner", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := []task.PublishedItem{
		{Slug: "a", Input: "do a"},
		{Slug: "b", Title: "B", Input: "do b", Needs: []string{"a"}},
	}
	if err := database.PublishItems(parent.ID, "inventory", items); err != nil {
		t.Fatal(err)
	}
	var children []*task.Task
	for _, it := range items {
		c, err := database.CreateFanoutChildTask(projID, it.Slug, it.Input, it.Slug, "unit", "", "", "", "", task.StatusPending, task.PriorityMedium, true, nil, nil,
			task.FanoutOrigin{ParentID: parent.ID, Step: "units", Item: it.Slug})
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, c)
	}
	if _, err := database.IncrementFanoutAttempts(children[1].ID); err != nil {
		t.Fatal(err)
	}
	plain, err := database.CreateTask(projID, "plain", "desc", "plain", "", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	info, _ := getTaskInfo(t, s, parent.ID)
	wantItems := map[string][]PublishedItemInfo{"inventory": {
		{Slug: "a", Input: "do a"},
		{Slug: "b", Title: "B", Input: "do b", Needs: []string{"a"}},
	}}
	if !reflect.DeepEqual(info.PublishedItems, wantItems) {
		t.Errorf("published_items = %+v, want %+v", info.PublishedItems, wantItems)
	}
	wantChildren := []FanoutChildInfo{
		{Step: "units", Slug: "a", TaskID: children[0].ID, Status: "pending", Attempts: 0},
		{Step: "units", Slug: "b", TaskID: children[1].ID, Status: "pending", Attempts: 1},
	}
	if !reflect.DeepEqual(info.FanoutChildren, wantChildren) {
		t.Errorf("fanout_children = %+v, want %+v", info.FanoutChildren, wantChildren)
	}
	if info.FanoutParent != nil {
		t.Errorf("parent fanout_parent = %+v, want nil", info.FanoutParent)
	}

	childInfo, _ := getTaskInfo(t, s, children[1].ID)
	wantParent := &FanoutParentInfo{TaskID: parent.ID, Step: "units", Item: "b"}
	if !reflect.DeepEqual(childInfo.FanoutParent, wantParent) {
		t.Errorf("fanout_parent = %+v, want %+v", childInfo.FanoutParent, wantParent)
	}

	_, raw := getTaskInfo(t, s, plain.ID)
	for _, key := range []string{"published_items", "fanout_children", "fanout_parent"} {
		if strings.Contains(raw, `"`+key+`"`) {
			t.Errorf("plain task payload carries %q: %s", key, raw)
		}
	}

	// Broadcasts go through taskToInfo, which must not carry fanout data.
	reloaded, err := database.GetTask(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s.taskToInfo(reloaded))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"published_items", "fanout_children", "fanout_parent"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("taskToInfo carries %q: %s", key, b)
		}
	}
}
