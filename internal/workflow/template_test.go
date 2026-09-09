package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// mustResolveTemplate resolves tmpl and fails the test on a prompt-include
// error, keeping assertions on the resolved string uncluttered.
func mustResolveTemplate(t *testing.T, tmpl string, ctx *TemplateContext) string {
	t.Helper()
	got, err := ResolveTemplate(tmpl, ctx)
	if err != nil {
		t.Fatalf("ResolveTemplate(%q): %v", tmpl, err)
	}
	return got
}

func makeLookup(tasks map[int64]*task.Task) func(int64) (*task.Task, error) {
	return func(id int64) (*task.Task, error) {
		t, ok := tasks[id]
		if !ok {
			return nil, errors.New("not found")
		}
		return t, nil
	}
}

func TestResolveTemplate_TaskRefFields(t *testing.T) {
	tasks := map[int64]*task.Task{
		42: {
			ID:      42,
			Title:   "Refactor parser",
			Branch:  "sakusen/42-refactor",
			Input:   "Pull tokenizer out of parser.go",
			Context: "Done — landed in commit abc123.",
		},
	}

	cases := []struct {
		name string
		tmpl string
		want string
	}{
		{"title", "title={{tasks.42.title}}", "title=Refactor parser"},
		{"branch", "branch={{tasks.42.branch}}", "branch=sakusen/42-refactor"},
		{"description", "desc={{tasks.42.input}}", "desc=Pull tokenizer out of parser.go"},
		{"context", "ctx={{tasks.42.context}}", "ctx=Done — landed in commit abc123."},
	}

	ctx := &TemplateContext{TaskLookup: makeLookup(tasks)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustResolveTemplate(t, tc.tmpl, ctx)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveTemplate_TaskContextField(t *testing.T) {
	// Direct {{task.context}} on the current task — fixes prior README drift.
	ctx := &TemplateContext{
		Task: TaskVars{Context: "prior summary text"},
	}
	got := mustResolveTemplate(t, "ctx={{task.context}}", ctx)
	if got != "ctx=prior summary text" {
		t.Errorf("got %q", got)
	}
}

func TestResolveTemplate_MissingTaskID(t *testing.T) {
	ctx := &TemplateContext{TaskLookup: makeLookup(nil)}
	got := mustResolveTemplate(t, "x={{tasks.99.title}}", ctx)
	if got != "x=" {
		t.Errorf("missing id should resolve empty, got %q", got)
	}
}

func TestResolveTemplate_NilLookup(t *testing.T) {
	ctx := &TemplateContext{}
	got := mustResolveTemplate(t, "x={{tasks.5.title}}", ctx)
	if got != "x=" {
		t.Errorf("nil lookup should resolve empty, got %q", got)
	}
}

func TestResolveTemplate_MalformedRef(t *testing.T) {
	ctx := &TemplateContext{TaskLookup: makeLookup(nil)}
	cases := []string{
		"{{tasks.abc.title}}",
		"{{tasks.42}}",
		"{{tasks.}}",
	}
	for _, c := range cases {
		got := mustResolveTemplate(t, c, ctx)
		if got != c {
			t.Errorf("malformed ref %q should stay verbatim, got %q", c, got)
		}
	}
}

func TestResolveTemplate_UnsupportedField(t *testing.T) {
	tasks := map[int64]*task.Task{1: {ID: 1, Title: "t"}}
	ctx := &TemplateContext{TaskLookup: makeLookup(tasks)}
	// Unsupported field resolves to "" (validator is the user-facing gate).
	got := mustResolveTemplate(t, "x={{tasks.1.slug}}", ctx)
	if got != "x=" {
		t.Errorf("unsupported field should resolve empty, got %q", got)
	}
}

func TestResolveTemplate_SelfReference(t *testing.T) {
	// A task can reference itself; it resolves to its own current field value.
	self := &task.Task{ID: 7, Title: "Self-task", Input: "self desc"}
	ctx := &TemplateContext{
		Task:       TaskVars{ID: 7, Title: "Self-task", Input: "self desc"},
		TaskLookup: makeLookup(map[int64]*task.Task{7: self}),
	}
	got := mustResolveTemplate(t, "{{tasks.7.title}}", ctx)
	if got != "Self-task" {
		t.Errorf("self ref got %q", got)
	}
}

func TestResolveTaskRefs_EndToEnd(t *testing.T) {
	// Simulates the engine's pre-expansion: {{task.input}} contains
	// {{tasks.42.title}}, and after pre-resolution the step prompt
	// "{{task.input}}" expands to the full text.
	tasks := map[int64]*task.Task{
		42: {ID: 42, Title: "Underlying refactor"},
	}
	rawDescription := "Build on top of: {{tasks.42.title}}"
	resolvedInput := ResolveTaskRefs(rawDescription, makeLookup(tasks))
	if resolvedInput != "Build on top of: Underlying refactor" {
		t.Fatalf("pre-resolution failed: %q", resolvedInput)
	}

	ctx := &TemplateContext{
		Task: TaskVars{Input: resolvedInput},
		// TaskLookup intentionally left nil here — pre-resolution should make
		// the final ResolveTemplate pass independent of the lookup table.
	}
	final := mustResolveTemplate(t, "{{task.input}}", ctx)
	if final != "Build on top of: Underlying refactor" {
		t.Errorf("final template got %q", final)
	}
}

func TestResolveTaskRefs_NoOpWhenNoMatches(t *testing.T) {
	got := ResolveTaskRefs("plain text", makeLookup(nil))
	if got != "plain text" {
		t.Errorf("expected pass-through, got %q", got)
	}
}

func TestExtractTaskRefs(t *testing.T) {
	in := "see {{tasks.42.title}} and {{tasks.7.branch}}; also {{task.id}} and {{tasks.abc.title}} and {{tasks.5.foo.bar}}"
	refs := ExtractTaskRefs(in)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %+v", len(refs), refs)
	}
	if refs[0].ID != 42 || refs[0].Field != "title" {
		t.Errorf("ref 0: %+v", refs[0])
	}
	if refs[1].ID != 7 || refs[1].Field != "branch" {
		t.Errorf("ref 1: %+v", refs[1])
	}
}

func TestResolveTemplate_ChildrenFields(t *testing.T) {
	ctx := &TemplateContext{
		Children: ChildrenVars{ByID: map[int64]ChildVars{
			5: {ID: 5, Title: "child five", Status: "completed", Context: "five did the thing"},
			6: {ID: 6, Title: "child six", Status: "failed", Context: "six exploded"},
		}},
	}

	cases := []struct {
		tmpl, want string
	}{
		{"{{children.5.id}}", "5"},
		{"{{children.5.title}}", "child five"},
		{"{{children.5.status}}", "completed"},
		{"{{children.5.context}}", "five did the thing"},
		{"{{children.6.status}}", "failed"},
		// Unknown ID → empty
		{"x={{children.99.title}}", "x="},
	}
	for _, tc := range cases {
		if got := mustResolveTemplate(t, tc.tmpl, ctx); got != tc.want {
			t.Errorf("ResolveTemplate(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestResolveTemplate_ChildrenSummary_DeterministicOrder(t *testing.T) {
	ctx := &TemplateContext{
		Children: ChildrenVars{ByID: map[int64]ChildVars{
			11: {ID: 11, Title: "eleven", Status: "completed", Context: "alpha"},
			3:  {ID: 3, Title: "three", Status: "failed", Context: "beta"},
		}},
	}
	got := mustResolveTemplate(t, "{{children.summary}}", ctx)
	// IDs sorted ascending: 3 first, 11 second
	if !strings.Contains(got, "## Child task #3") {
		t.Errorf("summary missing #3:\n%s", got)
	}
	if !strings.Contains(got, "## Child task #11") {
		t.Errorf("summary missing #11:\n%s", got)
	}
	if i3, i11 := strings.Index(got, "#3"), strings.Index(got, "#11"); !(i3 < i11) {
		t.Errorf("expected #3 to precede #11 in summary:\n%s", got)
	}
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Errorf("summary missing child contexts: %s", got)
	}
}

func TestResolveTemplate_ChildrenSummary_EmptyMap(t *testing.T) {
	ctx := &TemplateContext{Children: ChildrenVars{}}
	if got := mustResolveTemplate(t, "x={{children.summary}}", ctx); got != "x=" {
		t.Errorf("empty children summary should be empty, got %q", got)
	}
}

func TestResolveTemplate_ChildrenUnsupportedField(t *testing.T) {
	ctx := &TemplateContext{
		Children: ChildrenVars{ByID: map[int64]ChildVars{1: {ID: 1, Title: "x", Status: "completed"}}},
	}
	// Unsupported field resolves to "" rather than crashing.
	if got := mustResolveTemplate(t, "x={{children.1.nope}}", ctx); got != "x=" {
		t.Errorf("unsupported children field should resolve empty, got %q", got)
	}
}

func TestValidateTaskRefs(t *testing.T) {
	good := []TaskRef{
		{ID: 1, Field: "title", Raw: "{{tasks.1.title}}"},
		{ID: 2, Field: "context", Raw: "{{tasks.2.context}}"},
	}
	if err := ValidateTaskRefs(good); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	bad := []TaskRef{{ID: 1, Field: "slug", Raw: "{{tasks.1.slug}}"}}
	err := ValidateTaskRefs(bad)
	if err == nil {
		t.Fatal("expected error for unsupported field")
	}
	if !strings.Contains(err.Error(), "slug") {
		t.Errorf("error should mention the bad field name: %v", err)
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("error should list supported fields: %v", err)
	}
}

func TestResolveTemplate_Now(t *testing.T) {
	ctx := &TemplateContext{}
	out := mustResolveTemplate(t, "fired at {{now}}", ctx)
	const prefix = "fired at "
	if !strings.HasPrefix(out, prefix) {
		t.Fatalf("unexpected output: %q", out)
	}
	ts := strings.TrimPrefix(out, prefix)
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("{{now}} did not resolve to RFC3339: %q (%v)", ts, err)
	}
}

func TestResolveTemplate_NowDoesNotAffectOtherVars(t *testing.T) {
	ctx := &TemplateContext{Task: TaskVars{Title: "hello"}}
	out := mustResolveTemplate(t, "{{task.title}} {{unknown}}", ctx)
	if out != "hello {{unknown}}" {
		t.Fatalf("unexpected output: %q", out)
	}
}

// TestResolveTemplateConflictFiles verifies {{conflict.files}}: one markdown
// bullet per conflicted file, and "" in any context without conflicts (every
// step prompt).
func TestResolveTemplateConflictFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"multiple files", []string{"main.go", "internal/a.go"}, "- `main.go`\n- `internal/a.go`"},
		{"single file", []string{"main.go"}, "- `main.go`"},
		{"no conflict context", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &TemplateContext{Conflict: ConflictVars{Files: tt.files}}
			if got := mustResolveTemplate(t, "{{conflict.files}}", ctx); got != tt.want {
				t.Errorf("ResolveTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveTemplateHyphenatedStepName is a regression test for the
// placeholder character class: step names are kebab-case by convention, so a
// {{steps.<name>.context}} ref with a hyphen must resolve like any other.
// Before "-" was added to templatePattern these were left verbatim and shipped
// to the agent as literal placeholder text.
func TestResolveTemplateHyphenatedStepName(t *testing.T) {
	ctx := &TemplateContext{Steps: map[string]string{"final-planning": "THE PLAN"}}

	got, err := ResolveTemplate("plan: {{steps.final-planning.context}}", ctx)
	if err != nil {
		t.Fatalf("ResolveTemplate: %v", err)
	}
	if got != "plan: THE PLAN" {
		t.Errorf("got %q, want the hyphenated step context resolved", got)
	}

	// An unknown hyphenated name still passes through verbatim, like any other
	// unknown placeholder — only includes are hard errors.
	got, err = ResolveTemplate("x {{some-unknown-key}} y", ctx)
	if err != nil {
		t.Fatalf("ResolveTemplate: %v", err)
	}
	if got != "x {{some-unknown-key}} y" {
		t.Errorf("got %q, want the unknown placeholder left verbatim", got)
	}
}

// TestResolveTemplateBranchVars covers {{branch.name}}/{{branch.agent}}: set
// inside a parallel branch, empty everywhere else.
func TestResolveTemplateBranchVars(t *testing.T) {
	inBranch := &TemplateContext{Branch: BranchVars{Name: "review-opus", Agent: "claude:opus"}}
	got, err := ResolveTemplate("{{branch.name}}/{{branch.agent}}", inBranch)
	if err != nil {
		t.Fatalf("ResolveTemplate: %v", err)
	}
	if got != "review-opus/claude:opus" {
		t.Errorf("got %q, want the branch vars resolved", got)
	}

	got, err = ResolveTemplate("[{{branch.name}}][{{branch.agent}}]", &TemplateContext{})
	if err != nil {
		t.Fatalf("ResolveTemplate: %v", err)
	}
	if got != "[][]" {
		t.Errorf("got %q, want empty branch vars outside a branch", got)
	}
}
