package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/task"
)

func TestShouldSummarizeChat(t *testing.T) {
	bigChat := strings.Repeat("x", smallChatBytes)
	smallChat := strings.Repeat("x", smallChatBytes/4)

	tests := []struct {
		name       string
		chat       string
		resultText string
		useTmux    bool
		want       bool
	}{
		{"tmux always summarizes", smallChat, "irrelevant", true, true},
		{"tmux with no result text still summarizes", smallChat, "", true, true},
		{"non-tmux empty result text always summarizes", smallChat, "", false, true},
		{"non-tmux small chat with result text short-circuits", smallChat, "Done!", false, false},
		{"non-tmux large chat with result text summarizes", bigChat, "Done!", false, true},
		{"non-tmux whitespace-only result text counts as empty", smallChat, "   \n  ", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSummarizeChat(tt.chat, tt.resultText, tt.useTmux); got != tt.want {
				t.Errorf("shouldSummarizeChat(len=%d, result=%q, tmux=%v) = %v, want %v",
					len(tt.chat), tt.resultText, tt.useTmux, got, tt.want)
			}
		})
	}
}

func TestSplitOnLineBoundary(t *testing.T) {
	t.Run("under limit returns single chunk", func(t *testing.T) {
		input := "one\ntwo\nthree"
		got := splitOnLineBoundary(input, 1024)
		if len(got) != 1 || got[0] != input {
			t.Errorf("got %#v, want [%q]", got, input)
		}
	})

	t.Run("splits on line boundaries without breaking lines", func(t *testing.T) {
		// Each line is ~50 bytes; with maxBytes=100 we expect ~2 lines per chunk.
		var lines []string
		for i := 0; i < 10; i++ {
			lines = append(lines, fmt.Sprintf("line-%02d-%s", i, strings.Repeat("x", 40)))
		}
		input := strings.Join(lines, "\n")
		chunks := splitOnLineBoundary(input, 100)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		// Reassembling the chunks with newlines must recover the original content.
		if got := strings.Join(chunks, "\n"); got != input {
			t.Errorf("reassembled chunks != input\nwant: %q\ngot:  %q", input, got)
		}
		// Each chunk must end at a complete line.
		for i, ch := range chunks {
			if strings.HasSuffix(ch, "\n") {
				t.Errorf("chunk %d has trailing newline: %q", i, ch)
			}
		}
	})

	t.Run("oversized single line yields its own oversized chunk", func(t *testing.T) {
		long := strings.Repeat("a", 300)
		input := "short\n" + long + "\nshort"
		chunks := splitOnLineBoundary(input, 100)
		if len(chunks) < 2 {
			t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
		}
		// The long line must appear intact in exactly one chunk.
		found := false
		for _, ch := range chunks {
			if strings.Contains(ch, long) {
				if found {
					t.Errorf("long line appears in multiple chunks")
				}
				found = true
			}
		}
		if !found {
			t.Errorf("long line not found intact in any chunk: %#v", chunks)
		}
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		got := splitOnLineBoundary("", 100)
		if len(got) != 1 || got[0] != "" {
			t.Errorf("got %#v, want [\"\"]", got)
		}
	})
}

// TestSummarizeChatLogNoSummarizerConfigured verifies the degradation contract:
// with no `summarizer:` command configured, a summarize pass surfaces
// ErrNoSummarizer instead of silently returning an empty summary.
func TestSummarizeChatLogNoSummarizerConfigured(t *testing.T) {
	e := &Engine{
		cfg:      newEngineConfig(&config.Config{}),
		database: newFakeTaskStore(),
	}
	tk := &task.Task{ID: 1, Title: "no summarizer", WorktreePath: t.TempDir()}

	_, err := e.summarizeChatLog(context.Background(), tk, "grill", "", "some chat content")
	if err == nil {
		t.Fatal("expected an error when no summarizer is configured")
	}
	if !errors.Is(err, ErrNoSummarizer) {
		t.Errorf("expected error to wrap ErrNoSummarizer, got %v", err)
	}
}

