CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    default_priority TEXT NOT NULL DEFAULT 'medium',
    default_worktree INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    default_branch_mode INTEGER NOT NULL DEFAULT 0,
    default_workflow TEXT NOT NULL DEFAULT ''
);

-- tracks are named, mutable, hierarchical context containers. Tasks attach to
-- one at create time; step prompts opt into the ancestor-concatenated context
-- via {{track.*}} template vars. project_id NULL = global track. Slug
-- uniqueness is per-scope via the two partial unique indexes below (a plain
-- UNIQUE(project_id, slug) would treat NULL project_ids as distinct).
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER REFERENCES projects(id),
    parent_id INTEGER REFERENCES tracks(id),
    name TEXT NOT NULL DEFAULT '',
    slug TEXT NOT NULL DEFAULT '',
    workflow TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_project_slug ON tracks(project_id, slug) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_global_slug ON tracks(slug) WHERE project_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_tracks_project_id ON tracks(project_id);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    track_id INTEGER REFERENCES tracks(id),
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL,
    slug TEXT NOT NULL DEFAULT '',
    workflow TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    priority TEXT NOT NULL DEFAULT 'medium',
    step_index INTEGER NOT NULL DEFAULT 0,
    current_step TEXT,
    loop_iteration INTEGER NOT NULL DEFAULT 0,
    branch_name TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT '',
    target_branch TEXT NOT NULL DEFAULT '',
    checkout_branch TEXT NOT NULL DEFAULT '',
    worktree INTEGER NOT NULL DEFAULT 1,
    worktree_path TEXT,
    worktree_detached INTEGER NOT NULL DEFAULT 0,
    exit_code INTEGER,
    error_message TEXT,
    context TEXT,
    images TEXT,
    commits TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id);

CREATE TABLE IF NOT EXISTS task_dependencies (
    task_id INTEGER NOT NULL REFERENCES tasks(id),
    blocked_by INTEGER NOT NULL REFERENCES tasks(id),
    PRIMARY KEY (task_id, blocked_by)
);

-- task_waits_on records mid-step suspensions: a parent task whose currently
-- executing step has spawned child tasks (via create_tasks_and_wait or
-- wait_for_tasks) is suspended in StatusAwaitingChildren until every
-- waits_on_id child reaches a terminal status. Semantically distinct from
-- task_dependencies, which is a "blocked before start" relation that persists
-- until manual removal — a waits_on edge is removed automatically once the
-- child reaches terminal status and the parent is re-enqueued at the same step.
CREATE TABLE IF NOT EXISTS task_waits_on (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    waits_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, waits_on_id)
);
CREATE INDEX IF NOT EXISTS idx_task_waits_on_waits_on ON task_waits_on(waits_on_id);

CREATE TABLE IF NOT EXISTS task_steps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    step_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    context TEXT,
    exit_code INTEGER,
    started_at DATETIME,
    completed_at DATETIME,
    UNIQUE(task_id, step_name)
);

CREATE TABLE IF NOT EXISTS chats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    tmux_session_name TEXT,
    step_name TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_chats_task_id ON chats(task_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_chats_task_step ON chats(task_id, step_name);
