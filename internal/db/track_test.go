package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

func openTrackTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func trackTestProject(t *testing.T, database *DB, path string) int64 {
	t.Helper()
	proj, err := database.GetOrCreateProject(path)
	if err != nil {
		t.Fatalf("GetOrCreateProject(%s): %v", path, err)
	}
	return proj.ID
}

func TestCreateTrackRoundTrip(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	created, err := database.CreateTrack(&projID, "Payments API", "payments-api", "pay:impl", "seed context", "", nil)
	if err != nil {
		t.Fatalf("CreateTrack: %v", err)
	}
	if created.ID == 0 {
		t.Error("expected non-zero track ID")
	}
	if created.Name != "Payments API" || created.Slug != "payments-api" {
		t.Errorf("name/slug = %q/%q", created.Name, created.Slug)
	}
	if created.Workflow != "pay:impl" || created.Context != "seed context" {
		t.Errorf("workflow/context = %q/%q", created.Workflow, created.Context)
	}
	if created.ProjectID == nil || *created.ProjectID != projID {
		t.Errorf("ProjectID = %v, want %d", created.ProjectID, projID)
	}
	if created.ParentID != nil {
		t.Errorf("ParentID = %v, want nil", created.ParentID)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be set")
	}

	byID, err := database.GetTrack(created.ID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if byID.Slug != "payments-api" {
		t.Errorf("GetTrack slug = %q", byID.Slug)
	}

	bySlug, err := database.GetTrackBySlug(&projID, "payments-api")
	if err != nil {
		t.Fatalf("GetTrackBySlug: %v", err)
	}
	if bySlug.ID != created.ID {
		t.Errorf("GetTrackBySlug ID = %d, want %d", bySlug.ID, created.ID)
	}
}

func TestCreateTrackEmptySlugRejected(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")
	if _, err := database.CreateTrack(&projID, "x", "", "", "", "", nil); err == nil {
		t.Fatal("expected error for empty slug")
	}
}

// TestCreateTrackNumericSlugRejected: every resolution surface tries numeric
// refs as track IDs first, so a purely numeric slug (e.g. from a track named
// "2024") would be unresolvable by slug — reject it at creation.
func TestCreateTrackNumericSlugRejected(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")
	if _, err := database.CreateTrack(&projID, "2024", "2024", "", "", "", nil); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("expected numeric-slug rejection, got %v", err)
	}
}

func TestTrackSlugUniquenessPerScope(t *testing.T) {
	database := openTrackTestDB(t)
	projA := trackTestProject(t, database, "/tmp/proj-a")
	projB := trackTestProject(t, database, "/tmp/proj-b")

	if _, err := database.CreateTrack(&projA, "Sprint", "sprint", "", "", "", nil); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Duplicate slug in the same project → error with friendly message.
	if _, err := database.CreateTrack(&projA, "Sprint", "sprint", "", "", "", nil); err == nil {
		t.Fatal("expected duplicate slug error in same project")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected friendly duplicate error, got %v", err)
	}
	// Same slug in another project → OK.
	if _, err := database.CreateTrack(&projB, "Sprint", "sprint", "", "", "", nil); err != nil {
		t.Fatalf("same slug in other project should succeed: %v", err)
	}
	// Same slug as a global track → OK (coexists with project tracks).
	globalTr, err := database.CreateTrack(nil, "Sprint", "sprint", "", "global ctx", "", nil)
	if err != nil {
		t.Fatalf("global track with same slug should succeed: %v", err)
	}
	// Duplicate GLOBAL slug → error (proves the partial index on project_id IS NULL).
	if _, err := database.CreateTrack(nil, "Sprint", "sprint", "", "", "", nil); err == nil {
		t.Fatal("expected duplicate global slug error")
	}

	// Project shadows global on slug resolution.
	got, err := database.GetTrackBySlug(&projA, "sprint")
	if err != nil {
		t.Fatalf("GetTrackBySlug: %v", err)
	}
	if got.ProjectID == nil || *got.ProjectID != projA {
		t.Errorf("expected project track to shadow global, got ProjectID=%v", got.ProjectID)
	}

	// Nil projectID resolves only the global track.
	gotGlobal, err := database.GetTrackBySlug(nil, "sprint")
	if err != nil {
		t.Fatalf("GetTrackBySlug(nil): %v", err)
	}
	if gotGlobal.ID != globalTr.ID {
		t.Errorf("expected global track %d, got %d", globalTr.ID, gotGlobal.ID)
	}
}