// TestSummarizeChatLogMapReduce covers the summarizer.max_prompt_bytes gate:
// a prompt under the ceiling (or with chunking disabled) runs a single
// summarizer invocation, while an oversized chat is split map-reduce style —
// several summarize_chat_chunk passes followed by one final summarize_chat
// reduce pass. Invocations are counted via SORTIE_PURPOSE lines the stub
// appends to a file.
func TestSummarizeChatLogMapReduce(t *testing.T) {
	newSummarizerEngine := func(t *testing.T, maxPromptBytes int) (*Engine, string) {
		t.Helper()
		dir := t.TempDir()
		callLog := filepath.Join(dir, "calls.log")
		cmd := fmt.Sprintf(`cat > /dev/null; echo "$SORTIE_PURPOSE" >> %q; echo SUMMARY`, callLog)
		cfg := &config.Config{
			Summarizer: config.SummarizerConfig{Command: cmd, MaxPromptBytes: maxPromptBytes},
		}
		return &Engine{cfg: newEngineConfig(cfg), database: newFakeTaskStore()}, callLog
	}
	readCalls := func(t *testing.T, callLog string) []string {
		t.Helper()
		data, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	tk := &task.Task{ID: 1, Title: "map reduce"}

	t.Run("chunking disabled (max_prompt_bytes=0) runs one pass", func(t *testing.T) {
		e, callLog := newSummarizerEngine(t, 0)
		chat := strings.Repeat("line of chat content\n", 500)
		summary, err := e.summarizeChatLog(context.Background(), tk, "grill", "", chat)
		if err != nil {
			t.Fatalf("summarizeChatLog: %v", err)
		}
		if summary != "SUMMARY" {
			t.Errorf("summary = %q, want %q", summary, "SUMMARY")
		}
		calls := readCalls(t, callLog)
		if len(calls) != 1 || calls[0] != "summarize_chat" {
			t.Errorf("expected exactly one summarize_chat call, got %v", calls)
		}
	})

	t.Run("oversized prompt runs map passes then a reduce pass", func(t *testing.T) {
		// maxPromptBytes is far below the chunk headroom, so chunk size falls
		// back to maxPromptBytes itself; ~6KB of chat across a 2KB ceiling
		// yields multiple chunks.
		e, callLog := newSummarizerEngine(t, 2048)
		chat := strings.Repeat("line of chat content\n", 300)
		summary, err := e.summarizeChatLog(context.Background(), tk, "grill", "", chat)
		if err != nil {
			t.Fatalf("summarizeChatLog: %v", err)
		}
		if summary != "SUMMARY" {
			t.Errorf("summary = %q, want %q", summary, "SUMMARY")
		}
		calls := readCalls(t, callLog)
		if len(calls) < 3 {
			t.Fatalf("expected at least 2 map calls + 1 reduce call, got %v", calls)
		}
		for i, c := range calls[:len(calls)-1] {
			if c != "summarize_chat_chunk" {
				t.Errorf("call %d = %q, want summarize_chat_chunk", i, c)
			}
		}
		if last := calls[len(calls)-1]; last != "summarize_chat" {
			t.Errorf("final call = %q, want summarize_chat (the reduce pass)", last)
		}
	})
}

// TestLoadStepChatContentTmuxEnvContract verifies the tmux half of
// loadStepChatContent: the agent record's chat_log_command runs with the
// documented env contract (SORTIE_SESSION_ID from the recorded chat row,
// SORTIE_TASK_ID/SORTIE_STEP, and SORTIE_TRANSCRIPT_PATH/SORTIE_SENTINEL_FILE
// from the step's most recent sentinel), and its stdout becomes the chat
// content. A failing chat_log_command surfaces an error naming the mechanism.
func TestLoadStepChatContentTmuxEnvContract(t *testing.T) {
	// Echoes each env-contract variable so the returned chat content doubles
	// as an assertion surface; ${VAR:-none} pins the "absent" cases.
	const envDumpCmd = `echo "sid=${SORTIE_SESSION_ID:-none} task=$SORTIE_TASK_ID step=$SORTIE_STEP tp=${SORTIE_TRANSCRIPT_PATH:-none} sf=${SORTIE_SENTINEL_FILE:-none}"`

	newTmuxChatEngine := func(t *testing.T, chatLogCommand string) (*Engine, *config.WorkflowConfig, config.StepConfig, *fakeTaskStore, *task.Task) {
		t.Helper()
		wt := t.TempDir()
		cfg := &config.Config{
			Agents: map[string]config.AgentConfig{
				"claude-tmux": {Mode: config.AgentModeTmux, Command: "true", ChatLogCommand: chatLogCommand},
			},
			Workflows: []config.WorkflowConfig{{
				Name:  "wf",
				Agent: "claude-tmux",
				Steps: []config.StepConfig{{Name: "grill"}},
			}},
		}
		store := newFakeTaskStore()
		e := &Engine{cfg: newEngineConfig(cfg), database: store}
		tk := &task.Task{ID: 42, WorktreePath: wt}
		wf := cfg.GetWorkflow("wf")
		return e, wf, wf.Steps[0], store, tk
	}

	t.Run("recorded session and sentinel transcript_path reach the command", func(t *testing.T) {
		e, wf, step, store, tk := newTmuxChatEngine(t, envDumpCmd)
		if err := store.SetChatSessionID(tk.ID, "grill", "sess-123"); err != nil {
			t.Fatalf("SetChatSessionID: %v", err)
		}
		writeSentinel(t, tk.WorktreePath, "grill-1234567890.json", `{"session_id":"sess-123","transcript_path":"/tmp/grill.jsonl"}`)
		sentinelPath := filepath.Join(StepDoneDir(tk.WorktreePath), "grill-1234567890.json")

		out, err := e.loadStepChatContent(context.Background(), tk, wf, step, true)
		if err != nil {
			t.Fatalf("loadStepChatContent: %v", err)
		}
		for _, want := range []string{
			"sid=sess-123",
			"task=42",
			"step=grill",
			"tp=/tmp/grill.jsonl",
			"sf=" + sentinelPath,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("chat content = %q, expected it to contain %q", out, want)
			}
		}
	})

	t.Run("no session and no transcript_path degrade to absent env vars", func(t *testing.T) {
		e, wf, step, _, tk := newTmuxChatEngine(t, envDumpCmd)
		// Sentinel exists (so SORTIE_SENTINEL_FILE is set) but carries no
		// payload fields; no chat row was ever recorded.
		writeSentinel(t, tk.WorktreePath, "grill-1234567890.json", `{}`)
		sentinelPath := filepath.Join(StepDoneDir(tk.WorktreePath), "grill-1234567890.json")

		out, err := e.loadStepChatContent(context.Background(), tk, wf, step, true)
		if err != nil {
			t.Fatalf("loadStepChatContent: %v", err)
		}
		for _, want := range []string{"sid=none", "tp=none", "sf=" + sentinelPath} {
			if !strings.Contains(out, want) {
				t.Errorf("chat content = %q, expected it to contain %q", out, want)
			}
		}
	})

	t.Run("failing chat_log_command surfaces an error", func(t *testing.T) {
		e, wf, step, _, tk := newTmuxChatEngine(t, "exit 1")
		writeSentinel(t, tk.WorktreePath, "grill-1234567890.json", `{}`)

		_, err := e.loadStepChatContent(context.Background(), tk, wf, step, true)
		if err == nil {
			t.Fatal("expected an error when the chat_log_command exits non-zero")
		}
		if !strings.Contains(err.Error(), "chat_log_command failed") {
			t.Errorf("error = %q, expected it to name chat_log_command", err.Error())
		}
	})
}

