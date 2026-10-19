package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

const notificationsConfig = `
workflows:
  - name: report
    notifications:
      on_failed: false
      notify_if:
        step_context_contains: check
        marker: NEW
    steps:
      - name: check
        prompt: "look"
  - quiet:
      notifications:
        on_complete: false
      steps:
        - name: work
          prompt: "work"
periodic:
  - name: nightly-ref
    cadence: "0 3 * * *"
    workflow: report
    notifications:
      on_complete: false
  - name: nightly-inline
    cadence: "0 4 * * *"
    notifications:
      on_failed: true
      notify_if:
        step_context_nonempty: scan
    steps:
      - name: scan
        prompt: "scan"
`

func TestNotificationOverrides_Parse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, notificationsConfig)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	report := cfg.GetTaskWorkflow("report")
	if report == nil || report.Notifications == nil {
		t.Fatalf("report workflow notifications not parsed: %+v", report)
	}
	n := report.Notifications
	if n.OnComplete != nil || n.OnWaitingInput != nil {
		t.Errorf("unset fields must stay nil, got %+v", n)
	}
	if n.OnFailed == nil || *n.OnFailed {
		t.Errorf("explicit on_failed: false must be kept, got %v", n.OnFailed)
	}
	if n.NotifyIf == nil || n.NotifyIf.StepContextContains != "check" || n.NotifyIf.Marker != "NEW" {
		t.Errorf("notify_if not parsed: %+v", n.NotifyIf)
	}

	quiet := cfg.GetTaskWorkflow("quiet")
	if quiet == nil || quiet.Notifications == nil || quiet.Notifications.OnComplete == nil || *quiet.Notifications.OnComplete {
		t.Fatalf("named-body sugar workflow lost its notifications: %+v", quiet)
	}

	if len(cfg.Periodic) != 2 {
		t.Fatalf("expected 2 periodic entries, got %d", len(cfg.Periodic))
	}
	if p := cfg.Periodic[0].Notifications; p == nil || p.OnComplete == nil || *p.OnComplete {
		t.Errorf("ref-mode periodic notifications not parsed: %+v", p)
	}
	if p := cfg.Periodic[1].Notifications; p == nil || p.NotifyIf == nil || p.NotifyIf.StepContextNonempty != "scan" {
		t.Errorf("inline periodic notifications not parsed: %+v", p)
	}
	if wf := cfg.GetTaskWorkflow("periodic:nightly-inline"); wf == nil || wf.Notifications != nil {
		t.Errorf("inline periodic notifications must not be copied onto its hidden workflow: %+v", wf)
	}
}

