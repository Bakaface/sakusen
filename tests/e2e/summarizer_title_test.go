//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// summarizerTitleYAML wires the stub in twice: as the (only) step agent and
// as the top-level summarizer: command. The stub's summarizer branch routes
// purpose=title to testdata/summarizer_title/title.txt.
func summarizerTitleYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
summarizer:
  command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath, stubPath)
}

// noSummarizerYAML is the same workflow without a summarizer: command, to
// exercise the degradation path for tasks created without --title.
func noSummarizerYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath)
}

// TestSummarizerGeneratesTitle verifies the top-level summarizer: command
// end-to-end: a task created WITHOUT --title triggers async AI title
// generation (daemon shells out to the summarizer with SAKUSEN_PURPOSE=title
// and the prompt on stdin), and the task's title becomes the stub's
// title.txt fixture content. Title generation is asynchronous
// (refineTaskTitle runs in a goroutine), so the title is polled.
func TestSummarizerGeneratesTitle(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(summarizerTitleYAML(e.StubPath))

	// No --title: the daemon must call the summarizer to produce one.
	e.MustSakusen("create", "add a login form with client-side validation to the settings page")

	e.Eventually(10*time.Second, "AI-generated title", func() bool {
		return e.TaskField(1, "title") == "AI Generated Title"
	})

	// The summarizer command was actually invoked with purpose=title.
	if n := len(e.StubCalls("title")); n != 1 {
		t.Errorf("stub title calls: got %d, want 1", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
	e.AssertMergedFor(1)
}

// summarizerAgentYAML wires the stub in twice: as the step agent and as the
// summarizer's `agent:` — the registry-agent form, which hands the prompt over
// in $SAKUSEN_PROMPT_FILE instead of on stdin.
func summarizerAgentYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
summarizer:
  agent: stub
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath)
}

// TestSummarizerAgentGeneratesTitle is the summarizer.agent counterpart of
// TestSummarizerGeneratesTitle: the same AI title lands, but the call runs
// through the file contract — the stub sees SAKUSEN_PROMPT_FILE and
// SAKUSEN_RESULT_FILE alongside SAKUSEN_PURPOSE=title. The stub prints its
// answer on stdout without writing the result file, so this also exercises the
// stdout-tail fallback.
func TestSummarizerAgentGeneratesTitle(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(summarizerAgentYAML(e.StubPath))

	e.MustSakusen("create", "add a login form with client-side validation to the settings page")

	e.Eventually(10*time.Second, "AI-generated title", func() bool {
		return e.TaskField(1, "title") == "AI Generated Title"
	})

	calls := e.StubCalls("title")
	if len(calls) != 1 {
		t.Fatalf("stub title calls: got %d, want 1", len(calls))
	}
	if got := calls[0].Env["SAKUSEN_PROMPT_FILE"]; got == "" {
		t.Errorf("SAKUSEN_PROMPT_FILE not exported to the summarizer agent (env: %v)", calls[0].Env)
	}
	if got := calls[0].Env["SAKUSEN_RESULT_FILE"]; got == "" {
		t.Errorf("SAKUSEN_RESULT_FILE not exported to the summarizer agent (env: %v)", calls[0].Env)
	}
	if got := calls[0].Env["SAKUSEN_PURPOSE"]; got != "title" {
		t.Errorf("SAKUSEN_PURPOSE = %q, want %q", got, "title")
	}

	e.WaitStatus(1, "completed", 15*time.Second)
	e.AssertMergedFor(1)
}

