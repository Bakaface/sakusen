package db

import (
	"path/filepath"
	"testing"
)

// TestLatestSchemaVersionMatchesMigrationLadder guards the single-sourcing
// this test file is named after: latestSchemaVersion (used to stamp fresh
// installs) is derived from len(migrations) rather than hardcoded, so it
// cannot silently drift from the upgrade ladder the way two independent
// literals could.
func TestLatestSchemaVersionMatchesMigrationLadder(t *testing.T) {
	if len(migrations) == 0 {
		t.Fatal("migrations ladder is empty")
	}
	// migrations[0] upgrades the implicit pre-migration version (1) to
	// version 2, so the last entry's target version is len(migrations)+1.
	want := len(migrations) + 1
	if latestSchemaVersion != want {
		t.Errorf("latestSchemaVersion = %d, want %d (derived from len(migrations)=%d)", latestSchemaVersion, want, len(migrations))
	}
}

// TestMigrateReappliesFromOlderRecordedVersion exercises the actual upgrade
// loop in migrate() (not just the version==0 fresh-install path every other
// test in this package takes): starting from a fully-migrated fresh
// database, roll schema_version back and undo the schema objects the last
// migration creates, then re-run migrate() and confirm it walks forward from
// the rolled-back version, recreates them via migrateV20, and re-stamps
// schema_version at latestSchemaVersion.
func TestMigrateReappliesFromOlderRecordedVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Simulate a database that was last migrated at version 19: drop the
	// tracks table and the tasks.track_id column migrateV20 adds, and roll
	// the recorded version back.
	if _, err := database.sqlDB.Exec(`DROP TABLE tracks`); err != nil {
		t.Fatalf("failed to drop tracks: %v", err)
	}
	if _, err := database.sqlDB.Exec(`ALTER TABLE tasks DROP COLUMN track_id`); err != nil {
		t.Fatalf("failed to drop tasks.track_id: %v", err)
	}
	if _, err := database.sqlDB.Exec(`UPDATE schema_version SET version = 19`); err != nil {
		t.Fatalf("failed to roll back schema_version: %v", err)
	}

	if err := database.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var version int
	row := database.sqlDB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&version); err != nil {
		t.Fatalf("failed to read schema version: %v", err)
	}
	if version != latestSchemaVersion {
		t.Errorf("schema_version after re-migrate = %d, want %d", version, latestSchemaVersion)
	}

	var count int
	row = database.sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tracks'`)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("failed to check tracks existence: %v", err)
	}
	if count != 1 {
		t.Errorf("expected migrateV20 to recreate tracks, got count=%d", count)
	}

	// The re-applied ALTER TABLE must have restored tasks.track_id too.
	if _, err := database.sqlDB.Exec(`SELECT track_id FROM tasks LIMIT 1`); err != nil {
		t.Errorf("expected migrateV20 to restore tasks.track_id: %v", err)
	}
}

// TestMigrateV21AddsTrackDescription simulates a database last migrated at
// version 20 (tracks exist, but without a description column) and confirms the
// upgrade adds the column without disturbing existing rows.
func TestMigrateV21AddsTrackDescription(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	if _, err := database.sqlDB.Exec(`INSERT INTO tracks (name, slug, context) VALUES ('Legacy', 'legacy', 'kept')`); err != nil {
		t.Fatalf("failed to seed track: %v", err)
	}
	if _, err := database.sqlDB.Exec(`ALTER TABLE tracks DROP COLUMN description`); err != nil {
		t.Fatalf("failed to drop tracks.description: %v", err)
	}
	if _, err := database.sqlDB.Exec(`UPDATE schema_version SET version = 20`); err != nil {
		t.Fatalf("failed to roll back schema_version: %v", err)
	}

	if err := database.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tr, err := database.GetTrackBySlug(nil, "legacy")
	if err != nil {
		t.Fatalf("GetTrackBySlug after migrate: %v", err)
	}
	if tr.Description != "" {
		t.Errorf("pre-existing row description = %q, want the column default", tr.Description)
	}
	if tr.Context != "kept" {
		t.Errorf("context = %q, want %q", tr.Context, "kept")
	}
}
