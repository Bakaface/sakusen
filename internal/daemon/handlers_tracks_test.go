package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/workflow"
)

func decodePayload[T any](t *testing.T, msg *Message) T {
	t.Helper()
	var v T
	if err := msg.DecodePayload(&v); err != nil {
		t.Fatalf("decode %s payload: %v", msg.Type, err)
	}
	return v
}

func mustNotError(t *testing.T, msg *Message) {
	t.Helper()
	if msg.Type == MsgError {
		var e ErrorResponse
		_ = msg.DecodePayload(&e)
		t.Fatalf("unexpected error response: %s", e.Message)
	}
}

func expectError(t *testing.T, msg *Message, substr string) {
	t.Helper()
	if msg.Type != MsgError {
		t.Fatalf("expected error response, got %s", msg.Type)
	}
	var e ErrorResponse
	_ = msg.DecodePayload(&e)
	if !strings.Contains(e.Message, substr) {
		t.Fatalf("error %q does not contain %q", e.Message, substr)
	}
}

func TestHandleCreateTrack(t *testing.T) {
	t.Run("project track created with slugified name", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "Payments API", ProjectPath: "/tmp/sortie-test", Context: "seed"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTrackResponse](t, msg)
		if resp.Track.Slug != "payments-api" || resp.Track.Name != "Payments API" {
			t.Errorf("track = %+v", resp.Track)
		}
		if resp.Track.Global {
			t.Error("expected project-scoped track")
		}
		if resp.Track.Context != "seed" || resp.Track.ContextLen != 4 {
			t.Errorf("context = %q len=%d", resp.Track.Context, resp.Track.ContextLen)
		}
	})

	t.Run("global track needs no project path", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "Sprint 12", Global: true})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTrackResponse](t, msg)
		if !resp.Track.Global || resp.Track.ProjectID != nil {
			t.Errorf("expected global track, got %+v", resp.Track)
		}
	})

	t.Run("project track requires project path", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "X"})
		expectError(t, readOneMessage(t, clientConn), "project_path is required")
	})

	t.Run("missing name rejected", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "   ", ProjectPath: "/tmp/sortie-test"})
		expectError(t, readOneMessage(t, clientConn), "track name is required")
	})

	t.Run("parent by slug and by numeric ID", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		parent, err := s.database.CreateTrack(nil, "Sprint", "sprint", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "By Slug", ProjectPath: "/tmp/sortie-test", ParentTrack: "sprint"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTrackResponse](t, msg)
		if resp.Track.ParentID == nil || *resp.Track.ParentID != parent.ID {
			t.Errorf("ParentID = %v, want %d", resp.Track.ParentID, parent.ID)
		}
		if resp.Track.ParentSlug != "sprint" {
			t.Errorf("ParentSlug = %q", resp.Track.ParentSlug)
		}

		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleCreateTrack(serverConn2, CreateTrackRequest{Name: "By ID", ProjectPath: "/tmp/sortie-test", ParentTrack: fmt.Sprintf("%d", parent.ID)})
		msg2 := readOneMessage(t, clientConn2)
		mustNotError(t, msg2)
	})

	t.Run("global child under project parent rejected", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		if _, err := s.database.CreateTrack(&projID, "Proj Parent", "proj-parent", "", "", nil); err != nil {
			t.Fatal(err)
		}
		clientConn, serverConn := pipeForHandler(t)
		// Global scope → parent resolution is global-only, so the project
		// parent slug does not resolve.
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "Global Child", Global: true, ParentTrack: "proj-parent"})
		expectError(t, readOneMessage(t, clientConn), "not found")
	})

	t.Run("duplicate slug rejected with friendly message", func(t *testing.T) {
		s, _ := setupServerWithProject(t)
		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTrack(serverConn, CreateTrackRequest{Name: "Dup", ProjectPath: "/tmp/sortie-test"})
		mustNotError(t, readOneMessage(t, clientConn))
		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleCreateTrack(serverConn2, CreateTrackRequest{Name: "Dup", ProjectPath: "/tmp/sortie-test"})
		expectError(t, readOneMessage(t, clientConn2), "already exists")
	})
}

