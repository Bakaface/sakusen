//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fanoutNodesWorkflowYAML(stubPath string) string {
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
  - name: parent
    steps:
      - name: setup
        prompt: "Set up task #{{task.id}}"
      - name: build
        fanout:
          merge: parent
          nodes:
            - name: a
              workflow: node-a
              input: "do a"
            - name: b
              workflow: node-b
              input: "do b"
            - name: c
              workflow: node-c
              needs: [a, b]
              input: "Join: {{steps.a.context}} | {{steps.b.context}}"
      - name: after
        prompt: "Continue after the nodes:\n{{steps.build.context}}"
  - name: node-a
    steps:
      - name: a-work
        prompt: "Node a: {{task.input}}"
        summarization_strategy: last_message
  - name: node-b
    steps:
      - name: b-work
        prompt: "Node b: {{task.input}}"
        summarization_strategy: last_message
  - name: node-c
    steps:
      - name: c-work
        prompt: "Node c: {{task.input}}"
        summarization_strategy: last_message
`, stubPath)
}

// TestFanoutNodesDiamond drives a `nodes:` fanout shaped as a diamond: a and b
// run first, c needs both. c must not spawn until a and b completed, must
// start from the parent's branch carrying both merges, and must see their
// contexts in its input; main stays untouched until the parent itself merges.
func TestFanoutNodesDiamond(t *testing.T) {
	e := setupE2E(t, "fanout_nodes")
	// As `sakusen init` does: the parent's own headless output capture must
	// not make its worktree a dirty merge target.
	gitignore := filepath.Join(e.ProjectDir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte(".sakusen/\n.sakusen-output.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.mustRun(e.ProjectDir, "git", "add", ".gitignore")
	e.mustRun(e.ProjectDir, "git", "commit", "-m", "ignore sakusen output log")
	e.WriteSakusenYAML(fanoutNodesWorkflowYAML(e.StubPath))
	mainSetupCommits := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-list", "--count", "main"))

	e.MustSakusen("create", "--title", "nodes parent", "-w", "parent", "nodes parent work")
	e.WaitStatus(1, "awaiting-children", 15*time.Second)

	childID := func(node string) int64 {
		return e.DBQueryInt(`SELECT COALESCE(MAX(id), 0) FROM tasks WHERE fanout_parent_id = 1 AND fanout_item = ?`, node)
	}
	completed := func(id int64) bool {
		return id != 0 && e.TaskStatus(id) == "completed"
	}
	var aID, bID int64
	e.Eventually(15*time.Second, "nodes a and b spawned", func() bool {
		aID, bID = childID("a"), childID("b")
		return aID != 0 && bID != 0
	})
	// c is read before a and b: a c row seen while either is still running
	// was spawned too early.
	e.Eventually(45*time.Second, "nodes a and b completed", func() bool {
		cSpawned := childID("c") != 0
		bothDone := completed(aID) && completed(bID)
		if cSpawned && !bothDone {
			t.Fatalf("node c spawned before a (%s) and b (%s) completed", e.TaskStatus(aID), e.TaskStatus(bID))
		}
		return bothDone
	})
	var cID int64
	e.Eventually(15*time.Second, "node c spawned", func() bool {
		cID = childID("c")
		return cID != 0
	})
	e.WaitStatus(cID, "completed", 45*time.Second)
	e.WaitStatus(1, "completed", 45*time.Second)

	last := map[string]int{}
	for i, c := range e.StubCalls("step") {
		last[c.Step] = i
	}
	for _, s := range []string{"a-work", "b-work", "c-work"} {
		if _, ok := last[s]; !ok {
			t.Fatalf("stub log has no %s spawn", s)
		}
	}
	if last["c-work"] < last["a-work"] || last["c-work"] < last["b-work"] {
		t.Errorf("c-work spawned before a-work or b-work in the stub log")
	}

	files, err := os.ReadFile(filepath.Join(e.PromptDir, "c-files.txt"))
	if err != nil {
		t.Fatalf("node c did not record its worktree: %v", err)
	}
	for _, f := range []string{"node-a.txt", "node-b.txt"} {
		if !strings.Contains(string(files), f) {
			t.Errorf("node c's worktree lacks %s; files:\n%s", f, files)
		}
	}
	cLog, err := os.ReadFile(filepath.Join(e.PromptDir, "c-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// A node's merge commit subject is built from its title, the node name.
	for _, want := range []string{"feat: a", "feat: b"} {
		if !strings.Contains(string(cLog), want) {
			t.Errorf("node c's history lacks %q; log:\n%s", want, cLog)
		}
	}

	cPrompt := e.CapturedPrompt(cID, "c-work")
	for _, want := range []string{"A-CONTEXT-MARKER", "B-CONTEXT-MARKER"} {
		if !strings.Contains(cPrompt, want) {
			t.Errorf("node c's prompt lacks %q:\n%s", want, cPrompt)
		}
	}

	for _, name := range []string{"c-main-count.txt", "after-main-count.txt"} {
		count, err := os.ReadFile(filepath.Join(e.PromptDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(count)); got != mainSetupCommits {
			t.Errorf("%s: main had %s commits, want %s (nodes must not reach main)", name, got, mainSetupCommits)
		}
	}

	prompt := e.CapturedPrompt(1, "after")
	for _, want := range []string{
		"fanout build: 3/3 completed",
		fmt.Sprintf("- a #%d completed", aID),
		fmt.Sprintf("- b #%d completed", bID),
		fmt.Sprintf("- c #%d completed", cID),
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("after prompt lacks %q:\n%s", want, prompt)
		}
	}

	// The parent's merge carries every node plus its own commit to main.
	for _, f := range []string{"node-a.txt", "node-b.txt", "node-c.txt", "parent-after.txt"} {
		if strings.TrimSpace(gitInDir(e.ProjectDir, "ls-tree", "--name-only", "main", f)) != f {
			t.Errorf("main lacks %s after the parent merged", f)
		}
	}
	e.AssertMergedFor(1)
}
