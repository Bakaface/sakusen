package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMergeConfigDefaults(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.MergeCleanTargetTimeout != 0 {
		t.Errorf("MergeCleanTargetTimeout = %v, want 0 (wait forever)", cfg.MergeCleanTargetTimeout)
	}
	if len(cfg.MergeIgnorePaths) != 0 {
		t.Errorf("MergeIgnorePaths = %v, want empty", cfg.MergeIgnorePaths)
	}
	if cfg.Notifications.OnMergeBlocked {
		t.Error("Notifications.OnMergeBlocked defaults to true, want false")
	}
}

func TestMergeConfigParses(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	projectDir := writeProject(t, `merge:
  clean_target_timeout: 30m
  ignore_paths:
    - "scratch/**"
    - "*.tmp"
notifications:
  enabled: true
  on_merge_blocked: true
`)
	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.MergeCleanTargetTimeout != 30*time.Minute {
		t.Errorf("MergeCleanTargetTimeout = %v, want 30m", cfg.MergeCleanTargetTimeout)
	}
	if want := []string{"scratch/**", "*.tmp"}; !slices.Equal(cfg.MergeIgnorePaths, want) {
		t.Errorf("MergeIgnorePaths = %v, want %v", cfg.MergeIgnorePaths, want)
	}
	if !cfg.Notifications.OnMergeBlocked {
		t.Error("Notifications.OnMergeBlocked = false, want true")
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err != nil {
		t.Errorf("merge: block must validate, got %v", err)
	}
}

// Per-field merge: a project setting only ignore_paths keeps the global
// timeout, and a project timeout overrides the global one.
func TestMergeConfigTierOverride(t *testing.T) {
	writeGlobalConfigYaml(t, "merge:\n  clean_target_timeout: 1h\n  ignore_paths: [\"global/**\"]\n")

	cfg, err := LoadForProject(writeProject(t, "merge:\n  ignore_paths: [\"local/**\"]\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.MergeCleanTargetTimeout != time.Hour {
		t.Errorf("MergeCleanTargetTimeout = %v, want 1h inherited", cfg.MergeCleanTargetTimeout)
	}
	if want := []string{"local/**"}; !slices.Equal(cfg.MergeIgnorePaths, want) {
		t.Errorf("MergeIgnorePaths = %v, want %v", cfg.MergeIgnorePaths, want)
	}

	writeGlobalConfigYaml(t, "merge:\n  clean_target_timeout: 1h\n  ignore_paths: [\"global/**\"]\n")
	cfg, err = LoadForProject(writeProject(t, "merge:\n  clean_target_timeout: 0\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.MergeCleanTargetTimeout != 0 {
		t.Errorf("MergeCleanTargetTimeout = %v, want 0 from project", cfg.MergeCleanTargetTimeout)
	}
	if want := []string{"global/**"}; !slices.Equal(cfg.MergeIgnorePaths, want) {
		t.Errorf("MergeIgnorePaths = %v, want %v inherited", cfg.MergeIgnorePaths, want)
	}
}

func TestMergeConfigInvalidValuesFailLoad(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want string
	}{
		{"unparseable duration", "merge:\n  clean_target_timeout: soon\n", "clean_target_timeout"},
		{"negative duration", "merge:\n  clean_target_timeout: -1m\n", "clean_target_timeout"},
		{"empty ignore pattern", "merge:\n  ignore_paths: [\"docs/**\", \"\"]\n", "ignore_paths"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeGlobalConfigYaml(t, "")
			projectDir := writeProject(t, tc.yml)

			if _, err := LoadForProject(projectDir); err == nil {
				t.Fatal("expected LoadForProject to fail")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("LoadForProject error = %v, want it to mention %q", err, tc.want)
			}

			if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err == nil {
				t.Error("expected `sakusen validate` to reject the same value")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateFile error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