func TestHandleSetTrackContext(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tr, err := s.database.CreateTrack(&projID, "T", "t", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("replace then append", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleSetTrackContext(serverConn, SetTrackContextRequest{ProjectPath: "/tmp/sortie-test", Track: "t", Context: "one"})
		mustNotError(t, readOneMessage(t, clientConn))

		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleSetTrackContext(serverConn2, SetTrackContextRequest{ProjectPath: "/tmp/sortie-test", Track: "t", Context: "two", Mode: "append"})
		mustNotError(t, readOneMessage(t, clientConn2))

		got, _ := s.database.GetTrack(tr.ID)
		if got.Context != "one\n\ntwo" {
			t.Errorf("context = %q, want %q", got.Context, "one\n\ntwo")
		}
	})

	t.Run("invalid mode", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleSetTrackContext(serverConn, SetTrackContextRequest{ProjectPath: "/tmp/sortie-test", Track: "t", Context: "x", Mode: "merge"})
		expectError(t, readOneMessage(t, clientConn), "invalid mode")
	})

	t.Run("unknown ref", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleSetTrackContext(serverConn, SetTrackContextRequest{ProjectPath: "/tmp/sortie-test", Track: "nope", Context: "x"})
		expectError(t, readOneMessage(t, clientConn), "not found")
	})
}

