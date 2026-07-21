package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Bakaface/sortie/internal/task"
)

// maxTrackDepth caps the length of a track's ancestor chain (leaf included).
// Enforced at creation time in CreateTrack; since tracks cannot be re-parented,
// this is also the only cycle guard the chain walk needs.
const maxTrackDepth = 10

const trackColumns = `id, project_id, parent_id, name, slug, workflow, context, created_at, updated_at`

// CreateTrack inserts a new track after validating slug, parent scope, and
// chain depth. projectID nil creates a global track (visible from every
// project). Scope rule: a parent must be same-or-broader scope — a project
// track may have a global parent or a parent in the same project, but a global
// track may never have a project-scoped parent, and cross-project parents are
// forbidden.
func (db *DB) CreateTrack(projectID *int64, name, slug, workflow, context string, parentID *int64) (*task.Track, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("track slug cannot be empty")
	}
	// Purely numeric slugs are forbidden: every resolution surface
	// (resolveTrackRef, --track, --parent) tries numeric refs as track IDs
	// first, so such a slug would be unresolvable — or, worse, silently
	// resolve to a different track with that ID.
	if _, err := strconv.ParseInt(slug, 10, 64); err == nil {
		return nil, fmt.Errorf("track slug %q cannot be purely numeric (numeric references resolve as track IDs)", slug)
	}

	if parentID != nil {
		parent, err := db.GetTrack(*parentID)
		if err != nil {
			return nil, fmt.Errorf("parent track #%d not found", *parentID)
		}
		if parent.ProjectID != nil {
			if projectID == nil {
				return nil, fmt.Errorf("global track cannot have a project-scoped parent (track %q)", parent.Slug)
			}
			if *projectID != *parent.ProjectID {
				return nil, fmt.Errorf("parent track %q belongs to a different project", parent.Slug)
			}
		}
		chain, err := db.GetTrackChain(*parentID)
		if err != nil {
			return nil, err
		}
		if len(chain) >= maxTrackDepth {
			return nil, fmt.Errorf("track chain would exceed max depth %d", maxTrackDepth)
		}
	}

	result, err := db.sqlDB.Exec(
		`INSERT INTO tracks (project_id, parent_id, name, slug, workflow, context) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, parentID, name, slug, workflow, context,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("track %q already exists in this scope", slug)
		}
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.GetTrack(id)
}

func (db *DB) GetTrack(id int64) (*task.Track, error) {
	row := db.sqlDB.QueryRow(fmt.Sprintf(`SELECT %s FROM tracks WHERE id = ?`, trackColumns), id)
	return scanTrackRow(row)
}

// GetTrackBySlug resolves a slug. With a non-nil projectID, that project's
// tracks are consulted first, then global tracks (project shadows global on
// slug collision). With a nil projectID, only global tracks resolve.
func (db *DB) GetTrackBySlug(projectID *int64, slug string) (*task.Track, error) {
	var row *sql.Row
	if projectID != nil {
		// project_id IS NULL sorts false (0) before true (1), so a project
		// track wins over a global track with the same slug.
		row = db.sqlDB.QueryRow(fmt.Sprintf(
			`SELECT %s FROM tracks WHERE slug = ? AND (project_id = ? OR project_id IS NULL) ORDER BY project_id IS NULL LIMIT 1`,
			trackColumns), slug, *projectID)
	} else {
		row = db.sqlDB.QueryRow(fmt.Sprintf(
			`SELECT %s FROM tracks WHERE slug = ? AND project_id IS NULL`, trackColumns), slug)
	}
	return scanTrackRow(row)
}

// ListTracks returns the tracks visible from a project: its own plus all
// global tracks, project tracks first, alphabetical by slug within each group.
func (db *DB) ListTracks(projectID int64) ([]*task.Track, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(
		`SELECT %s FROM tracks WHERE project_id = ? OR project_id IS NULL ORDER BY project_id IS NULL, slug`,
		trackColumns), projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tracks []*task.Track
	for rows.Next() {
		tr, err := scanTrackRow(rows)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, tr)
	}
	return tracks, rows.Err()
}

// UpdateTrackContext sets (mode "replace") or appends (mode "append", with a
// blank-line separator) the track's own context. Append is a single SQL
// statement — no read-modify-write race under concurrent agents.
func (db *DB) UpdateTrackContext(id int64, context, mode string) error {
	var result sql.Result
	var err error
	switch mode {
	case "append":
		result, err = db.sqlDB.Exec(
			`UPDATE tracks SET context = CASE WHEN context = '' THEN ?1 ELSE context || char(10) || char(10) || ?1 END, updated_at = ?2 WHERE id = ?3`,
			context, time.Now(), id,
		)
	case "replace", "":
		result, err = db.sqlDB.Exec(
			`UPDATE tracks SET context = ?, updated_at = ? WHERE id = ?`,
			context, time.Now(), id,
		)
	default:
		return fmt.Errorf("invalid track context mode %q: must be \"replace\" or \"append\"", mode)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("track not found")
	}
	return nil
}

// GetTrackChain returns the track and its ancestors, root-first. Follows the
// in-Go loop convention of this package (see HasCircularDependency) rather
// than a recursive CTE. Errors if the chain exceeds maxTrackDepth hops.
func (db *DB) GetTrackChain(id int64) ([]*task.Track, error) {
	var chain []*task.Track
	cur := id
	for i := 0; i < maxTrackDepth; i++ {
		tr, err := db.GetTrack(cur)
		if err != nil {
			return nil, fmt.Errorf("track #%d not found in chain: %w", cur, err)
		}
		chain = append(chain, tr)
		if tr.ParentID == nil {
			// Reverse to root-first order.
			for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
				chain[l], chain[r] = chain[r], chain[l]
			}
			return chain, nil
		}
		cur = *tr.ParentID
	}
	return nil, fmt.Errorf("track chain exceeds max depth %d (possible cycle)", maxTrackDepth)
}

func scanTrackRow(s scanner) (*task.Track, error) {
	var tr task.Track
	var projectID, parentID sql.NullInt64
	var createdAt, updatedAt sql.NullTime

	err := s.Scan(&tr.ID, &projectID, &parentID, &tr.Name, &tr.Slug, &tr.Workflow, &tr.Context, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if projectID.Valid {
		v := projectID.Int64
		tr.ProjectID = &v
	}
	if parentID.Valid {
		v := parentID.Int64
		tr.ParentID = &v
	}
	if createdAt.Valid {
		tr.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		tr.UpdatedAt = updatedAt.Time
	}
	return &tr, nil
}
