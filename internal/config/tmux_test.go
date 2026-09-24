package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeGlobalSakusenYml writes a ~/.sakusen.yml under an isolated HOME, the
// middle tier between the XDG config.yaml and the project file.
func writeGlobalSakusenYml(t *testing.T, homeDir, yml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(homeDir, ".sakusen.yml"), []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeProject writes a project .sakusen.yml into a fresh temp dir and
// returns the dir.
func writeProject(t *testing.T, yml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Hibernation is on by default: a config that never mentions tmux: must still
// resolve to the built-in threshold and cap.
func TestTmuxHibernationDefaults(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.TmuxHibernateAfter != 4*time.Hour {
		t.Errorf("TmuxHibernateAfter = %v, want 4h", cfg.TmuxHibernateAfter)
	}
	if cfg.TmuxMaxLiveSessions != 12 {
		t.Errorf("TmuxMaxLiveSessions = %d, want 12", cfg.TmuxMaxLiveSessions)
	}
}

// Later tiers override earlier ones, and per field: ~/.sakusen.yml setting
// only hibernate_after must leave the XDG tier's cap in place.
func TestTmuxHibernationTierOverride(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, "tmux:\n  hibernate_after: 1h\n  max_live_sessions: 5\n")
	writeGlobalSakusenYml(t, homeDir, "tmux:\n  hibernate_after: 30m\n")

	cfg, err := LoadForProject(writeProject(t, "max_workers: 2\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.TmuxHibernateAfter != 30*time.Minute {
		t.Errorf("TmuxHibernateAfter = %v, want 30m from ~/.sakusen.yml", cfg.TmuxHibernateAfter)
	}
	if cfg.TmuxMaxLiveSessions != 5 {
		t.Errorf("TmuxMaxLiveSessions = %d, want 5 preserved from the config.yaml tier", cfg.TmuxMaxLiveSessions)
	}
}

// An explicit 0 disables each feature — the whole point of max_live_sessions
// being a pointer, since 0 is otherwise indistinguishable from "unset".
func TestTmuxHibernationExplicitZeroDisables(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	cfg, err := LoadForProject(writeProject(t, "tmux:\n  hibernate_after: 0\n  max_live_sessions: 0\n"))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.TmuxHibernateAfter != 0 {
		t.Errorf("TmuxHibernateAfter = %v, want 0 (disabled)", cfg.TmuxHibernateAfter)
	}
	if cfg.TmuxMaxLiveSessions != 0 {
		t.Errorf("TmuxMaxLiveSessions = %d, want 0 (disabled)", cfg.TmuxMaxLiveSessions)
	}
}

func TestTmuxHibernationInvalidValuesFailLoad(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want string
	}{
		{"unparseable duration", "tmux:\n  hibernate_after: soon\n", "hibernate_after"},
		{"negative duration", "tmux:\n  hibernate_after: -1h\n", "hibernate_after"},
		{"negative cap", "tmux:\n  max_live_sessions: -1\n", "max_live_sessions"},
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

// Strict validation (KnownFields) must accept a TOP-LEVEL tmux: block while
// still rejecting tmux inside a step, where mode fields were removed.
func TestTmuxBlockStrictValidation(t *testing.T) {
	writeGlobalConfigYaml(t, "")

	ok := writeProject(t, `tmux:
  hibernate_after: 2h
  max_live_sessions: 4
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
`)
	if err := ValidateFile(filepath.Join(ok, ".sakusen.yml")); err != nil {
		t.Errorf("top-level tmux: must validate, got %v", err)
	}

	bad := writeProject(t, `workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
        tmux: true
`)
	if err := ValidateFile(filepath.Join(bad, ".sakusen.yml")); err == nil {
		t.Error("tmux: inside a step must still be rejected")
	}
}
