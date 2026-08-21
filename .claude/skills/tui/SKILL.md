---
name: tui
description: >
  Sakusen's BubbleTea terminal UI architecture, component patterns, and conventions.
  Use when editing files in internal/tui/, working on terminal UI components, BubbleTea
  models, Lip Gloss styling, keybindings, views, or task list/detail rendering.
---

# TUI Architecture

## Component Hierarchy

Single root `Model` (app.go) with nested sub-views, switched via `view` enum (`viewList`, `viewDetail`, `viewTaskInfo`, `viewPrompt`, `viewArtifact`):

```
Model (app.go)
  listView       (list.go)            Task table with search/filtering
  detailView     (detail.go)          Live logs with follow mode
  taskInfoView   (task_info.go)       Task metadata + workflow progress
  promptView     (prompt.go)          New task creation form
  artifactViewState (artifact_view.go) Artifact file viewer
```

## File Map

### Core

| File | Responsibility |
|------|----------------|
| `app.go` | Root Model, Init/Update/View routing, global state, message handling |
| `keys.go` | KeyMap structs for each view mode |
| `styles.go` | Pre-computed `lipgloss.Style` maps, status icons, adaptive colors |

### Views

| File | Responsibility |
|------|----------------|
| `list.go` | Task table with custom row rendering, scroll offset, search matching |
| `detail.go` | Viewport-based log viewer with follow mode, ANSI stripping |
| `prompt.go` | Dual-field form (textarea + textinput), worktree toggle (`alt+W`), pane cycling (`CyclePane`), image detection |
| `task_info.go` | Metadata display + workflow step progress (icons: `○`/`●`/`✓`/`✗`) |
| `artifact_view.go` | Step context viewer (fetches from daemon RPC, not disk) |

### Update Handlers

| File | Responsibility |
|------|----------------|
| `update_list.go` | List view: navigation, task actions, selections, search, commands |
| `update_detail.go` | Detail view: scroll, follow toggle, back |
| `update_task_info.go` | Task info: scroll, copy-to-clipboard (yd/yc), edit fields, artifact selection |
| `update_prompt.go` | Prompt view: three-layer dispatch (shared keys in `handlePromptKey` → `handleTaskPaneKey` / `handleWorkflowPaneKey`), submit via `handlePromptSubmit` |
| `update_artifact.go` | Artifact selection/viewing: list navigation, open/edit |

### Actions & Utilities

| File | Responsibility |
|------|----------------|
| `actions.go` | Async `tea.Cmd` functions: API calls, editor spawning, tmux attachment, log loading |
| `chords.go` | Declarative two-key chord registry (`chordRegistry` map[view]map[string]chordEntry). Add new chords by appending to the registry in `init()`. Single `pendingChord` field replaces per-chord booleans. Includes `tryChord()` dispatcher and all chord handler functions. |
| `command.go` | Vim-style `:` command parsing with declarative option registry (`boolOption`/`intOption` slices → `matchSetOption`/`execSetOption`). Add new `:set` options by appending to `boolOptions` or `intOptions`. Also: goto line, RunTask, noh. |
| `selector.go` | Generic list-pick dialog: `selector` struct with `HandleKey()`/`View()`, vim navigation, number-key quick select. Used for workflow, task, init, priority, artifact selection. |
| `search.go` | Forward/backward search with match highlighting via `performSearch()`, `nextMatch()`, `previousMatch()` |
| `tree.go` | Task-tree view (parent/dependency tree rendering used in the list and detail views) |
| `history.go` | Per-task history navigation (previous step contexts, log scrollback) |

### Misc

| File | Responsibility |
|------|----------------|
| `sakusen_animation.go` | Splash/idle animation with plane flyover and ASCII art, driven by `sakusenTickMsg` |

## Custom Message Types

Task updates: `taskUpdateMsg`, `taskCreatedMsg`, `taskDeletedMsg`, `tasksLoadedMsg`, `taskFieldUpdatedMsg`
Output: `outputLoadedMsg`, `stepContextsLoadedMsg`, `branchesLoadedMsg`
Editor: `editorFinishedMsg`, `editorPromptFinishedMsg`, `editorFieldFinishedMsg`, `editorLogFinishedMsg`
System: `clientConnectedMsg`, `tmuxDetachedMsg`, `tmuxSessionsMsg`, `tickMsg`, `errorMsg`

## Key Patterns

### Keybindings
- Define in `keys.go` as `keyMap` structs with `key.Binding` fields
- Multiple KeyMaps: `keyMap` (list), `detailFollowKeyMap`, `detailNormalKeyMap`, `taskInfoKeyMap`, `artifactViewKeyMap`, `promptKeyMap`
- Vim-style mnemonics: `gg`/`G`, `dd`, `/`/`?`, `:`