func TestGetTrackChainRootFirst(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	root, err := database.CreateTrack(nil, "Root", "root", "", "root ctx", "", nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	mid, err := database.CreateTrack(&projID, "Mid", "mid", "", "mid ctx", "", &root.ID)
	if err != nil {
		t.Fatalf("create mid: %v", err)
	}
	leaf, err := database.CreateTrack(&projID, "Leaf", "leaf", "", "leaf ctx", "", &mid.ID)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}

	chain, err := database.GetTrackChain(leaf.ID)
	if err != nil {
		t.Fatalf("GetTrackChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(chain))
	}
	if chain[0].ID != root.ID || chain[1].ID != mid.ID || chain[2].ID != leaf.ID {
		t.Errorf("chain order = [%d %d %d], want root-first [%d %d %d]",
			chain[0].ID, chain[1].ID, chain[2].ID, root.ID, mid.ID, leaf.ID)
	}
}

func TestCreateTrackDepthCap(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	var parentID *int64
	for i := 0; i < maxTrackDepth; i++ {
		tr, err := database.CreateTrack(&projID, "T", trackDepthSlug(i), "", "", "", parentID)
		if err != nil {
			t.Fatalf("create depth %d: %v", i, err)
		}
		parentID = &tr.ID
	}
	// Depth is now maxTrackDepth; one more child must be rejected.
	if _, err := database.CreateTrack(&projID, "T", "too-deep", "", "", "", parentID); err == nil {
		t.Fatal("expected depth-cap error")
	} else if !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected depth error, got %v", err)
	}
}

func trackDepthSlug(i int) string {
	return "depth-" + string(rune('a'+i))
}

func TestCreateTrackScopeRules(t *testing.T) {
	database := openTrackTestDB(t)
	projA := trackTestProject(t, database, "/tmp/proj-a")
	projB := trackTestProject(t, database, "/tmp/proj-b")

	globalParent, err := database.CreateTrack(nil, "Global Parent", "global-parent", "", "", "", nil)
	if err != nil {
		t.Fatalf("create global parent: %v", err)
	}
	projParent, err := database.CreateTrack(&projA, "Proj Parent", "proj-parent", "", "", "", nil)
	if err != nil {
		t.Fatalf("create project parent: %v", err)
	}

	// Project child → global parent: OK.
	if _, err := database.CreateTrack(&projA, "Child", "child-1", "", "", "", &globalParent.ID); err != nil {
		t.Errorf("project child under global parent should succeed: %v", err)
	}
	// Project child → same-project parent: OK.
	if _, err := database.CreateTrack(&projA, "Child", "child-2", "", "", "", &projParent.ID); err != nil {
		t.Errorf("project child under same-project parent should succeed: %v", err)
	}
	// Global child → project parent: forbidden.
	if _, err := database.CreateTrack(nil, "Child", "child-3", "", "", "", &projParent.ID); err == nil {
		t.Error("global child under project parent should fail")
	}
	// Cross-project parent: forbidden.
	if _, err := database.CreateTrack(&projB, "Child", "child-4", "", "", "", &projParent.ID); err == nil {
		t.Error("cross-project parent should fail")
	}
	// Missing parent: error.
	missing := int64(9999)
	if _, err := database.CreateTrack(&projA, "Child", "child-5", "", "", "", &missing); err == nil {
		t.Error("missing parent should fail")
	}
}

