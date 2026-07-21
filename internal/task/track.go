package task

import "time"

// Track is a named, mutable, hierarchical context container. Tasks attach to a
// track at create time; the track's ancestor-concatenated context is injectable
// into step prompts via {{track.*}} template vars. ProjectID nil = global track
// (attachable from any project); non-nil scopes the track to that project.
// ParentID nil = root track. Parents must be same-or-broader scope: a project
// track may have a global parent, but not vice versa.
type Track struct {
	ID        int64
	ProjectID *int64
	ParentID  *int64
	Name      string
	Slug      string
	Workflow  string // optional workflow name (often namespaced "<slug>:<name>")
	Context   string
	CreatedAt time.Time
	UpdatedAt time.Time
}
