package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateStepContextFlags_RequiresOne(t *testing.T) {
	if err := validateStepContextFlags("", false); err == nil {
		t.Fatal("expected error when neither --step nor --all is set")
	} else if !strings.Contains(err.Error(), "--step") || !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should mention both flags, got %q", err.Error())
	}
}

func TestValidateStepContextFlags_MutuallyExclusive(t *testing.T) {
	if err := validateStepContextFlags("planning", true); err == nil {
		t.Fatal("expected error when both --step and --all are set")
	} else if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateStepContextFlags_StepOnly(t *testing.T) {
	if err := validateStepContextFlags("planning", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateStepContextFlags_AllOnly(t *testing.T) {
	if err := validateStepContextFlags("", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatAvailableSteps_Empty(t *testing.T) {
	if got := formatAvailableSteps(nil); got != "(none)" {
		t.Errorf("expected (none), got %q", got)
	}
	if got := formatAvailableSteps(map[string]string{}); got != "(none)" {
		t.Errorf("expected (none) for empty map, got %q", got)
	}
}

func TestFormatAvailableSteps_Sorted(t *testing.T) {
	steps := map[string]string{
		"review":   "",
		"planning": "",
		"build":    "",
	}
	got := formatAvailableSteps(steps)
	want := "build, planning, review"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestWriteStepContext_StepRawBytes(t *testing.T) {
	// The whole point of this command is `> file.md`-safe output — verify
	// the writer receives the stored bytes verbatim, no surrounding fluff
	// and no extra trailing newline.
	steps := map[string]string{"planning": "# PRD\n\nLine two."}
	var buf bytes.Buffer
	if err := writeStepContext(&buf, steps, "planning", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := buf.String(); got != "# PRD\n\nLine two." {
		t.Errorf("expected verbatim bytes, got %q", got)
	}
}

func TestWriteStepContext_StepPreservesTrailingNewline(t *testing.T) {
	// If the stored data ends with a newline, the writer must keep it —
	// the rule is "no newline beyond what's in the data", not "strip
	// trailing newline".
	steps := map[string]string{"planning": "ends with newline\n"}
	var buf bytes.Buffer
	if err := writeStepContext(&buf, steps, "planning", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := buf.String(); got != "ends with newline\n" {
		t.Errorf("expected preserved trailing newline, got %q", got)
	}
}

func TestWriteStepContext_StepNotFound(t *testing.T) {
	steps := map[string]string{
		"planning": "...",
		"build":    "...",
	}
	err := writeStepContext(&bytes.Buffer{}, steps, "missing", false)
	if err == nil {
		t.Fatal("expected error for missing step")
	}
	if !strings.Contains(err.Error(), `step "missing" not found`) {
		t.Errorf("expected helpful step-name error, got %q", err.Error())
	}
	// The error must list the steps that ARE available so the caller can
	// fix the invocation without going to the TUI.
	if !strings.Contains(err.Error(), "build, planning") {
		t.Errorf("error should list available steps in sorted order, got %q", err.Error())
	}
}

func TestWriteStepContext_StepNotFound_EmptyMap(t *testing.T) {
	err := writeStepContext(&bytes.Buffer{}, map[string]string{}, "planning", false)
	if err == nil {
		t.Fatal("expected error for missing step on empty map")
	}
	if !strings.Contains(err.Error(), "(none)") {
		t.Errorf("error should say (none) when no steps exist, got %q", err.Error())
	}
}

func TestWriteStepContext_AllAsJSON(t *testing.T) {
	steps := map[string]string{
		"planning": "# PRD",
		"build":    "log output",
	}
	var buf bytes.Buffer
	if err := writeStepContext(&buf, steps, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, buf.String())
	}
	if len(decoded) != 2 || decoded["planning"] != "# PRD" || decoded["build"] != "log output" {
		t.Errorf("unexpected decoded payload: %#v", decoded)
	}
}

func TestWriteStepContext_AllEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeStepContext(&buf, nil, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty JSON object, not "null" — downstream tooling expects {}.
	if got := strings.TrimSpace(buf.String()); got != "{}" {
		t.Errorf("expected {}, got %q", got)
	}
}

func TestStepContextCmd_InvalidTaskID(t *testing.T) {
	err := stepContextCmd.RunE(stepContextCmd, []string{"not-a-number"})
	if err == nil {
		t.Fatal("expected error for non-numeric task ID")
	}
	if got := err.Error(); got != "invalid task ID: not-a-number" {
		t.Errorf("unexpected error: %q", got)
	}
}

func TestStepContextCmd_StepFlagRegistered(t *testing.T) {
	if f := stepContextCmd.Flag("step"); f == nil {
		t.Error("expected --step flag to be registered")
	}
	if f := stepContextCmd.Flag("all"); f == nil {
		t.Error("expected --all flag to be registered")
	}
}