func TestUpdateTrackContext(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	tr, err := database.CreateTrack(&projID, "T", "t", "", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTrack: %v", err)
	}

	// Append onto empty: no leading separator.
	if err := database.UpdateTrackContext(tr.ID, "first", "append"); err != nil {
		t.Fatalf("append onto empty: %v", err)
	}
	got, _ := database.GetTrack(tr.ID)
	if got.Context != "first" {
		t.Errorf("context = %q, want %q", got.Context, "first")
	}

	// Append onto non-empty: blank-line separator.
	if err := database.UpdateTrackContext(tr.ID, "second", "append"); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ = database.GetTrack(tr.ID)
	if got.Context != "first\n\nsecond" {
		t.Errorf("context = %q, want %q", got.Context, "first\n\nsecond")
	}

	// Replace.
	if err := database.UpdateTrackContext(tr.ID, "replaced", "replace"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = database.GetTrack(tr.ID)
	if got.Context != "replaced" {
		t.Errorf("context = %q, want %q", got.Context, "replaced")
	}

	// Unknown id.
	if err := database.UpdateTrackContext(9999, "x", "replace"); err == nil {
		t.Error("expected 'track not found' for unknown id")
	} else if !strings.Contains(err.Error(), "track not found") {
		t.Errorf("expected 'track not found', got %v", err)
	}

	// Invalid mode.
	if err := database.UpdateTrackContext(tr.ID, "x", "merge"); err == nil {
		t.Error("expected invalid-mode error")
	}
}

func TestTrackDescription(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	created, err := database.CreateTrack(&projID, "Payments API", "payments-api", "", "seed context", "Owns the payments API surface", nil)
	if err != nil {
		t.Fatalf("CreateTrack: %v", err)
	}
	if created.Description != "Owns the payments API surface" {
		t.Errorf("created description = %q", created.Description)
	}
	got, err := database.GetTrack(created.ID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got.Description != "Owns the payments API surface" {
		t.Errorf("round-tripped description = %q", got.Description)
	}
	if got.Context != "seed context" {
		t.Errorf("description write must not disturb context, got %q", got.Context)
	}

	before := got.UpdatedAt
	if err := database.UpdateTrackDescription(created.ID, "Now covers refunds too"); err != nil {
		t.Fatalf("UpdateTrackDescription: %v", err)
	}
	got, _ = database.GetTrack(created.ID)
	if got.Description != "Now covers refunds too" {
		t.Errorf("description = %q, want %q", got.Description, "Now covers refunds too")
	}
	if got.Context != "seed context" {
		t.Errorf("context = %q, want it untouched by a description write", got.Context)
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt = %v, want it bumped past %v", got.UpdatedAt, before)
	}

	if err := database.UpdateTrackDescription(9999, "x"); err == nil {
		t.Error("expected 'track not found' for unknown id")
	} else if !strings.Contains(err.Error(), "track not found") {
		t.Errorf("expected 'track not found', got %v", err)
	}
}

func TestListTracksVisibility(t *testing.T) {
	database := openTrackTestDB(t)
	projA := trackTestProject(t, database, "/tmp/proj-a")
	projB := trackTestProject(t, database, "/tmp/proj-b")

	mustCreate := func(projectID *int64, name, slug string) {
		t.Helper()
		if _, err := database.CreateTrack(projectID, name, slug, "", "", "", nil); err != nil {
			t.Fatalf("CreateTrack(%s): %v", slug, err)
		}
	}
	mustCreate(&projA, "Zeta", "zeta")
	mustCreate(&projA, "Alpha", "alpha")
	mustCreate(&projB, "Other", "other")
	mustCreate(nil, "Global B", "g-bravo")
	mustCreate(nil, "Global A", "g-alpha")

	tracks, err := database.ListTracks(projA)
	if err != nil {
		t.Fatalf("ListTracks: %v", err)
	}
	var slugs []string
	for _, tr := range tracks {
		slugs = append(slugs, tr.Slug)
	}
	want := []string{"alpha", "zeta", "g-alpha", "g-bravo"} // project first, alpha within groups
	if len(slugs) != len(want) {
		t.Fatalf("slugs = %v, want %v", slugs, want)
	}
	for i := range want {
		if slugs[i] != want[i] {
			t.Fatalf("slugs = %v, want %v", slugs, want)
		}
	}
}

func TestTaskTrackIDRoundTrip(t *testing.T) {
	database := openTrackTestDB(t)
	projID := trackTestProject(t, database, "/tmp/track-proj")

	tr, err := database.CreateTrack(&projID, "T", "t", "", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTrack: %v", err)
	}

	withTrack, err := database.CreateTaskWithPriority(projID, "Task", "desc", "task", "", "", "", "", "", task.StatusPending, task.PriorityMedium, true, nil, &tr.ID)
	if err != nil {
		t.Fatalf("CreateTaskWithPriority: %v", err)
	}
	fetched, err := database.GetTask(withTrack.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if fetched.TrackID == nil || *fetched.TrackID != tr.ID {
		t.Errorf("TrackID = %v, want %d", fetched.TrackID, tr.ID)
	}

	trackless, err := database.CreateTaskWithPriority(projID, "Task2", "desc", "task2", "", "", "", "", "", task.StatusPending, task.PriorityMedium, true, nil, nil)
	if err != nil {
		t.Fatalf("CreateTaskWithPriority (trackless): %v", err)
	}
	fetched2, err := database.GetTask(trackless.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if fetched2.TrackID != nil {
		t.Errorf("TrackID = %v, want nil", fetched2.TrackID)
	}
}
