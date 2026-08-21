package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

func setupDetailView(lines []string) detailView {
	d := newDetailView()
	d.SetTask(&daemon.TaskInfo{
		ID:     1,
		Title:  "Test task",
		Status: "running",
	})
	d.SetSize(80, 24)
	if lines != nil {
		d.SetOutput(lines)
	}
	return d
}

func TestSetOutput_SkipsUpdateWhenContentUnchanged(t *testing.T) {
	d := setupDetailView([]string{"line1", "line2", "line3"})

	// Record the viewport state after first SetOutput
	initialLineCount := d.contentLineCount
	if initialLineCount != 3 {
		t.Fatalf("expected contentLineCount to be 3, got %d", initialLineCount)
	}

	// SetOutput with same line count should be a no-op (skip)
	d.SetOutput([]string{"line1", "line2", "line3"})

	// contentLineCount should remain unchanged (wasn't re-processed)
	if d.contentLineCount != 3 {
		t.Errorf("expected contentLineCount to remain 3, got %d", d.contentLineCount)
	}
	if d.contentDirty {
		t.Error("expected contentDirty to be false")
	}
}

func TestSetOutput_UpdatesWhenContentGrows(t *testing.T) {
	d := setupDetailView([]string{"line1", "line2"})

	if d.contentLineCount != 2 {
		t.Fatalf("expected contentLineCount to be 2, got %d", d.contentLineCount)
	}

	// SetOutput with more lines should trigger update
	d.SetOutput([]string{"line1", "line2", "line3"})

	if d.contentLineCount != 3 {
		t.Errorf("expected contentLineCount to be 3 after growth, got %d", d.contentLineCount)
	}
}

func TestSetOutput_UpdatesWhenContentDirty(t *testing.T) {
	d := setupDetailView([]string{"line1", "line2"})

	// Simulate a resize which sets contentDirty
	d.contentDirty = true

	// SetOutput with same line count should still update because dirty
	d.SetOutput([]string{"line1", "line2"})

	if d.contentDirty {
		t.Error("expected contentDirty to be cleared after update")
	}
	if d.contentLineCount != 2 {
		t.Errorf("expected contentLineCount to be 2, got %d", d.contentLineCount)
	}
}

func TestSetOutput_UpdatesWhenContentShrinks(t *testing.T) {
	d := setupDetailView([]string{"line1", "line2", "line3"})

	// SetOutput with fewer lines should trigger update
	d.SetOutput([]string{"line1"})

	if d.contentLineCount != 1 {
		t.Errorf("expected contentLineCount to be 1 after shrink, got %d", d.contentLineCount)
	}
}

func TestAppendOutput_MarksContentDirty(t *testing.T) {
	d := setupDetailView([]string{"line1"})

	d.AppendOutput([]string{"line2"})

	// After AppendOutput -> updateViewportContent, dirty should be cleared
	// and line count updated
	if d.contentDirty {
		t.Error("expected contentDirty to be false after AppendOutput completes")
	}
	if d.contentLineCount != 2 {
		t.Errorf("expected contentLineCount to be 2, got %d", d.contentLineCount)
	}
}

func TestRecalcViewport_MarksContentDirty(t *testing.T) {
	d := setupDetailView([]string{"line1", "line2"})

	// Clear dirty state
	d.contentDirty = false

	// Resize should mark dirty and re-process
	d.SetSize(100, 30)

	// After recalcViewport, dirty should be cleared (processed)
	if d.contentDirty {
		t.Error("expected contentDirty to be false after recalc")
	}
}

func TestUpdateViewportContent_SetsContentDirectly(t *testing.T) {
	d := setupDetailView(nil)

	d.output = []string{"hello", "world"}
	d.rebuildViewportContent()

	// Verify the viewport received the content
	view := d.viewport.View()
	if !strings.Contains(view, "hello") {
		t.Error("expected viewport to contain 'hello'")
	}
	if !strings.Contains(view, "world") {
		t.Error("expected viewport to contain 'world'")
	}
}

func BenchmarkSetOutput_Unchanged(b *testing.B) {
	d := setupDetailView(nil)

	// Create a large output similar to real log files
	lines := make([]string, 10000)
	for i := range lines {
		lines[i] = fmt.Sprintf("[2024-01-01 12:00:00] Log line %d with some typical content and padding", i)
	}

	d.SetOutput(lines)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulates the tick handler calling SetOutput with unchanged content
		d.SetOutput(lines)
	}
}

func BenchmarkSetOutput_Growing(b *testing.B) {
	d := setupDetailView(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Reset state
		d.output = nil
		d.contentLineCount = 0
		d.contentDirty = false

		lines := make([]string, 1000)
		for j := range lines {
			lines[j] = fmt.Sprintf("[2024-01-01 12:00:00] Log line %d", j)
		}
		b.StartTimer()

		d.SetOutput(lines)
	}
}
