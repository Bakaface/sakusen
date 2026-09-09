package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkdir(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
		clean   string
	}{
		{in: "", clean: ""},
		{in: ".", clean: ""},
		{in: "./", clean: ""},
		{in: "sub/dir", clean: "sub/dir"},
		{in: "./sub/dir/", clean: "sub/dir"},
		{in: "a/../b", clean: "b"},
		{in: "/abs", wantErr: true},
		{in: "../x", wantErr: true},
		{in: "a/../../x", wantErr: true},
		{in: "..", wantErr: true},
	}
	for _, tt := range tests {
		err := ValidateWorkdir(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateWorkdir(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr {
			if got := CleanWorkdir(tt.in); got != tt.clean {
				t.Errorf("CleanWorkdir(%q) = %q, want %q", tt.in, got, tt.clean)
			}
		}
	}
}

func TestWorkflowWorkdirPin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, `
workflows:
  - name: sub
    workdir: em/rails-foundation
    steps:
      - name: implement
        prompt: "do it"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if wf := cfg.GetTaskWorkflow("sub"); wf == nil || wf.Workdir != "em/rails-foundation" {
		t.Fatalf("workdir pin not parsed: %+v", wf)
	}
}

func TestWorkflowWorkdirPinRejected(t *testing.T) {
	for _, bad := range []string{"/abs", "../x", "a/../../x"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := writeTempConfig(t, `
workflows:
  - name: sub
    workdir: "`+bad+`"
    steps:
      - name: implement
        prompt: "do it"
`)
			for name, err := range map[string]error{
				"validate": ValidateFile(path),
				"load":     func() error { _, err := LoadForProject(filepath.Dir(path)); return err }(),
			} {
				if err == nil || !strings.Contains(err.Error(), `workflow "sub"`) || !strings.Contains(err.Error(), "workdir") {
					t.Errorf("%s: expected workdir error naming the workflow, got: %v", name, err)
				}
			}
		})
	}
}