// TestSummarizeChatLogMapReduceHeadroom extends the map-reduce coverage to the
// normal headroom path: with max_prompt_bytes comfortably above
// chunkHeadroomBytes, chunks are sized to `max - headroom` (not the tiny-ceiling
// fallback), every summarizer invocation's prompt stays under the ceiling, the
// chunk-call count matches splitOnLineBoundary's arithmetic, and the final
// reduce prompt joins the chunk summaries with the CHUNK BOUNDARY marker.
func TestSummarizeChatLogMapReduceHeadroom(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	lastPrompt := filepath.Join(dir, "last-prompt.txt")
	// The stub records "<purpose> <prompt bytes>" per call, keeps the most
	// recent prompt (the reduce pass runs last), and returns a fixed summary.
	cmd := fmt.Sprintf(`p=$(cat); printf '%%s %%s\n' "$SORTIE_PURPOSE" "${#p}" >> %q; printf '%%s' "$p" > %q; echo CHUNK-OK`, callLog, lastPrompt)

	const maxPromptBytes = 40 * 1024 // well above chunkHeadroomBytes: normal path
	cfg := &config.Config{
		Summarizer: config.SummarizerConfig{Command: cmd, MaxPromptBytes: maxPromptBytes},
	}
	e := &Engine{cfg: newEngineConfig(cfg), database: newFakeTaskStore()}
	tk := &task.Task{ID: 1, Title: "headroom"}

	// ~46KB of 20-char lines: over the 40KB ceiling, and sized to need
	// several `max - headroom` chunks.
	chat := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 20)+"\n", 2200), "\n")
	chunkBytes := maxPromptBytes - chunkHeadroomBytes
	if chunkBytes < 1024 {
		t.Fatalf("test setup: chunkBytes = %d must exercise the normal headroom path", chunkBytes)
	}
	wantChunks := len(splitOnLineBoundary(chat, chunkBytes))
	if wantChunks < 2 {
		t.Fatalf("test setup: expected the chat to need >= 2 chunks, got %d", wantChunks)
	}

	summary, err := e.summarizeChatLog(context.Background(), tk, "grill", "", chat)
	if err != nil {
		t.Fatalf("summarizeChatLog: %v", err)
	}
	if summary != "CHUNK-OK" {
		t.Errorf("summary = %q, want %q", summary, "CHUNK-OK")
	}

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != wantChunks+1 {
		t.Fatalf("expected %d map calls + 1 reduce call, got %d: %v", wantChunks, len(calls), calls)
	}
	for i, c := range calls {
		fields := strings.Fields(c)
		if len(fields) != 2 {
			t.Fatalf("call log line %d = %q, want \"<purpose> <bytes>\"", i, c)
		}
		wantPurpose := "summarize_chat_chunk"
		if i == len(calls)-1 {
			wantPurpose = "summarize_chat" // the reduce pass
		}
		if fields[0] != wantPurpose {
			t.Errorf("call %d purpose = %q, want %q", i, fields[0], wantPurpose)
		}
		// The headroom exists precisely so chunk + instruction prompt stays
		// under the configured ceiling.
		size, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("call %d prompt size %q not a number: %v", i, fields[1], err)
		}
		if size > maxPromptBytes {
			t.Errorf("call %d prompt = %d bytes, exceeds max_prompt_bytes %d", i, size, maxPromptBytes)
		}
	}

	reducePrompt, err := os.ReadFile(lastPrompt)
	if err != nil {
		t.Fatalf("read last prompt: %v", err)
	}
	if !strings.Contains(string(reducePrompt), "--- CHUNK BOUNDARY ---") {
		t.Errorf("reduce prompt does not contain the chunk-boundary marker:\n%s", reducePrompt)
	}
}

