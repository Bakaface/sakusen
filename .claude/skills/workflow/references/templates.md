# Template Variables Reference

## TemplateContext

```go
type TemplateContext struct {
    Task      TaskVars
    Steps     map[string]string   // step_name -> step context (from DB task_steps table)
    Git       GitVars
    Loop      LoopVars
    Track     TrackVars           // zero value (trackless task) resolves every {{track.*}} var to ""
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
| `{{steps.step_name.context}}` | Step context from DB (captured from the agent's result text or a summarize_chat pass) |
| `{{artifacts.step_name}}` | Backward compat alias for `{{steps.step_name.context}}` |
| `{{prompt.name}}` | Contents of a shared prompt file — see below |

Pattern: regex `\{\{([a-zA-Z0-9_.]+)\}\}` — unknown keys pass through unchanged.

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
