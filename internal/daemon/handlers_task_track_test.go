package daemon

import (
	"strconv"
	"testing"
)

// TestHandleUpdateFieldTrack covers the update-task "track" field: attaching
// by slug, re-attaching by numeric ID, detaching with the "none" sentinel, and
// the two rejections resolveTrackRef is responsible for (unknown ref, track
// owned by another project).
func TestHandleUpdateFieldTrack(t *testing.T) {
	t.Run("attach by slug, change by numeric ID, detach with none", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		first, err := s.database.CreateTrack(&projID, "First", "first", "", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.database.CreateTrack(&projID, "Second", "second", "", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tk, err := s.database.CreateTask(projID, "Task", "input", "task", "", "", "pending", nil)
		if err != nil {
			t.Fatal(err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateField(serverConn, UpdateFieldRequest{TaskID: tk.ID, Field: "track", Value: "first"})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[UpdateFieldResponse](t, msg)
		if resp.Task.TrackID == nil || *resp.Task.TrackID != first.ID || resp.Task.Track != "first" {
			t.Errorf("after attach task = %+v, want track %d/%q", resp.Task, first.ID, "first")
		}

		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleUpdateField(serverConn2, UpdateFieldRequest{TaskID: tk.ID, Field: "track", Value: strconv.FormatInt(second.ID, 10)})
		msg2 := readOneMessage(t, clientConn2)
		mustNotError(t, msg2)
		resp2 := decodePayload[UpdateFieldResponse](t, msg2)
		if resp2.Task.TrackID == nil || *resp2.Task.TrackID != second.ID || resp2.Task.Track != "second" {
			t.Errorf("after change task = %+v, want track %d/%q", resp2.Task, second.ID, "second")
		}

		clientConn3, serverConn3 := pipeForHandler(t)
		go s.handleUpdateField(serverConn3, UpdateFieldRequest{TaskID: tk.ID, Field: "track", Value: "none"})
		msg3 := readOneMessage(t, clientConn3)
		mustNotError(t, msg3)
		resp3 := decodePayload[UpdateFieldResponse](t, msg3)
		if resp3.Task.TrackID != nil {
			t.Errorf("after detach TrackID = %d, want nil", *resp3.Task.TrackID)
		}
	})

	t.Run("unknown track rejected and the task left untouched", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		tr, err := s.database.CreateTrack(&projID, "First", "first", "", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tk, err := s.database.CreateTask(projID, "Task", "input", "task", "", "", "pending", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.database.UpdateTaskTrack(tk.ID, &tr.ID); err != nil {
			t.Fatal(err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateField(serverConn, UpdateFieldRequest{TaskID: tk.ID, Field: "track", Value: "nope"})
		expectError(t, readOneMessage(t, clientConn), `track "nope" not found`)

		got, err := s.database.GetTask(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.TrackID == nil || *got.TrackID != tr.ID {
			t.Errorf("TrackID = %v, want the original %d", got.TrackID, tr.ID)
		}
	})

	t.Run("track owned by another project rejected", func(t *testing.T) {
		s, projID := setupServerWithProject(t)
		other, err := s.database.GetOrCreateProject("/tmp/sakusen-other")
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := s.database.CreateTrack(&other.ID, "Foreign", "foreign", "", "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		tk, err := s.database.CreateTask(projID, "Task", "input", "task", "", "", "pending", nil)
		if err != nil {
			t.Fatal(err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleUpdateField(serverConn, UpdateFieldRequest{TaskID: tk.ID, Field: "track", Value: strconv.FormatInt(foreign.ID, 10)})
		expectError(t, readOneMessage(t, clientConn), "belongs to a different project")
	})
}