### Chord Sequences
Two-key sequences (dd, gg, oa, ea, ed, et, ec, yd, yc) are handled by a declarative registry in `chords.go`. A single `pendingChord string` field on Model tracks the first key. `tryChord(keyStr)` is called at the top of `handleListKey`/`handleTaskInfoKey` — returns `(model, cmd, true)` if consumed. Add new chords by appending to `chordRegistry[view]` in `init()`. Note: detail/artifact/selector views still use their own `pendingG` for `gg` — only list and taskInfo views use the chord system.

### Message Flow
User key -> handler returns `tea.Cmd` -> goroutine -> `tea.Msg` -> `Update()` processes. Async API calls in `actions.go`, return typed messages.

### Selection/Confirmation Dialogs
Generic `selector` struct (`selector.go`) handles all list-pick dialogs (workflow, task, init, priority, artifact). Add new selectors by appending a `selectorKind` const and a case in `handleSelectorChoice()`. Branch selection stays separate (has fuzzy filtering). Confirmation: `confirmAction` + `confirmTaskID` for y/n prompts.

### Styling
- Pre-computed style maps in `styles.go`: `stateStyles`, `priorityStyles`
- Use `lipgloss.AdaptiveColor` for light/dark terminal support
- Status icons: `●` running, `○` pending, `✓` completed, `✗` failed, `◷` awaiting, `▣` tmux
- Helpers: `stateStyle()`, `priorityStyle()`, `priorityBadge()`, `statusIconFor()`

## Non-Worktree Mode

The `promptView` includes a worktree toggle (`alt+W`):
- When **on** (default): task runs in an isolated git worktree with its own branch
- When **off**: task runs directly in the project root directory; branch input is hidden
- The toggle state persists per-project (stored in DB via `default_worktree`) and within a TUI session (survives `Reset()`)
- When worktree is toggled off while branch field is focused, focus auto-switches to description

### Prompt Keybinding Scheme

Three-layer key dispatch in `update_prompt.go`: `handlePromptKey` (shared) → `handleTaskPaneKey` / `handleWorkflowPaneKey` (pane-specific).

| Category | Keys | Notes |
|----------|------|-------|
| **Section focus** | `alt+t` title, `alt+d`/`alt+enter` description, `alt+g` git, `alt+w` workflow | Navigate directly to a section |
| **Pane cycling** | `alt+]`/`alt+[` (or `alt+tab`/`alt+shift+tab`) | Cycles main→git→workflow; `CyclePane(forward bool)` on promptView |
| **Input cycling** | `tab`/`shift+tab` | Cycles through individual inputs within current pane context |
| **Toggles** | `alt+W` worktree, `alt+M` branch mode | Alt+Shift = toggle action (heavier modifier) |

## Verifying Layout & Rendering

See CLAUDE.md for the general principle. Below are lipgloss/BubbleTea-specific traps:

### Common rendering traps

- **String prefix on multi-line output**: `b.WriteString(" " + multiLineString)` only prefixes the FIRST line. Use a helper like `indentBlock()` that prefixes every line.
- **`textinput.View()` width**: Renders at `Width + len(Prompt) + 1` (cursor char), NOT just `Width`. Always subtract `lipgloss.Width(input.Prompt) + 1` when sizing inputs to fit inside a frame.
- **`lipgloss.JoinHorizontal()` width inflation**: Pads ALL lines of the left block to the width of its WIDEST line. If even one content line overflows, every border line gets trailing padding, visually breaking the frame. Either ensure uniform widths before joining or use a manual line-by-line join (see `joinFramesHorizontal()`).
- **`SetSize()` timing**: Must be called AFTER populating dynamic content (e.g., workflow list) that affects layout calculations, not just on `WindowSizeMsg`.
- **lipgloss v1.x has no border labels**: `Border(lipgloss.RoundedBorder())` cannot embed a label in the top border. Use manual frame construction (`renderFramedSection()`).

### Verification checklist for layout changes

1. Render the component with realistic data (multiple workflows, long branch names, styled labels)
2. Assert every line in framed/boxed sections has exactly the expected `lipgloss.Width()`
3. Test at multiple terminal widths (narrow <60, normal ~120, wide ~200)
4. Test with both focused and unfocused states (cursor presence affects width)
5. Run `mise run test` (covers `./internal/tui/...` via `go test ./...`) — existing tests check structural properties. Add a new test file alongside the change rather than relying on the existing suite to catch new layouts.

## Pitfalls

- List view uses custom `renderTask()`, NOT `table.Model`'s built-in rendering
- Detail view: check `contentDirty` before re-wrapping content (performance)
- Handle `tea.WindowSizeMsg` in every view to recalculate dimensions
- `promptView` auto-detects image paths from textarea — preserve this behavior

## Adjacent Packages

- **daemon** via `client.Client`: task CRUD, subscriptions, log streaming
- **config**: `ListWorkflowNames()`, `ListAllWorkflowNames()`, `GetWorkflow()`
- **tmux**: `ListSessions()`, `AttachCommand()`, `SwitchClientCommand()`
- **workflow**: `ProjectLogsDir()`
