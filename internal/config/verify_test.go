package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitVerifyDefaults(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.VerifyCommand != "" {
		t.Errorf("Git.VerifyCommand = %q, want empty", cfg.Git.VerifyCommand)
	}
	if cfg.GitVerifyTimeout != DefaultGitVerifyTimeout {
		t.Errorf("GitVerifyTimeout = %v, want %v", cfg.GitVerifyTimeout, DefaultGitVerifyTimeout)
	}
}

func TestGitVerifyParses(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	projectDir := writeProject(t, "git:\n  verify_command: mise run check\n  verify_timeout: 20m\n")
	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.VerifyCommand != "mise run check" {
		t.Errorf("Git.VerifyCommand = %q, want %q", cfg.Git.VerifyCommand, "mise run check")
	}
	if cfg.GitVerifyTimeout != 20*time.Minute {
		t.Errorf("GitVerifyTimeout = %v, want 20m", cfg.GitVerifyTimeout)
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err != nil {
		t.Errorf("git verify keys must validate, got %v", err)
	}
}

// Per key, like the other git: keys: the project overrides what it sets and
// inherits the rest from ~/.sakusen.yml.
func TestGitVerifyTierOverride(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, "")
	writeGlobalSakusenYml(t, homeDir, "git:\n  verify_command: make test\n  verify_timeout: 1h\n")

	cfg, err := LoadForProject(writeProject(t, "git:\n  verify_command: mise run check\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.VerifyCommand != "mise run check" {
		t.Errorf("Git.VerifyCommand = %q, want project value", cfg.Git.VerifyCommand)
	}
	if cfg.GitVerifyTimeout != time.Hour {
		t.Errorf("GitVerifyTimeout = %v, want 1h inherited", cfg.GitVerifyTimeout)
	}

	cfg, err = LoadForProject(writeProject(t, "git:\n  verify_timeout: 0\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.VerifyCommand != "make test" {
		t.Errorf("Git.VerifyCommand = %q, want inherited %q", cfg.Git.VerifyCommand, "make test")
	}
	if cfg.GitVerifyTimeout != 0 {
		t.Errorf("GitVerifyTimeout = %v, want 0 from project", cfg.GitVerifyTimeout)
	}
}

func TestGitVerifyTimeoutInvalidFailsLoad(t *testing.T) {
	for _, value := range []string{"soon", "-1m"} {
		t.Run(value, func(t *testing.T) {
			writeGlobalConfigYaml(t, "")
			projectDir := writeProject(t, "git:\n  verify_timeout: "+value+"\n")

			if _, err := LoadForProject(projectDir); err == nil || !strings.Contains(err.Error(), "verify_timeout") {
				t.Errorf("LoadForProject error = %v, want it to mention verify_timeout", err)
			}
			if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err == nil || !strings.Contains(err.Error(), "verify_timeout") {
				t.Errorf("ValidateFile error = %v, want it to mention verify_timeout", err)
			}
		})
	}
}

func TestGitVerifyParentMergesDefaultsTrueAndMergesPerTier(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if !cfg.GitVerifyParentMerges {
		t.Error("GitVerifyParentMerges = false, want default true")
	}

	writeGlobalSakusenYml(t, homeDir, "git:\n  verify_parent_merges: false\n")
	cfg, err = LoadForProject(writeProject(t, "git:\n  verify_command: make test\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.GitVerifyParentMerges {
		t.Error("GitVerifyParentMerges = true, want false inherited from the global tier")
	}

	projectDir := writeProject(t, "git:\n  verify_parent_merges: true\n")
	cfg, err = LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if !cfg.GitVerifyParentMerges {
		t.Error("GitVerifyParentMerges = false, want project's explicit true to win")
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err != nil {
		t.Errorf("verify_parent_merges must validate, got %v", err)
	}
}
