package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitCommitMessageDefaults(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.CommitMessageCommand != "" {
		t.Errorf("Git.CommitMessageCommand = %q, want empty", cfg.Git.CommitMessageCommand)
	}
	if cfg.GitCommitMessageTimeout != DefaultGitCommitMessageTimeout {
		t.Errorf("GitCommitMessageTimeout = %v, want %v", cfg.GitCommitMessageTimeout, DefaultGitCommitMessageTimeout)
	}
}

// Per key, like the other git: keys: the project overrides what it sets and
// inherits the rest from ~/.sakusen.yml.
func TestGitCommitMessageTierOverride(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, "")
	writeGlobalSakusenYml(t, homeDir, "git:\n  commit_message_command: global-msg\n  commit_message_timeout: 30s\n")

	projectDir := writeProject(t, "git:\n  commit_message_command: project-msg\n")
	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.CommitMessageCommand != "project-msg" {
		t.Errorf("Git.CommitMessageCommand = %q, want project value", cfg.Git.CommitMessageCommand)
	}
	if cfg.GitCommitMessageTimeout != 30*time.Second {
		t.Errorf("GitCommitMessageTimeout = %v, want 30s inherited", cfg.GitCommitMessageTimeout)
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err != nil {
		t.Errorf("commit message keys must validate, got %v", err)
	}

	cfg, err = LoadForProject(writeProject(t, "git:\n  commit_message_timeout: 5m\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Git.CommitMessageCommand != "global-msg" {
		t.Errorf("Git.CommitMessageCommand = %q, want inherited %q", cfg.Git.CommitMessageCommand, "global-msg")
	}
	if cfg.GitCommitMessageTimeout != 5*time.Minute {
		t.Errorf("GitCommitMessageTimeout = %v, want 5m from project", cfg.GitCommitMessageTimeout)
	}
}

func TestGitCommitMessageTimeoutInvalidFailsLoad(t *testing.T) {
	for _, value := range []string{"soon", "-1m", "0"} {
		t.Run(value, func(t *testing.T) {
			writeGlobalConfigYaml(t, "")
			projectDir := writeProject(t, "git:\n  commit_message_timeout: "+value+"\n")

			if _, err := LoadForProject(projectDir); err == nil || !strings.Contains(err.Error(), "commit_message_timeout") {
				t.Errorf("LoadForProject error = %v, want it to mention commit_message_timeout", err)
			}
			if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err == nil || !strings.Contains(err.Error(), "commit_message_timeout") {
				t.Errorf("ValidateFile error = %v, want it to mention commit_message_timeout", err)
			}
		})
	}
}
