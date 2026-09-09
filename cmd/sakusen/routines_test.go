package main

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

func TestRoutinesCmd_Registered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "routines" {
			found = true
		}
		if c.Name() == "periodics" {
			t.Error("the removed 'periodics' command is still registered")
		}
	}
	if !found {
		t.Fatal("expected 'routines' command registered on root")
	}

	subs := map[string]bool{}
	for _, c := range routinesCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"list", "show", "pause", "resume", "runs", "run"} {
		if !subs[want] {
			t.Errorf("expected 'routines %s' subcommand", want)
		}
	}
}

// `run` takes an optional input argument; every other verb takes exactly the
// routine name.
func TestRoutinesCmd_ArgCounts(t *testing.T) {
	if err := routinesRunCmd.Args(routinesRunCmd, []string{"compose-wiki"}); err != nil {
		t.Errorf("run <name>: %v", err)
	}
	if err := routinesRunCmd.Args(routinesRunCmd, []string{"digest", "the section"}); err != nil {
		t.Errorf("run <name> <input>: %v", err)
	}
	if err := routinesRunCmd.Args(routinesRunCmd, []string{"a", "b", "c"}); err == nil {
		t.Error("run must reject a third argument")
	}
	if err := routinesRunCmd.Args(routinesRunCmd, nil); err == nil {
		t.Error("run must require a name")
	}

	for _, cmd := range []struct {
		name string
		args func([]string) error
	}{
		{"show", func(a []string) error { return routinesShowCmd.Args(routinesShowCmd, a) }},
		{"pause", func(a []string) error { return routinesPauseCmd.Args(routinesPauseCmd, a) }},
		{"resume", func(a []string) error { return routinesResumeCmd.Args(routinesResumeCmd, a) }},
		{"runs", func(a []string) error { return routinesRunsCmd.Args(routinesRunsCmd, a) }},
	} {
		if err := cmd.args([]string{"compose-wiki"}); err != nil {
			t.Errorf("%s <name>: %v", cmd.name, err)
		}
		if err := cmd.args([]string{"a", "b"}); err == nil {
			t.Errorf("%s must take exactly one argument", cmd.name)
		}
	}
}

func TestTasksCmd_RoutineIDFlag(t *testing.T) {
	if tasksCmd.Flag("routine-id") == nil {
		t.Fatal("expected --routine-id flag on tasks command")
	}
	if tasksCmd.Flag("periodic-id") != nil {
		t.Error("the removed --periodic-id flag is still registered")
	}
}

func TestRoutineStatusLabel(t *testing.T) {
	cases := []struct {
		cadence string
		paused  bool
		want    string
	}{
		{"0 3 * * *", false, "active"},
		{"0 3 * * *", true, "paused"},
		{"", false, "on-demand"},
		{"", true, "on-demand"},
	}
	for _, c := range cases {
		got := routineStatusLabel(daemon.PeriodicInfo{Cadence: c.cadence, Paused: c.paused})
		if got != c.want {
			t.Errorf("cadence=%q paused=%v: got %q, want %q", c.cadence, c.paused, got, c.want)
		}
	}
}
