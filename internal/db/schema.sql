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
    description TEXT NOT NULL DEFAULT '',
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
    input TEXT NOT NULL,
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
    -- Agent start directory relative to the task's checkout; '' = checkout root.
    workdir TEXT NOT NULL DEFAULT '',
    exit_code INTEGER,
    error_message TEXT,
    context TEXT,
    images TEXT,
    commits TEXT,
    periodic_id INTEGER REFERENCES periodic_definitions(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    -- Set while a tmux task's interactive session is hibernated (killed to
    -- free memory; the task stays in 'tmux' status and its worktree, branch,
    -- step cursor and agent conversation are intact). NULL = live session.
    hibernated_at DATETIME,
    -- Earliest time (UTC) the poller may claim this pending task. NULL = now.
    not_before DATETIME,
    -- Fanout child identity: the parent task, its fanout step and the
    -- published item slug this task was spawned for. Written in the child's
    -- INSERT; unique per (parent, step, item) via idx_tasks_fanout_item.
    fanout_parent_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    fanout_step TEXT NOT NULL DEFAULT '',
    fanout_item TEXT NOT NULL DEFAULT '',
    fanout_attempts INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_periodic_id ON tasks(periodic_id) WHERE periodic_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_fanout_item ON tasks(fanout_parent_id, fanout_step, fanout_item) WHERE fanout_parent_id IS NOT NULL;

-- periodic_definitions stores routines reconciled from the top-level routines:
-- section of .sakusen.yml. A fire materializes an ordinary tasks row
-- (tasks.periodic_id FK). cadence empty ⇒ an after:-triggered or on-demand
-- routine: it keeps a row (and its run history) but never becomes due, so
-- next_fire_at holds a filler value the due-scan ignores. workflow_ref names the workflow the routine
-- binds. priority empty ⇒ the project default at fire time. deleted_at is set
-- (soft delete) when a routine disappears from the yml so its run history
-- (linked tasks) survives.
CREATE TABLE IF NOT EXISTS periodic_definitions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    cadence TEXT NOT NULL,
    workflow_ref TEXT NOT NULL DEFAULT '',
    input TEXT NOT NULL DEFAULT '',
    priority TEXT NOT NULL DEFAULT '',
    paused INTEGER NOT NULL DEFAULT 0,
    config_paused INTEGER NOT NULL DEFAULT 0,
    next_fire_at DATETIME NOT NULL,
    last_fired_at DATETIME,
    last_task_id INTEGER REFERENCES tasks(id),
    -- Non-empty ⇒ event-triggered: fires when the named upstream routine's
    -- run completes (cadence is '' and next_fire_at is unused).
    -- after_last_task_id is the upstream run already consumed.
    after_name TEXT NOT NULL DEFAULT '',
    after_last_task_id INTEGER,
    deleted_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_periodic_due ON periodic_definitions(next_fire_at) WHERE deleted_at IS NULL AND paused = 0;

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

-- task_published_items holds the latest item list a task's step published for
-- a fanout step to spawn children from: one row per (task, step), replaced on
-- re-publish. items_json is a JSON array of task.PublishedItem.
CREATE TABLE IF NOT EXISTS task_published_items (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    step_name TEXT NOT NULL,
    items_json TEXT NOT NULL,
    published_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (task_id, step_name)
);

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
