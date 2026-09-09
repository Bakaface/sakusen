# Template Variables Reference

## TemplateContext

```go
type TemplateContext struct {
    Task      TaskVars
    Steps     map[string]string   // step_name -> step context (from DB task_steps table)
    Git       GitVars
    Loop      LoopVars
    Track     TrackVars           // zero value (trackless task) resolves every {{track.*}} var to ""
    Branch    BranchVars          // set ONLY while resolving a parallel branch's prompt
    PromptDirs []string           // ordered {{prompt.<name>}} search roots (config.PromptDirs)
}

type TaskVars struct {
    ID          int64
    Title, Description, Slug, Branch string
    Images []string  // worktree-relative paths
}

type GitVars struct {
    BaseBranch, TargetBranch, RepoRoot string
}

type LoopVars struct {
    Iteration, MaxIterations int
}

type TrackVars struct {
    ID         int64
    Name       string
    Context    string // full ancestor chain, root-first, "## Track: <name>" headers (FormatTrackChain)
    OwnContext string // leaf track's own context only
}

type BranchVars struct {
    Name  string // parallel branch name
    Agent string // resolved agent slug (not the authored ref)
}
```

## Supported Placeholders

| Placeholder | Source |
|-------------|--------|
| `{{task.id}}` | Task ID |
| `{{task.title}}` | Task title |
| `{{task.input}}` | Task input (user-supplied) |
| `{{task.slug}}` | URL-safe slug from title |
| `{{task.branch}}` | Resolved branch name |
| `{{task.images}}` | Newline-joined image paths |
| `{{git.base_branch}}` | Base branch (e.g., main) |
| `{{git.target_branch}}` | Effective target branch (per-task override or base branch) |
| `{{git.repo_root}}` | Repository root path |
| `{{loop.iteration}}` | Current loop iteration |
| `{{loop.max_iterations}}` | Max iterations configured |
| `{{track.id}}` | Attached track's ID ("" for trackless tasks — never "0") |
| `{{track.name}}` | Attached track's name |
| `{{track.context}}` | Root-first ancestor-concatenated track context (re-read live at every step launch) |
| `{{track.own_context}}` | Leaf track's own context only |
| `{{conflict.files}}` | Conflicted files as one `` - `path` `` line each — merge-conflict resolver prompts only; "" everywhere else |
| `{{branch.name}}` | Parallel branch name — "" outside a branch prompt |
| `{{branch.agent}}` | Resolved agent slug of the running branch — "" outside a branch prompt |
| `{{steps.step_name.context}}` | Step context from DB (captured from the agent's result text or a summarize_chat pass) |
| `{{steps.group_name.context}}` | A parallel group's AGGREGATE — see below |
| `{{artifacts.step_name}}` | Backward compat alias for `{{steps.step_name.context}}` |
| `{{prompt.name}}` | Contents of a shared prompt file — see below |

Pattern: regex `\{\{([a-zA-Z0-9_.-]+)\}\}` — unknown keys pass through unchanged. The class
includes `-`, so kebab-case step and branch names (`{{steps.final-planning.context}}`) resolve
like any other.

## Parallel Group Aggregate — `{{steps.<group>.context}}`

A `parallel:` group publishes one step context assembled by `FormatParallelAggregate`: every
branch, in config order, under a `## <branch> (<resolved-agent>)` header, sections separated by a
blank line. The format is FIXED and there is no template knob for it:

```
## review-opus (claude:opus)

<review-opus's step context verbatim>

## review-codex (codex) (failed: timed out after 30m)

## review-none (claude) (no context)
```

A failed branch renders as a header with `(failed: <reason>)` and NO body; a branch that captured
nothing (strategy `none`, or an empty result) renders `(no context)`. Both are deliberately loud:
a synthesis prompt must never mistake an absent review for agreement.

**Want a different layout?** Each branch is also addressable on its own as
`{{steps.<branch>.context}}`, so a prompt author can lay the sections out however they like. That
is the escape hatch; a configurable aggregate template would need iteration inside
`ResolveTemplate`, which is a flat single-pass substitution. `{{children.summary}}` and
`{{track.context}}` are the existing fixed-format precedents.

## Prompt Includes — `{{prompt.<name>}}`

A `{{prompt.<name>}}` placeholder is replaced by the contents of `<name>.md`, letting several
workflows share one passage of prompt text (craft guidance, review rules, commit hygiene) without
a new schema concept. It works in any string that goes through `ResolveTemplate`: step `prompt`,
step `summarization_prompt`, workflow `summarizer_prompt`, `merge_conflicts.prompt`.

`<name>` allows letters, digits, dashes and underscores (kebab-case by convention). Lookup order
(first hit wins), mirroring the workflow-file tiers:

1. `<project>/.sakusen/prompts/<name>.md`
2. `~/.sakusen/prompts/<name>.md`

Semantics:

- **Expanded first, resolved in the same pass** — `ResolveTemplate` substitutes the file contents,
  then resolves every other placeholder over the combined text, so an included passage may itself
  use `{{task.id}}`, `{{steps.<name>.context}}`, etc.
- **Depth 1** — an included file containing `{{prompt.*}}` is an error, not a recursion.
- **One trailing newline is stripped** so inline placement (`Foo {{prompt.x}} bar`) adds no blank
  line; other leading/trailing whitespace is preserved.
- **Never left unresolved** — unlike an unknown placeholder (which passes through verbatim), a
  missing file, a malformed name, or a nested include is an error. The config loader and
  `sakusen validate` catch it at load time, naming the workflow, step, field and searched paths;
  at runtime `ResolveTemplate` returns the error and the caller fails the step.
- The **files are re-read at every step launch** (the loader validates them but does not bake them
  into the config), so editing a shared passage takes effect without a daemon restart.
