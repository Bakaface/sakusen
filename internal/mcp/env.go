package mcp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The workflow engine injects these into every step's agent subprocess; an MCP
// server spawned inside that process inherits them, so agent-facing tools can
// default their identity arguments instead of demanding them.
const (
	envTaskID   = "SAKUSEN_TASK_ID"
	envStepName = "SAKUSEN_STEP"
)

// resolveTaskIDFromEnv returns explicit when set, else parses envTaskID. argName
// is the tool argument being defaulted, so the error names what the caller
// should pass.
func resolveTaskIDFromEnv(explicit int64, argName string) (int64, error) {
	if explicit > 0 {
		return explicit, nil
	}
	// Only an omitted argument (zero) falls back to the env; a negative one is
	// a caller mistake that must not be papered over with someone else's ID.
	if explicit < 0 {
		return 0, fmt.Errorf("%s must be a positive integer", argName)
	}
	env := os.Getenv(envTaskID)
	if env == "" {
		return 0, fmt.Errorf("%s is required (%s env var not set; this tool must be called from a running sakusen step)", argName, envTaskID)
	}
	id, err := strconv.ParseInt(env, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s=%q", envTaskID, env)
	}
	return id, nil
}

// resolveParentTaskID resolves the parent_task_id argument of the waits-on tools.
func resolveParentTaskID(explicit int64) (int64, error) {
	return resolveTaskIDFromEnv(explicit, "parent_task_id")
}

// resolveTaskID resolves the task_id argument of the calling-task tools.
func resolveTaskID(explicit int64) (int64, error) {
	return resolveTaskIDFromEnv(explicit, "task_id")
}

// resolveStepName returns explicit when non-blank, else the running step's name
// from envStepName.
func resolveStepName(explicit string) (string, error) {
	if name := strings.TrimSpace(explicit); name != "" {
		return name, nil
	}
	name := strings.TrimSpace(os.Getenv(envStepName))
	if name == "" {
		return "", fmt.Errorf("step_name is required (%s env var not set; this tool must be called from a running sakusen step)", envStepName)
	}
	return name, nil
}
