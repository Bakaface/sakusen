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
