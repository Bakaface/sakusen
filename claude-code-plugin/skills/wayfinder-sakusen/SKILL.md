---
name: wayfinder-sakusen
description: >
  Issue-tracker doc that lets Matt Pocock's `wayfinder` planning skill use
  Sakusen as its tracker instead of GitHub issues: the map is a Sakusen track,
  decision tickets are child tasks spawned in dependency waves, and the daemon
  is the frontier query. Defines all six wayfinding primitives in Sakusen terms
  and ships the reference workflows that drive them. Use when (1) running
  `/wayfinder` on a repo whose tracker is Sakusen, (2) installing or debugging
  the wayfinder-on-sakusen workflows, (3) charting a map, spawning a wave, or
  folding a resolved ticket back into a map, (4) the user mentions wayfinder,
  a decision map, decision tickets, waves, or asks how to plan an effort too
  big for one session with Sakusen tasks. Do NOT use for ordinary `.sakusen.yml`
  authoring — that is `sakusen-configurer`.
---

# Wayfinder on Sakusen

[`wayfinder`](https://github.com/mattpocock/skills/blob/main/skills/engineering/wayfinder/SKILL.md)
plans a chunk of work too big for one agent session as a **map** of **decision tickets**, and
works them one at a time until the way to the destination is clear. It plans; it does not
implement.

Wayfinder hardcodes no tracker. It delegates every tracker operation to a tracker doc that
defines six primitives — Map, Child ticket, Blocking, Frontier query, Claim, Resolve. **This
skill is that doc, for Sakusen.** Wayfinder itself is unmodified; install it separately.

> This skill does not reimplement wayfinding. When a session is doing wayfinder work, load
> `wayfinder` too (it is marked `disable-model-invocation`, so invoke it by name with the
> Skill tool). If it is not installed, say so and stop rather than improvising a map.

## The mapping at a glance

| Wayfinder | Sakusen |
|---|---|
| Map | a **track**; the map body is the track's own context |
| Child ticket | a **child task** on that track; the ticket name is the task title |
| Ticket type label | the child's **workflow** |
| Blocking | **waves**, not `blocked_by`; edges are `Blocked by:` lines on the map |
| Frontier query | the orchestrator reads the map; the daemon schedules what it spawns |
| Claim | the daemon moving the task to `running` — there is no assignee |
| Resolve | the child task completing; its final task context is the resolution |

Because there is no issue number to refer to, a ticket **is** its name. Keep names short,
unique within a map, and stable — the orchestrator matches children back to map entries by
title.

## Wayfinding operations

### Map

The map is a Sakusen **track**, and the map body is that track's own context. Every task
attached to the track sees it in prompts as `{{track.own_context}}`.

- **Create**: `create_track` with `name` (the map's name), `description` (a stable one-liner —
  routing agents read it from `list_tracks`; it is never injected into prompts), and
  `context` seeded with the map skeleton below.
- **Read**: `get_track` with `track="<slug-or-id>"`. Use its `own_context` — that is the map.
  `rendered_context` also folds in ancestor tracks, which are not part of the map.
- **Write**: `update_track` with `task_id=<the map task's id>`, `context_mode="replace"`, and
  the **complete** rewritten map body as `context`.

Three rules on writes, all of them load-bearing:

1. **Only the parent map task ever writes the map.** Children never call `update_track`. Two
   writers clobber each other — `replace` is whole-document.
2. **Never `context_mode="append"` for the map.** The map is one document; appending
   duplicates it.
3. **`get_track` before every write.** The `{{track.own_context}}` in your prompt was rendered
   when your process started; a human may have edited the map since.

`update_track` only writes the *calling task's own* track, and only while that task has an
active step. That is why the map body lives on the map task's own track and not a parent track.

#### The map body

```markdown
# <map name>

## Destination

<what reaching the end of this map looks like: the spec, decision, or change this effort is
finding its way to. One or two lines; every session orients to it before choosing a ticket.>

## Notes

<domain; skills every session should consult; standing preferences for this effort>

## Open tickets

<!-- charted but not yet spawned. This section is the Sakusen addition: a ticket has no
     existence as a task until its wave spawns, so the charted-but-unspawned set has to
     live on the map or it is lost. -->

- **<ticket name>** (`research`) — <the question, one line>
  Blocked by: none
- **<ticket name>** (`grilling`) — <the question, one line>
  Blocked by: <ticket name>, <ticket name>

## Decisions so far

<!-- the index: one line per resolved ticket, enough to judge relevance; zoom the task id
     with `get_task` for the detail the resolution holds -->

- **<ticket name>** (#<task id>): <one-line gist of the answer>

## Not yet specified

<!-- fog: in-scope questions you cannot phrase sharply enough to ticket yet -->

## Out of scope

- <gist> — <why it is past the destination> (#<task id>, if it was ever a ticket)
```

`## Open tickets` is the only section wayfinder's own map body does not have. On GitHub an open
ticket is an open child issue; in Sakusen a ticket only becomes a task when its wave spawns, so
the pending set has nowhere else to live. Everything else is wayfinder's layout verbatim.

### Child ticket

A ticket is a **child task** on the map's track, spawned with `create_tasks_and_wait`. The
ticket name is the task title; the ticket's question is the task input; the ticket's type
selects the workflow.

```jsonc
{
  "title":    "<ticket name>",              // verbatim — skips AI title generation
  "workflow": "wayfinder-research",         // by ticket type, see below
  "input":    "## Question\n\n<the decision or investigation this ticket resolves>\n\n<any context from the map the child needs that is not already in the map body>"
}
```

Leave `track` unset: a child in the parent's project inherits the parent's track, so it reads
the map for orientation. Leave `worktree` unset: the workflow pins it.

| Ticket type | Workflow | Mode |
|---|---|---|
| `research` | `wayfinder-research` | headless, unattended |
| `grilling` | `wayfinder-hitl` | tmux, human in the loop |
| `prototype` | `wayfinder-hitl` | tmux, human in the loop |
| `task` | `wayfinder-research` if the agent can do it alone, else `wayfinder-hitl` | either |

Record the type on the map next to the ticket name so a later wave picks the same workflow.

### Blocking

**Do not use `blocked_by` between tickets.** A Sakusen dependency is satisfied only by a
`completed` blocker, so a failed blocker strands its dependents forever: the dependent never
reaches a terminal status, the parent suspended on it never resumes, and nothing times it out.

Blocking is expressed on the map instead, as a `Blocked by: <ticket name>, ...` line under each
open ticket, and enforced by spawning tickets in **waves**: the orchestrator spawns only
tickets whose blockers are already resolved, waits for the whole wave, folds the results in,
and spawns the next. A failed ticket therefore costs one wave, not the map.

### Frontier query

Two halves:

- **Which tickets are takeable** is computed by the orchestrator from the map: every entry in
  `## Open tickets` whose `Blocked by:` names all appear in `## Decisions so far` (or whose
  `Blocked by:` is `none`). That set is the frontier and it is spawned as one wave.
- **Scheduling the wave** is the daemon's job. Spawned tickets sit `pending` and the scheduler
  starts them as workers free up, honouring `max_workers`. Nothing else queries anything.

`## Open tickets` non-empty with an empty frontier means a blocking cycle, or every remaining
ticket is blocked by one that failed. Stop and report it; do not guess.

### Claim

The daemon moving a task to `running`. There is no assignee and nothing to claim by hand: a
ticket is spawned and claimed in the same act, so "open and unclaimed" is not a state a
Sakusen ticket can be in. Concurrent sessions cannot collide over a ticket.

### Resolve

A ticket resolves by its child task **completing**. The child's final task context is the
resolution; the parent reads it from `{{children.<id>.context}}` (and `{{children.summary}}`)
when the daemon resumes it, and folds a one-line gist into `## Decisions so far`. The full
resolution stays on the child task — `get_task` zooms it.

- `{{children.<id>.status}}` is `completed` or `failed`. A `failed` child is **not** a
  resolution: leave its ticket open on the map, re-worded if the failure showed the question
  was wrong, and spawn it fresh next wave.
- **Prefer a fresh ticket to `retry_task`.** `wait_for_tasks` silently skips any child that is
  already terminal at the instant of the call, so waiting on a retried child can record no
  suspension at all and the parent runs straight on with stale results. If you do call
  `wait_for_tasks`, read its reply: "no suspension recorded" means you are still in the same
  turn and must not act as though the children ran.
- If a resolution shows a ticket sits past the destination, move it to `## Out of scope` with
  one line of why. It never enters `## Decisions so far` — that section records the route
  actually walked.

## How a map runs

```
sakusen tracks create        →  map created, body seeded
task on wayfinder-map
  step 1  charting  (tmux, human)     grill the user, write the map body
  step 2  resolving (headless)        wave 1: spawn frontier  → SUSPEND
          resolving (headless, fresh) fold results, rewrite map,
                                      wave 2: spawn frontier  → SUSPEND
          resolving (headless, fresh) ... until Open tickets is empty
                                      → handoff: /to-spec, /to-tickets, /implement
```

## Hard rules

These are engine behaviours, not style preferences. Each one has bitten this design.

1. **The orchestrator forgets everything between waves.** When a step calls
   `create_tasks_and_wait`, the engine suspends the task at the *same* step index and
   explicitly skips context capture, the step completion record, and chat recording. Headless
   steps record no transcript either. Every resume is a fresh process that sees only template
   variables. **The map is the only memory.** Re-read it and rewrite it every wave; never rely
   on remembering anything, and never leave work queued after a call that suspends.
2. **A track cannot be attached after task creation.** Create the track first, then create the
   map task with `--track <slug>`. A trackless task resolves every `{{track.*}}` variable to
   empty and cannot write a track at all, so it cannot host a map.
3. **Only the parent writes the map**, with `get_track` then `update_track` /
   `context_mode: replace`. See [Map](#map).
4. **Waves, never `blocked_by`.** See [Blocking](#blocking).
5. **A retried child can silently skip the wait.** See [Resolve](#resolve).
6. **The children's workflows pin `worktree: false` on purpose.** A task whose worktree has no
   meaningful changes is fast-tracked straight to `completed`, skipping finalization — which
   means skipping the summarizer that produces the task context. A decision ticket usually
   changes no files, so a worktree-backed ticket would hand the parent an **empty** resolution.
   The fast-track check is short-circuited for non-worktree tasks, so `worktree: false`
   guarantees the resolution survives. It also means a ticket never merges anything.
   If you change a child workflow to use a worktree and merge, note that the parent's worktree
   is cut from the base branch and never rebased mid-task, so any step that must observe merged
   child work has to `git merge {{git.target_branch}}` into its own worktree first.
7. **Human-in-the-loop tickets work, and they run in the repo root.** A `human: true` step is
   never auto-advanced, a suspended parent is kept alive, and nothing times out the wait. But
   because HITL tickets are not worktree-isolated, **spawn at most one per wave** — two live
   tmux sessions editing the same working tree will collide. Research tickets are read-only and
   parallelise freely. (Wayfinder's own "never resolve more than one ticket per session, except
   research" says the same thing.)

## Install

The workflows ship as reference copies under `workflows/` in this skill directory, because
`.sakusen/` is gitignored in most repos and these belong in the user's global pool.

1. **Install wayfinder** if it is not already installed (see
   [mattpocock/skills](https://github.com/mattpocock/skills)). This adapter is useless without it.

2. **Copy the workflows into the global pool:**

   ```bash
   cp <this-skill-dir>/workflows/wayfinder-*.yml ~/.sakusen/workflows/
   ```

   Check the agent slugs first. The shipped files use the aliases `smart-conversationist`
   (tmux) and `planner` (headless). If your `~/.sakusen.yml` does not define those, re-point
   the `agent:` fields at slugs it does define — an unknown slug is a hard load error. The only
   requirement is that `smart-conversationist`'s slot resolves to a **tmux**-mode agent and
   `planner`'s to a **headless** one.

3. **Reference them from the project's `.sakusen.yml`** so they show up in the TUI's new-task
   menu (they are reachable by exact name without this, as hidden workflows):

   ```yaml
   workflows:
     - wayfinder-map
     - wayfinder-research
     - wayfinder-hitl
   ```

4. **Verify the config still loads:**

   ```bash
   sakusen validate            # or: sakusen validate path/to/.sakusen.yml
   ```

## Bootstrap a map

Two steps, in this order. The order is not optional — see hard rule 2.

```bash
# 1. The map. Its context is the map body. Leave it empty — the charting step
#    writes the whole thing. --context seeds it if you want a head start.
sakusen tracks create "Offline sync" \
  --description "Wayfinder map: finding the way to an offline-sync design"

# 2. The map task, attached to that track.
sakusen create -w wayfinder-map --track offline-sync \
  "Chart the way to an offline-sync design for the mobile client."
```

Then attach to the `charting` tmux session and grill. When you approve that step, the
`resolving` orchestrator takes over and runs waves until the map is clear.

> **Future simplification.** A separate change allowing a track to be attached to an existing
> task would collapse this two-step bootstrap into one: create the task, then create and attach
> the map. Today `update_task` has no track field, so the track must exist first.
