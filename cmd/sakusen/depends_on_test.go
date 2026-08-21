package main

import (
	"strings"
	"testing"
)

func TestParseDependsOnArgs_Valid(t *testing.T) {
	taskID, blockedBy, err := parseDependsOnArgs([]string{"5", "3"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if taskID != 5 || blockedBy != 3 {
		t.Errorf("expected (5, 3), got (%d, %d)", taskID, blockedBy)
	}
}

func TestParseDependsOnArgs_InvalidTaskID(t *testing.T) {
	_, _, err := parseDependsOnArgs([]string{"abc", "3"})
	if err == nil {
		t.Fatal("expected error for non-numeric task ID")
	}
	if !strings.Contains(err.Error(), "invalid task ID") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseDependsOnArgs_InvalidBlockedBy(t *testing.T) {
	_, _, err := parseDependsOnArgs([]string{"5", "xyz"})
	if err == nil {
		t.Fatal("expected error for non-numeric blocked-by ID")
	}
	if !strings.Contains(err.Error(), "invalid blocked-by ID") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseDependsOnArgs_SelfDependency(t *testing.T) {
	_, _, err := parseDependsOnArgs([]string{"7", "7"})
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
	if !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDependsOnSubcommandsRegistered(t *testing.T) {
	if dependsOnCmd.Name() != "depends-on" {
		t.Errorf("expected command name depends-on, got %q", dependsOnCmd.Name())
	}
	for _, want := range []string{"add", "rm", "list"} {
		if c, _, err := dependsOnCmd.Find([]string{want}); err != nil || c == dependsOnCmd {
			t.Errorf("expected subcommand %q to be registered", want)
		}
	}
}
