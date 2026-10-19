package config

import (
	"strings"
	"testing"
)

func TestValidatePeriodic_AfterTriggers(t *testing.T) {
	cases := []struct {
		name    string
		entries string
		wantErr string // empty ⇒ valid
	}{
		{
			name: "valid chain",
			entries: `
  - name: a
    cadence: "0 0 * * *"
    workflow: default
  - name: b
    after: a
    workflow: default
  - name: c
    after: b
    workflow: default
`,
		},
		{
			name: "neither cadence nor after",
			entries: `
  - name: a
    workflow: default
`,
			wantErr: "cadence or after is required",
		},
		{
			name: "both cadence and after",
			entries: `
  - name: a
    cadence: "0 0 * * *"
    workflow: default
  - name: b
    cadence: "0 1 * * *"
    after: a
    workflow: default
`,
			wantErr: "cadence and after are mutually exclusive",
		},
		{
			name: "unknown upstream",
			entries: `
  - name: b
    after: nope
    workflow: default
`,
			wantErr: `unknown periodic "nope"`,
		},
		{
			name: "self reference",
			entries: `
  - name: b
    after: b
    workflow: default
`,
			wantErr: "not itself",
		},
		{
			name: "two-cycle",
			entries: `
  - name: a
    after: b
    workflow: default
  - name: b
    after: a
    workflow: default
`,
			wantErr: "cycle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, periodicBaseWorkflows+"\nperiodic:"+tc.entries)
			err := ValidateFile(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
