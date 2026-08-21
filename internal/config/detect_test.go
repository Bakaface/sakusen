package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectGo(t *testing.T) {
	dir := t.TempDir()
	goMod := filepath.Join(dir, "go.mod")
	content := "module github.com/Bakaface/sakusen\n\ngo 1.23\n"
	if err := os.WriteFile(goMod, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	p := DetectProject(dir)
	if p.Type != ProjectTypeGo {
		t.Fatalf("expected Go project, got %s", p.Type)
	}
}

func TestApplyDetectedProjectUsesDirName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "coolapp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	goMod := filepath.Join(dir, "go.mod")
	content := "module github.com/example/anything\n\ngo 1.23\n"
	if err := os.WriteFile(goMod, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Project.AutoDetect = true
	cfg.ApplyDetectedProject(dir)

	if cfg.Project.Name != "coolapp" {
		t.Errorf("expected project name 'coolapp', got %q", cfg.Project.Name)
	}
}

func TestApplyDetectedProjectDoesNotOverrideExplicitName(t *testing.T) {
	dir := t.TempDir()
	goMod := filepath.Join(dir, "go.mod")
	content := "module github.com/example/coolapp\n\ngo 1.23\n"
	if err := os.WriteFile(goMod, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Project.AutoDetect = true
	cfg.Project.Name = "my-custom-name"
	cfg.ApplyDetectedProject(dir)

	if cfg.Project.Name != "my-custom-name" {
		t.Errorf("expected project name 'my-custom-name', got %q", cfg.Project.Name)
	}
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"myproject", "myproject"},
		{".docs", "_docs"},
		{"my.project.name", "my_project_name"},
		{".", "_"},
		{"sakusen", "sakusen"},
		{"no-dots-here", "no-dots-here"},
	}
	for _, tt := range tests {
		got := SanitizeProjectName(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeProjectName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestApplyDetectedProjectSanitizesDots(t *testing.T) {
	// Create a temp dir with a dot in the name to simulate a dotted project directory
	parent := t.TempDir()
	dir := filepath.Join(parent, ".my-project")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Project.AutoDetect = true
	cfg.ApplyDetectedProject(dir)

	if cfg.Project.Name != "_my-project" {
		t.Errorf("expected project name '_my-project', got %q", cfg.Project.Name)
	}
}

func TestProjectNameFromPath(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"/Users/me/dev/myproject", "myproject"},
		{"/Users/me/dev/.pai", "_pai"},
		{"/Users/me/dev/.docs", "_docs"},
		{"/tmp/my.project", "my_project"},
		{"sakusen", "sakusen"},
		// Trailing slashes should be ignored by filepath.Base.
		{"/Users/me/dev/.pai/", "_pai"},
	}
	for _, tt := range tests {
		got := ProjectNameFromPath(tt.input)
		if got != tt.expected {
			t.Errorf("ProjectNameFromPath(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDetectNodeProject(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := filepath.Join(dir, "package.json")
	content := `{"name": "@whop/monorepo", "scripts": {"test": "jest"}}`
	if err := os.WriteFile(pkgJSON, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	p := DetectProject(dir)
	if p.Type != ProjectTypeNode {
		t.Fatalf("expected Node project, got %s", p.Type)
	}
}
