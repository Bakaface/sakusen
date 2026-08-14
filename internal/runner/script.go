package runner

import (
	"fmt"
	"sort"
	"strings"
)

// BuildWrapperScript renders the bash wrapper script that runs an agent
// command inside a tmux session: it exports the sortie env contract (plus any
// agent-record env), runs the command, and drops to an interactive bash so the
// pane survives for inspection after the agent exits.
//
// Env keys are exported in sorted order so identical inputs produce identical
// scripts (stable across retries/restores).
func BuildWrapperScript(command string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("export %s=%s\n", k, shellQuote(env[k])))
	}
	b.WriteString(command)
	b.WriteString("\nexec bash\n")
	return b.String()
}

// shellQuote wraps s in single quotes for literal use in a shell script,
// escaping embedded single quotes. Unlike Go's %q (double quotes), single
// quotes prevent the shell from expanding $vars and `command` substitutions,
// so env values reach the agent byte-for-byte — matching the literal
// semantics of Process.SetEnv on the headless path.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// MergeEnv overlays contract onto agentEnv and returns the result (neither
// input is mutated). Used to fold an agent record's `env:` map into sortie's
// per-spawn environment contract; the contract wins on key collisions so
// agents cannot mask SORTIE_* variables sortie relies on.
func MergeEnv(contract map[string]string, agentEnv map[string]string) map[string]string {
	out := make(map[string]string, len(contract)+len(agentEnv))
	for k, v := range agentEnv {
		out[k] = v
	}
	for k, v := range contract {
		out[k] = v
	}
	return out
}