func TestHandleUpdateTaskTrackContext(t *testing.T) {
	t.Run("running step with track succeeds", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		tr, err := s.database.CreateTrack(&projID, "T", "t", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		// createRunningStep uses CreateTask (no track); create a tracked task
		// with a running step by hand instead.
		tk, err := s.database.CreateTaskWithPriority(projID, "tracked", "desc", "tracked", "default", "", "main", "", "", "running", "medium", true, nil, &tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.database.UpdateTaskStep(tk.ID, 0, "implementing"); err != nil {
			t.Fatal(err)
		}
		if err := s.database.CreateTaskStep(tk.ID, "implementing"); err != nil {
			t.Fatal(err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateTaskTrackContext(serverConn, UpdateTaskTrackContextRequest{TaskID: tk.ID, Context: "learned", Mode: "append"})
		mustNotError(t, readOneMessage(t, clientConn))

		got, _ := s.database.GetTrack(tr.ID)
		if got.Context != "learned" {
			t.Errorf("track context = %q, want %q", got.Context, "learned")
		}
	})

	t.Run("task with no track rejected", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		tk := createRunningStep(t, s, projID, "implementing")
		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateTaskTrackContext(serverConn, UpdateTaskTrackContextRequest{TaskID: tk.ID, Context: "x"})
		expectError(t, readOneMessage(t, clientConn), "has no track")
	})

	t.Run("completed task with stale current_step rejected", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		tr, err := s.database.CreateTrack(&projID, "T2", "t2", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Normal completion does not clear tasks.current_step, so a finished
		// task still resolves an active step name — the status guard must
		// reject the write anyway.
		tk, err := s.database.CreateTaskWithPriority(projID, "done", "desc", "done", "default", "", "main", "", "", "completed", "medium", true, nil, &tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.database.UpdateTaskStep(tk.ID, 0, "implementing"); err != nil {
			t.Fatal(err)
		}
		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateTaskTrackContext(serverConn, UpdateTaskTrackContextRequest{TaskID: tk.ID, Context: "late", Mode: "append"})
		expectError(t, readOneMessage(t, clientConn), "has no active step")

		got, _ := s.database.GetTrack(tr.ID)
		if got.Context != "" {
			t.Errorf("track context = %q, want empty (write must be rejected)", got.Context)
		}
	})

	t.Run("task with no active step rejected", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		tr, err := s.database.CreateTrack(&projID, "T", "t", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Pending task, no current step, no engine state.
		tk, err := s.database.CreateTaskWithPriority(projID, "idle", "desc", "idle", "default", "", "main", "", "", "pending", "medium", true, nil, &tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateTaskTrackContext(serverConn, UpdateTaskTrackContextRequest{TaskID: tk.ID, Context: "x"})
		expectError(t, readOneMessage(t, clientConn), "has no active step")
	})
}

func TestHandleListAndGetTrack(t *testing.T) {
	s, projID := setupServerWithProject(t)
	longCtx := strings.Repeat("x", 300)
	root, err := s.database.CreateTrack(nil, "Sprint", "sprint", "", "sprint goal", nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := s.database.CreateTrack(&projID, "Payments", "payments", "", longCtx, &root.ID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("list clears contexts and sets previews", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleListTracks(serverConn, ListTracksRequest{ProjectPath: "/tmp/sortie-test"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[ListTracksResponse](t, msg)
		if len(resp.Tracks) != 2 {
			t.Fatalf("got %d tracks, want 2", len(resp.Tracks))
		}
		for _, info := range resp.Tracks {
			if info.Context != "" {
				t.Errorf("list must clear Context, got %d bytes for %s", len(info.Context), info.Slug)
			}
		}
		// Project first.
		if resp.Tracks[0].Slug != "payments" {
			t.Errorf("first track = %s, want payments", resp.Tracks[0].Slug)
		}
		if resp.Tracks[0].ContextLen != 300 || len(resp.Tracks[0].ContextPreview) != 200 {
			t.Errorf("len=%d preview=%d", resp.Tracks[0].ContextLen, len(resp.Tracks[0].ContextPreview))
		}
	})

	t.Run("preview truncation is rune-safe", func(t *testing.T) {
		// 100 × "€" (3 bytes each) = 300 bytes; a naive 200-byte cut lands
		// mid-rune, so the preview must back up to a boundary (198 bytes).
		if _, err := s.database.CreateTrack(&projID, "Unicode", "unicode", "", strings.Repeat("€", 100), nil); err != nil {
			t.Fatal(err)
		}
		clientConn, serverConn := pipeForHandler(t)
		go s.handleListTracks(serverConn, ListTracksRequest{ProjectPath: "/tmp/sortie-test"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[ListTracksResponse](t, msg)
		for _, info := range resp.Tracks {
			if info.Slug != "unicode" {
				continue
			}
			if !utf8.ValidString(info.ContextPreview) {
				t.Error("preview must be valid UTF-8")
			}
			if len(info.ContextPreview) != 198 {
				t.Errorf("preview len = %d, want 198 (backed up to rune boundary)", len(info.ContextPreview))
			}
			return
		}
		t.Fatal("unicode track not in list")
	})

	t.Run("get returns chain and rendered context", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleGetTrack(serverConn, GetTrackRequest{ProjectPath: "/tmp/sortie-test", Track: "payments"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[GetTrackResponse](t, msg)
		if resp.Track.ID != leaf.ID {
			t.Errorf("track ID = %d, want %d", resp.Track.ID, leaf.ID)
		}
		if len(resp.Chain) != 2 || resp.Chain[0].ID != root.ID || resp.Chain[1].ID != leaf.ID {
			t.Errorf("chain = %+v", resp.Chain)
		}
		chain, _ := s.database.GetTrackChain(leaf.ID)
		if resp.RenderedContext != workflow.FormatTrackChain(chain) {
			t.Errorf("RenderedContext mismatch")
		}
	})

	t.Run("get without project path resolves only global tracks", func(t *testing.T) {
		clientConn, serverConn := pipeForHandler(t)
		go s.handleGetTrack(serverConn, GetTrackRequest{Track: "sprint"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[GetTrackResponse](t, msg)
		if resp.Track.ID != root.ID || !resp.Track.Global {
			t.Errorf("track = %+v, want global root #%d", resp.Track, root.ID)
		}

		// A project-scoped slug must NOT resolve without a project path.
		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleGetTrack(serverConn2, GetTrackRequest{Track: "payments"})
		expectError(t, readOneMessage(t, clientConn2), "not found")
	})
}

func TestCreateTaskFromRequest_Tracks(t *testing.T) {
	projectPath := "/tmp/sortie-test"

	newServer := func(t *testing.T, wfs ...config.WorkflowConfig) (*Server, int64) {
		t.Helper()
		if len(wfs) == 0 {
			wfs = []config.WorkflowConfig{{Name: "default", Steps: []config.StepConfig{{Name: "implementing", Prompt: "p"}}}}
		}
		s, projID := setupServerWithPinnedWorkflow(t, projectPath, wfs[0])
		if len(wfs) > 1 {
			s.projectsMu.Lock()
			s.projects[projID].cfg.Workflows = wfs
			s.projectsMu.Unlock()
		}
		return s, projID
	}

	t.Run("slug attach sets track_id", func(t *testing.T) {
		s, projID := newServer(t)
		tr, err := s.database.CreateTrack(&projID, "Payments", "payments", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "payments"})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.TrackID == nil || *tk.TrackID != tr.ID {
			t.Errorf("TrackID = %v, want %d", tk.TrackID, tr.ID)
		}
	})

	t.Run("numeric ID attach", func(t *testing.T) {
		s, projID := newServer(t)
		tr, err := s.database.CreateTrack(&projID, "Payments", "payments", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: strconv.FormatInt(tr.ID, 10)})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.TrackID == nil || *tk.TrackID != tr.ID {
			t.Errorf("TrackID = %v, want %d", tk.TrackID, tr.ID)
		}
	})

	t.Run("unknown track fails create", func(t *testing.T) {
		s, _ := newServer(t)
		if _, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "nope"}); err == nil {
			t.Fatal("expected error for unknown track")
		}
	})

	t.Run("other project's track ID fails", func(t *testing.T) {
		s, _ := newServer(t)
		otherProj, err := s.database.GetOrCreateProject("/tmp/other-project")
		if err != nil {
			t.Fatal(err)
		}
		otherTr, err := s.database.CreateTrack(&otherProj.ID, "Foreign", "foreign", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: strconv.FormatInt(otherTr.ID, 10)}); err == nil {
			t.Fatal("expected cross-project rejection")
		}
	})

	t.Run("none sentinel is trackless", func(t *testing.T) {
		s, projID := newServer(t)
		if _, err := s.database.CreateTrack(&projID, "Payments", "payments", "", "", nil); err != nil {
			t.Fatal(err)
		}
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "None"})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.TrackID != nil {
			t.Errorf("TrackID = %v, want nil", tk.TrackID)
		}
	})

	t.Run("workflow precedence explicit beats track beats default", func(t *testing.T) {
		wfs := []config.WorkflowConfig{
			{Name: "default", Steps: []config.StepConfig{{Name: "implementing", Prompt: "p"}}},
			{Name: "track-flow", Hidden: true, Steps: []config.StepConfig{{Name: "implementing", Prompt: "p"}}},
			{Name: "explicit-flow", Steps: []config.StepConfig{{Name: "implementing", Prompt: "p"}}},
		}
		s, projID := newServer(t, wfs...)
		if _, err := s.database.CreateTrack(&projID, "Payments", "payments", "track-flow", "", nil); err != nil {
			t.Fatal(err)
		}

		// Track's workflow when request leaves it empty.
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "payments"})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Workflow != "track-flow" {
			t.Errorf("Workflow = %q, want track-flow", tk.Workflow)
		}

		// Explicit request beats the track's workflow.
		tk2, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "payments", Workflow: "explicit-flow"})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk2.Workflow != "explicit-flow" {
			t.Errorf("Workflow = %q, want explicit-flow", tk2.Workflow)
		}

		// UpdateProjectDefaults must have received the raw request workflow —
		// a track's workflow must NOT become the project default.
		proj, err := s.database.GetProject(projID)
		if err != nil {
			t.Fatal(err)
		}
		if proj.DefaultWorkflow == "track-flow" {
			t.Error("track workflow leaked into project defaults")
		}
	})

	t.Run("track referencing unknown workflow errors", func(t *testing.T) {
		s, projID := newServer(t)
		if _, err := s.database.CreateTrack(&projID, "Payments", "payments", "ghost-flow", "", nil); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.createTaskFromRequest(CreateTaskRequest{Description: "d", ProjectPath: projectPath, Track: "payments"})
		if err == nil || !strings.Contains(err.Error(), "unknown workflow") {
			t.Fatalf("expected unknown-workflow error, got %v", err)
		}
	})
}

func TestCreateTasksAndWait_TrackInheritance(t *testing.T) {
	projectPath := "/tmp/sortie-test"
	wf := config.WorkflowConfig{Name: "default", Steps: []config.StepConfig{{Name: "implementing", Prompt: "p"}}}
	s, projID := setupServerWithPinnedWorkflow(t, projectPath, wf)

	parentTrack, err := s.database.CreateTrack(&projID, "Sprint", "sprint", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherTrack, err := s.database.CreateTrack(&projID, "Other", "other", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.database.CreateTaskWithPriority(projID, "parent", "d", "parent", "default", "", "main", "", "", "running", "medium", true, nil, &parentTrack.ID)
	if err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
		ParentTaskID: parent.ID,
		Tasks: []CreateTaskRequest{
			{Description: "inherits", Title: "inherits"},                 // inherits parent's track
			{Description: "override", Title: "override", Track: "other"}, // explicit override
			{Description: "detached", Title: "detached", Track: "none"},  // explicitly trackless
		},
	})
	msg := readOneMessage(t, clientConn)
	mustNotError(t, msg)
	resp := decodePayload[CreateTasksAndWaitResponse](t, msg)
	if len(resp.Children) != 3 {
		t.Fatalf("got %d children, want 3", len(resp.Children))
	}

	check := func(idx int, want *int64) {
		t.Helper()
		child, err := s.database.GetTask(resp.Children[idx].ID)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case want == nil && child.TrackID != nil:
			t.Errorf("child %d: TrackID = %v, want nil", idx, child.TrackID)
		case want != nil && (child.TrackID == nil || *child.TrackID != *want):
			t.Errorf("child %d: TrackID = %v, want %d", idx, child.TrackID, *want)
		}
	}
	check(0, &parentTrack.ID)
	check(1, &otherTrack.ID)
	check(2, nil)
}

func TestTaskToInfoTrackEnrichment(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tr, err := s.database.CreateTrack(&projID, "Payments", "payments", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.database.CreateTaskWithPriority(projID, "T", "d", "t", "", "", "", "", "", "pending", "medium", true, nil, &tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	info := s.taskToInfo(tk)
	if info.TrackID == nil || *info.TrackID != tr.ID {
		t.Errorf("TrackID = %v, want %d", info.TrackID, tr.ID)
	}
	if info.Track != "payments" {
		t.Errorf("Track = %q, want payments", info.Track)
	}
}