// TestNoSummarizerFallsBackToTruncatedInput verifies the degradation path:
// without a summarizer: command, a task created without --title gets a
// sanitized, truncated slice of its input's first line as the title (see
// refineTaskTitle/truncateTitleInput in internal/daemon/handlers_task.go)
// instead of blocking creation.
func TestNoSummarizerFallsBackToTruncatedInput(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(noSummarizerYAML(e.StubPath))

	// First line is 9 space-separated 9-char tokens = 89 chars (> the 80-char
	// clip). truncateTitleInput keeps the first line, clips to 80 bytes
	// (which lands exactly on the space after token 8), and trims — so the
	// expected title is the first 8 tokens.
	tokens := []string{
		"aaaaaaaaa", "bbbbbbbbb", "ccccccccc", "ddddddddd", "eeeeeeeee",
		"fffffffff", "ggggggggg", "hhhhhhhhh", "iiiiiiiii",
	}
	input := strings.Join(tokens, " ") + "\nmore detail on a second line"
	want := strings.Join(tokens[:8], " ")

	e.MustSakusen("create", input)

	e.Eventually(10*time.Second, "truncated-input fallback title", func() bool {
		return e.TaskField(1, "title") == want
	})

	// No summarizer configured → the stub must never be called for a title.
	if n := len(e.StubCalls("title")); n != 0 {
		t.Errorf("stub title calls: got %d, want 0 (no summarizer configured)", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
}

// TestSummarizerGeneratesSlug verifies AI slug generation end-to-end: a task
// created without --slug triggers a separate summarizer call
// (SAKUSEN_PURPOSE=slug), and the answer becomes the task's slug and feeds the
// branch template. A --title task still gets the slug call — only the title
// call is skipped.
func TestSummarizerGeneratesSlug(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(summarizerTitleYAML(e.StubPath))

	e.MustSakusen("create", "--title", "Manual Title", "add a login form with client-side validation")

	e.Eventually(10*time.Second, "AI-generated slug", func() bool {
		return e.TaskField(1, "slug") == "ai-generated-slug"
	})

	if got := e.TaskField(1, "title"); got != "Manual Title" {
		t.Errorf("title = %q, want the manual title", got)
	}
	if got := e.TaskField(1, "branch"); got != "sakusen/1-ai-generated-slug" {
		t.Errorf("branch = %q, want the slug-derived branch", got)
	}
	// The manual title skips the title call; the slug call still happens.
	if n := len(e.StubCalls("slug")); n != 1 {
		t.Errorf("stub slug calls: got %d, want 1", n)
	}
	if n := len(e.StubCalls("title")); n != 0 {
		t.Errorf("stub title calls: got %d, want 0 (--title was passed)", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
}

// slugCommandYAML wires the stub in as step agent and summarizer, but points
// slug generation at a separate command that answers with a slug well past
// task.MaxSlugLength.
func slugCommandYAML(stubPath, slug string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
summarizer:
  command: "%s"
  slug_command: "cat > /dev/null; printf '%%s\\n' '%s'"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath, stubPath, slug)
}

// TestSlugCommandGeneratesUntruncatedSlug verifies both halves of the slug
// configuration: slug_command replaces the summarizer command for the slug call
// only, and its answer becomes the slug verbatim — no MaxSlugLength cap, no
// word-boundary trimming — feeding the branch template as-is.
func TestSlugCommandGeneratesUntruncatedSlug(t *testing.T) {
	const slug = "put-guardrails-on-ai-task-slug-generation"

	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(slugCommandYAML(e.StubPath, slug))

	e.MustSakusen("create", "--title", "Manual Title", "add a login form with client-side validation")

	e.Eventually(10*time.Second, "untruncated slug from slug_command", func() bool {
		return e.TaskField(1, "slug") == slug
	})

	if got := e.TaskField(1, "branch"); got != "sakusen/1-"+slug {
		t.Errorf("branch = %q, want the full slug in the branch", got)
	}
	// slug_command handled the slug call, so the stub never saw one.
	if n := len(e.StubCalls("slug")); n != 0 {
		t.Errorf("stub slug calls: got %d, want 0 (slug_command is configured)", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
}

// TestExplicitSlugSkipsSummarizer verifies that --slug wins: it is normalized
// (lowercased, kebab-cased, length-capped) and no slug call is made.
func TestExplicitSlugSkipsSummarizer(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSakusenYAML(summarizerTitleYAML(e.StubPath))

	e.MustSakusen("create", "--title", "Manual Title", "--slug", "My Custom Slug", "add a login form")

	e.Eventually(10*time.Second, "explicit slug", func() bool {
		return e.TaskField(1, "slug") == "my-custom-slug"
	})
	if n := len(e.StubCalls("slug")); n != 0 {
		t.Errorf("stub slug calls: got %d, want 0 (--slug was passed)", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
}