func TestExtractLatestStepRegion(t *testing.T) {
	t.Run("returns empty when step header absent", func(t *testing.T) {
		got := extractLatestStepRegion("[10:00:00] some unrelated line\n", "implement")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("returns region between header and footer", func(t *testing.T) {
		content := "[10:00:00] === Step: implement (task #42) ===\n" +
			"[10:00:00] Prompt:\n" +
			"[10:00:01] body line\n" +
			"[10:00:02] === Step implement finished (exit=0) ===\n"
		got := extractLatestStepRegion(content, "implement")
		if !strings.Contains(got, "Prompt:") {
			t.Errorf("expected prompt line in region, got %q", got)
		}
		if !strings.Contains(got, "body line") {
			t.Errorf("expected body line in region, got %q", got)
		}
		if !strings.Contains(got, "implement finished") {
			t.Errorf("expected footer in region, got %q", got)
		}
	})

	t.Run("picks the most recent run on retry", func(t *testing.T) {
		content := "[10:00:00] === Step: implement (task #42) ===\n" +
			"[10:00:01] first attempt body\n" +
			"[10:00:02] === Step implement finished (exit=1) ===\n" +
			"[10:01:00] === Step: implement (task #42) ===\n" +
			"[10:01:01] second attempt body\n" +
			"[10:01:02] === Step implement finished (exit=0) ===\n"
		got := extractLatestStepRegion(content, "implement")
		if strings.Contains(got, "first attempt body") {
			t.Errorf("expected only most recent run, got %q", got)
		}
		if !strings.Contains(got, "second attempt body") {
			t.Errorf("expected second attempt body, got %q", got)
		}
	})

	t.Run("ignores other steps", func(t *testing.T) {
		content := "[10:00:00] === Step: review (task #42) ===\n" +
			"[10:00:01] review body\n" +
			"[10:00:02] === Step review finished (exit=0) ===\n" +
			"[10:01:00] === Step: implement (task #42) ===\n" +
			"[10:01:01] implement body\n" +
			"[10:01:02] === Step implement finished (exit=0) ===\n"
		got := extractLatestStepRegion(content, "implement")
		if strings.Contains(got, "review body") {
			t.Errorf("expected no review content, got %q", got)
		}
		if !strings.Contains(got, "implement body") {
			t.Errorf("expected implement body, got %q", got)
		}
	})

	t.Run("returns through end of file when footer missing", func(t *testing.T) {
		content := "[10:00:00] === Step: implement (task #42) ===\n" +
			"[10:00:01] streaming body\n"
		got := extractLatestStepRegion(content, "implement")
		if !strings.Contains(got, "streaming body") {
			t.Errorf("expected body through EOF, got %q", got)
		}
	})

	t.Run("handles iteration suffix in header", func(t *testing.T) {
		content := "[10:00:00] === Step: implement (task #42) [iteration 2] ===\n" +
			"[10:00:01] iteration body\n" +
			"[10:00:02] === Step implement finished (exit=0) ===\n"
		got := extractLatestStepRegion(content, "implement")
		if !strings.Contains(got, "iteration body") {
			t.Errorf("expected iteration body, got %q", got)
		}
	})
}

// TestBuildSummarizePrompt covers the user-facing summarization_prompt
// contract: a custom prompt with a {{chat}} placeholder gets the chat spliced
// in-place (task template vars also resolved); a custom prompt WITHOUT the
// placeholder gets the chat appended under a conversation-log divider; and an
// empty custom prompt falls back to the default, which must carry the step
// name, task id, and chat content.
func TestBuildSummarizePrompt(t *testing.T) {
	engine := &Engine{
		cfg:      newEngineConfig(&config.Config{}),
		database: newFakeTaskStore(),
	}
	tk := &task.Task{ID: 7, Title: "fix the widget"}

	t.Run("custom prompt with chat placeholder", func(t *testing.T) {
		got := engine.buildSummarizePrompt(tk, "implement",
			"Summarize task {{task.id}} ({{task.title}}):\n{{chat}}\nEND", "THE CHAT LOG")
		want := "Summarize task 7 (fix the widget):\nTHE CHAT LOG\nEND"
		if got != want {
			t.Errorf("prompt = %q, want %q", got, want)
		}
		if strings.Contains(got, "--- CONVERSATION LOG ---") {
			t.Errorf("placeholder prompt must not also append the log divider: %q", got)
		}
	})

	t.Run("custom prompt without placeholder appends the log", func(t *testing.T) {
		got := engine.buildSummarizePrompt(tk, "implement", "Summarize this.", "THE CHAT LOG")
		want := "Summarize this.\n\n--- CONVERSATION LOG ---\nTHE CHAT LOG"
		if got != want {
			t.Errorf("prompt = %q, want %q", got, want)
		}
	})

	t.Run("empty custom prompt uses the default", func(t *testing.T) {
		got := engine.buildSummarizePrompt(tk, "implement", "", "THE CHAT LOG")
		for _, want := range []string{`"implement"`, "#7", "fix the widget", "--- CONVERSATION LOG ---\nTHE CHAT LOG"} {
			if !strings.Contains(got, want) {
				t.Errorf("default prompt missing %q:\n%s", want, got)
			}
		}
	})
}
