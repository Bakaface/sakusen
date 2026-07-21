package daemon

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/workflow"
)

// trackContextPreviewLen is how many bytes of a track's own context list_tracks
// exposes as ContextPreview.
const trackContextPreviewLen = 200

// resolveTrackRef resolves a slug-or-numeric-ID track reference visible from
// projectID (project tracks shadow global on slug collision). projectID nil
// restricts resolution to global tracks. Numeric refs are visibility-checked:
// a track ID belonging to a different project is rejected.
func (s *Server) resolveTrackRef(projectID *int64, ref string) (*task.Track, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("track reference is required")
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		tr, err := s.database.GetTrack(id)
		if err != nil {
			return nil, fmt.Errorf("track #%d not found", id)
		}
		if tr.ProjectID != nil && (projectID == nil || *tr.ProjectID != *projectID) {
			return nil, fmt.Errorf("track #%d belongs to a different project", id)
		}
		return tr, nil
	}
	tr, err := s.database.GetTrackBySlug(projectID, ref)
	if err != nil {
		return nil, fmt.Errorf("track %q not found", ref)
	}
	return tr, nil
}

// validateTrackContextMode normalizes/validates a replace|append mode string
// (empty → replace).
func validateTrackContextMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		return "", fmt.Errorf("invalid mode %q: must be \"replace\" or \"append\"", raw)
	}
	return mode, nil
}

func (s *Server) handleCreateTrack(conn net.Conn, req CreateTrackRequest) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.sendError(conn, "track name is required")
		return
	}
	slug := task.Slugify(name)
	if slug == "" {
		s.sendError(conn, "track name produces an empty slug")
		return
	}

	var projectID *int64
	if !req.Global {
		if req.ProjectPath == "" {
			s.sendError(conn, "project_path is required for project tracks")
			return
		}
		proj, err := s.database.GetOrCreateProject(req.ProjectPath)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
			return
		}
		projectID = &proj.ID
	}

	var parentID *int64
	if req.ParentTrack != "" {
		// For a global child projectID is nil, so only global parents resolve
		// here; db.CreateTrack re-enforces the scope rule as a backstop.
		parent, err := s.resolveTrackRef(projectID, req.ParentTrack)
		if err != nil {
			s.sendError(conn, err.Error())
			return
		}
		parentID = &parent.ID
	}

	tr, err := s.database.CreateTrack(projectID, name, slug, req.Workflow, req.Context, parentID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to create track: %v", err))
		return
	}

	s.sendMessage(conn, MsgCreateTrack, CreateTrackResponse{Track: s.trackToInfo(tr, true)})
}

func (s *Server) handleGetTrack(conn net.Conn, req GetTrackRequest) {
	var projectID *int64
	if req.ProjectPath != "" {
		proj, err := s.database.GetOrCreateProject(req.ProjectPath)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
			return
		}
		projectID = &proj.ID
	}

	tr, err := s.resolveTrackRef(projectID, req.Track)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}

	chain, err := s.database.GetTrackChain(tr.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load track chain: %v", err))
		return
	}

	infos := make([]TrackInfo, len(chain))
	for i, c := range chain {
		infos[i] = s.trackToInfo(c, true)
	}

	s.sendMessage(conn, MsgGetTrack, GetTrackResponse{
		Track:           s.trackToInfo(tr, true),
		Chain:           infos,
		RenderedContext: workflow.FormatTrackChain(chain),
	})
}

func (s *Server) handleListTracks(conn net.Conn, req ListTracksRequest) {
	if req.ProjectPath == "" {
		s.sendError(conn, "project_path is required")
		return
	}
	proj, err := s.database.GetOrCreateProject(req.ProjectPath)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
		return
	}

	tracks, err := s.database.ListTracks(proj.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list tracks: %v", err))
		return
	}

	infos := make([]TrackInfo, len(tracks))
	for i, tr := range tracks {
		info := s.trackToInfo(tr, false)
		preview := tr.Context
		if len(preview) > trackContextPreviewLen {
			cut := trackContextPreviewLen
			// Back up to a rune boundary so the preview stays valid UTF-8.
			for cut > 0 && !utf8.RuneStart(preview[cut]) {
				cut--
			}
			preview = preview[:cut]
		}
		info.ContextPreview = preview
		infos[i] = info
	}

	s.sendMessage(conn, MsgListTracks, ListTracksResponse{Tracks: infos})
}

