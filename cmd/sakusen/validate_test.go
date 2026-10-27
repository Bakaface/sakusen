package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGlobalConfig redirects HOME and XDG_CONFIG_HOME to empty temp dirs so
// that the real ~/.sakusen.yml is not loaded during validation. This prevents
// the user's global config (which may use an old format) from interfering with
// tests that exercise config.Diagnose.
func isolateGlobalConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func writeValidateFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".sakusen.yml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestValidateCmd_ExplicitPath_Valid(t *testing.T) {
	isolateGlobalConfig(t)
	path := writeValidateFixture(t, `
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "x"
`)
	err := validateCmd.RunE(validateCmd, []string{path})
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidateCmd_ExplicitPath_Invalid(t *testing.T) {
	isolateGlobalConfig(t)
	path := writeValidateFixture(t, "on_complete: boom\n")
	err := validateCmd.RunE(validateCmd, []string{path})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "on_complete") {
		t.Errorf("expected on_complete error, got: %v", err)
	}
}

func TestValidateCmd_MissingFile(t *testing.T) {
	isolateGlobalConfig(t)
	missing := filepath.Join(t.TempDir(), "nope.yml")
	err := validateCmd.RunE(validateCmd, []string{missing})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestValidateCmd_DefaultPath_UsesCwd(t *testing.T) {
	isolateGlobalConfig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte(`
workflows:
  - name: default
    steps:
      - name: a
        prompt: "x"
`), 0644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if err := validateCmd.RunE(validateCmd, nil); err != nil {
		t.Fatalf("expected default-path validation to pass, got: %v", err)
	}
}

func TestValidateCmd_DefaultPath_NoConfig(t *testing.T) {
	isolateGlobalConfig(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	err := validateCmd.RunE(validateCmd, nil)
	if err == nil {
		t.Fatal("expected error when no .sakusen.yml in cwd")
	}
	if !strings.Contains(err.Error(), "no .sakusen.yml") {
		t.Errorf("expected 'no .sakusen.yml' error, got: %v", err)
	}
}

func TestValidateCmd_FileOnlySkipsAgentRefResolution(t *testing.T) {
	isolateGlobalConfig(t)
	path := writeValidateFixture(t, `
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "x"
        agent: claude:fable
`)
	err := validateCmd.RunE(validateCmd, []string{path})
	if err == nil || !strings.Contains(err.Error(), `unknown agent "claude:fable"`) {
		t.Fatalf("expected unknown agent error, got: %v", err)
	}

	if err := validateCmd.Flags().Set("file-only", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = validateCmd.Flags().Set("file-only", "false") })
	if err := validateCmd.RunE(validateCmd, []string{path}); err != nil {
		t.Fatalf("expected --file-only to pass, got: %v", err)
	}
}

const fanoutValidateFixture = `
agents:
  claude:
    command: "claude -p"
workflows:
  - name: unit
    steps:
      - name: build
        prompt: "build"
  - name: unit-human
    steps:
      - name: approve
        prompt: "approve"
        human: true
  - name: planner
    steps:
      - name: plan
        prompt: "plan"
      - name: units
        fanout:
          from: %FROM%
          workflow: %CHILD%
%EXTRA%`

func fanoutFixture(from, child, extra string) string {
	return strings.NewReplacer("%FROM%", from, "%CHILD%", child, "%EXTRA%", extra).Replace(fanoutValidateFixture)
}

func TestValidate_FanoutValid(t *testing.T) {
	isolateGlobalConfig(t)
	path := writeValidateFixture(t, fanoutFixture("plan", "unit", ""))
	if err := validateCmd.RunE(validateCmd, []string{path}); err != nil {
		t.Fatalf("expected valid fanout config, got: %v", err)
	}
}

func TestValidate_FanoutRejects(t *testing.T) {
	cases := []struct {
		name, from, child, extra, wantErr string
	}{
		{"from not earlier", "units", "unit", "", "must name an earlier step"},
		{"human child", "plan", "unit-human", "", "has human: true"},
		{"loop over fanout", "plan", "unit", `      - name: check
        prompt: "check"
        loop:
          goto: plan
          max_iterations: 2
`, "loop range contains fanout step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGlobalConfig(t)
			path := writeValidateFixture(t, fanoutFixture(tc.from, tc.child, tc.extra))
			err := validateCmd.RunE(validateCmd, []string{path})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

const fanoutNodesValidateFixture = `
agents:
  claude:
    command: "claude -p"
workflows:
  - name: unit
    on_complete: merge
    steps:
      - name: build
        prompt: "build"
  - name: planner
    steps:
      - name: plan
        prompt: "plan"
      - name: units
        fanout:
          workflow: unit
          nodes:
            - name: api
              input: "api"
%NODES%`

func TestValidate_FanoutNodes(t *testing.T) {
	cases := []struct {
		name, nodes, wantErr string
	}{
		{"valid", `            - name: ui
              input: "{{steps.api.context}} {{steps.plan.context}}"
              needs: [api]
`, ""},
		{"cycle", `            - name: a
              input: "a"
              needs: [b]
            - name: b
              input: "b"
              needs: [a]
`, "a -> b -> a"},
		{"non-need reference", `            - name: ui
              input: "{{steps.api.context}}"
`, `fanout node "ui" input references {{steps.api.context}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGlobalConfig(t)
			path := writeValidateFixture(t, strings.Replace(fanoutNodesValidateFixture, "%NODES%", tc.nodes, 1))
			err := validateCmd.RunE(validateCmd, []string{path})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid nodes config, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// A needed node's workflow may inherit on_complete: merge from the project's
// top level; validate must apply that cascade, not the built-in default.
func TestValidate_FanoutNodesTopLevelOnComplete(t *testing.T) {
	nodes := `            - name: ui
              input: "{{steps.api.context}}"
              needs: [api]
`
	fixture := strings.Replace(strings.Replace(fanoutNodesValidateFixture, "%NODES%", nodes, 1), "    on_complete: merge\n", "", 1)
	for _, tc := range []struct{ name, top, wantErr string }{
		{"inherits merge", "on_complete: merge\n", ""},
		{"inherits commit", "on_complete: commit\n", `must have on_complete: merge (got "commit")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGlobalConfig(t)
			path := writeValidateFixture(t, tc.top+fixture)
			err := validateCmd.RunE(validateCmd, []string{path})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid nodes config, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