func TestNotificationOverrides_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown key in workflow block",
			yaml: `
workflows:
  - name: w
    notifications:
      on_compelte: false
    steps:
      - name: s
        prompt: p
`,
			wantErr: `unknown field "on_compelte"`,
		},
		{
			name: "unknown key in notify_if",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_empty: s
    steps:
      - name: s
        prompt: p
`,
			wantErr: `unknown field "step_context_empty"`,
		},
		{
			name: "enabled is not an override key",
			yaml: `
periodic:
  - name: p
    cadence: "@every 1h"
    notifications:
      enabled: true
    steps:
      - name: s
        prompt: p
`,
			wantErr: `unknown field "enabled"`,
		},
		{
			name: "both forms",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_nonempty: s
        step_context_contains: s
        marker: X
    steps:
      - name: s
        prompt: p
`,
			wantErr: "not both",
		},
		{
			name: "neither form",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if: {}
    steps:
      - name: s
        prompt: p
`,
			wantErr: "set one of",
		},
		{
			name: "marker missing",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_contains: s
    steps:
      - name: s
        prompt: p
`,
			wantErr: "`marker` is required",
		},
		{
			name: "marker with nonempty",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_nonempty: s
        marker: X
    steps:
      - name: s
        prompt: p
`,
			wantErr: "only valid with `step_context_contains`",
		},
		{
			name: "unknown step in workflow",
			yaml: `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_nonempty: nope
    steps:
      - name: s
        prompt: p
`,
			wantErr: `step "nope" does not exist`,
		},
		{
			name: "unknown step in inline periodic",
			yaml: `
periodic:
  - name: p
    cadence: "@every 1h"
    notifications:
      notify_if:
        step_context_nonempty: nope
    steps:
      - name: s
        prompt: p
`,
			wantErr: `step "nope" does not exist`,
		},
		{
			name: "unknown step in ref-mode periodic",
			yaml: `
workflows:
  - name: w
    steps:
      - name: s
        prompt: p
periodic:
  - name: p
    cadence: "@every 1h"
    workflow: w
    notifications:
      notify_if:
        step_context_nonempty: nope
`,
			wantErr: `step "nope" does not exist`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFile(writeTempConfig(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNotificationOverrides_ParallelBranchIsAStep(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: w
    notifications:
      notify_if:
        step_context_contains: probe-a
        marker: FOUND
    steps:
      - name: fan
        parallel:
          branches:
            - name: probe-a
              prompt: a
            - name: probe-b
              prompt: b
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("parallel branch name should be accepted, got: %v", err)
	}
}

func TestNotifyCondition_Matches(t *testing.T) {
	contains := &NotifyCondition{StepContextContains: "s", Marker: "NEW"}
	nonempty := &NotifyCondition{StepContextNonempty: "s"}
	cases := []struct {
		cond *NotifyCondition
		ctx  string
		want bool
	}{
		{contains, "found NEW items", true},
		{contains, "found new items", false},
		{contains, "", false},
		{nonempty, "something", true},
		{nonempty, " \n\t", false},
		{nonempty, "", false},
	}
	for _, tc := range cases {
		if got := tc.cond.Matches(tc.ctx); got != tc.want {
			t.Errorf("%+v.Matches(%q) = %v, want %v", tc.cond, tc.ctx, got, tc.want)
		}
	}
}

func TestEffectiveNotifications_Cascade(t *testing.T) {
	wfCond := &NotifyCondition{StepContextNonempty: "s"}
	pCond := &NotifyCondition{StepContextContains: "s", Marker: "X"}
	cfg := &Config{
		Workflows: []WorkflowConfig{
			{Name: "plain"},
			{Name: "wf", Notifications: &NotificationOverrides{OnFailed: boolPtr(false), OnComplete: boolPtr(true), NotifyIf: wfCond}},
		},
		Periodic: []PeriodicEntry{
			{Name: "only-complete", Workflow: "wf", Notifications: &NotificationOverrides{OnComplete: boolPtr(false)}},
			{Name: "with-cond", Workflow: "wf", Notifications: &NotificationOverrides{NotifyIf: pCond}},
			{Name: "no-block", Workflow: "wf"},
		},
	}
	base := NotificationsConfig{Enabled: true, OnComplete: true, OnFailed: true, OnWaitingInput: true}

	cases := []struct {
		name     string
		workflow string
		periodic string
		want     EffectiveNotifications
	}{
		{
			name:     "no tiers set",
			workflow: "plain",
			want:     EffectiveNotifications{OnComplete: true, OnFailed: true, OnWaitingInput: true},
		},
		{
			name:     "unknown workflow skipped",
			workflow: "missing",
			want:     EffectiveNotifications{OnComplete: true, OnFailed: true, OnWaitingInput: true},
		},
		{
			name:     "workflow tier",
			workflow: "wf",
			want: EffectiveNotifications{
				OnComplete: true, OnFailed: false, OnWaitingInput: true,
				OnCompleteOverridden: true, OnFailedOverridden: true, NotifyIf: wfCond,
			},
		},
		{
			name:     "periodic and workflow merge field by field",
			workflow: "wf",
			periodic: "only-complete",
			want: EffectiveNotifications{
				OnComplete: false, OnFailed: false, OnWaitingInput: true,
				OnCompleteOverridden: true, OnFailedOverridden: true, NotifyIf: wfCond,
			},
		},
		{
			name:     "notify_if from most specific tier",
			workflow: "wf",
			periodic: "with-cond",
			want: EffectiveNotifications{
				OnComplete: true, OnFailed: false, OnWaitingInput: true,
				OnCompleteOverridden: true, OnFailedOverridden: true, NotifyIf: pCond,
			},
		},
		{
			name:     "periodic without block falls through",
			workflow: "plain",
			periodic: "no-block",
			want:     EffectiveNotifications{OnComplete: true, OnFailed: true, OnWaitingInput: true},
		},
		{
			name:     "missing periodic entry skipped",
			workflow: "wf",
			periodic: "deleted",
			want: EffectiveNotifications{
				OnComplete: true, OnFailed: false, OnWaitingInput: true,
				OnCompleteOverridden: true, OnFailedOverridden: true, NotifyIf: wfCond,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.EffectiveNotifications(tc.workflow, tc.periodic, base); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