func (s *Server) handleSetTrackContext(conn net.Conn, req SetTrackContextRequest) {
	mode, err := validateTrackContextMode(req.Mode)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}

	var projectID *int64
	if req.ProjectPath != "" {
		proj, err := s.database.GetOrCreateProject(req.ProjectPath)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
			return
		}
		projectID = &proj.ID
	}

	tr, err := s.resolveTrackRef(projectID, req.Track)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}

	if err := s.database.UpdateTrackContext(tr.ID, req.Context, mode); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update track context: %v", err))
		return
	}

	s.sendMessage(conn, MsgOK, OKResponse{Message: fmt.Sprintf("track %q context updated (%s)", tr.Slug, mode)})
}

// handleUpdateTaskTrackContext is the own-track-only track context write for
// agents (the update_track_context MCP tool), mirroring
// handleUpdateActiveStepContext's enforcement shape: the task must have a
// track AND an active step (running, or paused at a tmux gate) — only a task
// actively in a step may write its track.
func (s *Server) handleUpdateTaskTrackContext(conn net.Conn, req UpdateTaskTrackContextRequest) {
	if req.TaskID <= 0 {
		s.sendError(conn, "task_id is required")
		return
	}
	mode, err := validateTrackContextMode(req.Mode)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task #%d: %v", req.TaskID, err))
		return
	}
	if t.TrackID == nil {
		s.sendError(conn, fmt.Sprintf("task #%d has no track", req.TaskID))
		return
	}
	// current_step is not cleared on normal completion, so ResolveActiveStep
	// alone would still resolve a step for a finished task. Require the task
	// itself to be actively executing (unlike update_step_context, this write
	// has no second running-row guard at the DB layer).
	if !t.Status.IsActive() {
		s.sendError(conn, fmt.Sprintf("task #%d has no active step (status %s)", req.TaskID, t.Status))
		return
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project context for task #%d: %v", req.TaskID, err))
		return
	}
	if activeStep, _ := pc.engine.ResolveActiveStep(t); activeStep == "" {
		s.sendError(conn, fmt.Sprintf("task #%d has no active step", req.TaskID))
		return
	}

	if err := s.database.UpdateTrackContext(*t.TrackID, req.Context, mode); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update track context: %v", err))
		return
	}

	s.sendMessage(conn, MsgOK, OKResponse{Message: fmt.Sprintf("track context updated (%s)", mode)})
}

// trackToInfo converts a *task.Track to its wire projection. includeContext
// controls whether the full own context is carried (create/get) or cleared
// (list — the caller sets ContextPreview instead). ContextLen is always set.
func (s *Server) trackToInfo(tr *task.Track, includeContext bool) TrackInfo {
	info := TrackInfo{
		ID:         tr.ID,
		ProjectID:  tr.ProjectID,
		Global:     tr.ProjectID == nil,
		ParentID:   tr.ParentID,
		Name:       tr.Name,
		Slug:       tr.Slug,
		Workflow:   tr.Workflow,
		ContextLen: len(tr.Context),
		CreatedAt:  tr.CreatedAt,
		UpdatedAt:  tr.UpdatedAt,
	}
	if includeContext {
		info.Context = tr.Context
	}
	if tr.ParentID != nil {
		// Best-effort enrichment: a failed lookup leaves ParentSlug empty.
		if parent, err := s.database.GetTrack(*tr.ParentID); err == nil {
			info.ParentSlug = parent.Slug
		}
	}
	return info
}
