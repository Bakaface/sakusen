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
// generation (daemon shells out to the summarizer with SORTIE_PURPOSE=title
// and the prompt on stdin), and the task's title becomes the stub's
// title.txt fixture content. Title generation is asynchronous
// (refineTaskTitle runs in a goroutine), so the title is polled.
func TestSummarizerGeneratesTitle(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSortieYAML(summarizerTitleYAML(e.StubPath))

	// No --title: the daemon must call the summarizer to produce one.
	e.MustSortie("create", "add a login form with client-side validation to the settings page")

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

// TestNoSummarizerFallsBackToTruncatedInput verifies the degradation path:
// without a summarizer: command, a task created without --title gets a
// sanitized, truncated slice of its input's first line as the title (see
// refineTaskTitle/truncateTitleInput in internal/daemon/handlers_task.go)
// instead of blocking creation.
func TestNoSummarizerFallsBackToTruncatedInput(t *testing.T) {
	e := setupE2E(t, "summarizer_title")
	e.WriteSortieYAML(noSummarizerYAML(e.StubPath))

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

	e.MustSortie("create", input)

	e.Eventually(10*time.Second, "truncated-input fallback title", func() bool {
		return e.TaskField(1, "title") == want
	})

	// No summarizer configured → the stub must never be called for a title.
	if n := len(e.StubCalls("title")); n != 0 {
		t.Errorf("stub title calls: got %d, want 0 (no summarizer configured)", n)
	}

	e.WaitStatus(1, "completed", 15*time.Second)
}
