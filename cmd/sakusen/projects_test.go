package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

func TestResolveProjectRef(t *testing.T) {
	dir := t.TempDir()
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(canon, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(canon, "gone", "deeper")
	projects := []daemon.ProjectInfo{
		{ID: 3, Path: canon},
		{ID: 7, Path: missing},
	}

	for _, tc := range []struct {
		ref    string
		wantID int64
	}{
		{"3", 3},
		{"7", 7},
		{canon, 3},
		{link, 3},
		{missing, 7},
		{filepath.Join(link, "gone", "deeper"), 7},
	} {
		p, err := resolveProjectRef(projects, tc.ref)
		if err != nil {
			t.Errorf("resolveProjectRef(%q): %v", tc.ref, err)
			continue
		}
		if p.ID != tc.wantID {
			t.Errorf("resolveProjectRef(%q) = #%d, want #%d", tc.ref, p.ID, tc.wantID)
		}
	}

	for _, ref := range []string{"9", filepath.Join(t.TempDir(), "unknown")} {
		if p, err := resolveProjectRef(projects, ref); err == nil {
			t.Errorf("resolveProjectRef(%q) = #%d, want error", ref, p.ID)
		}
	}
}

func TestProjectsCmd_NoProjectConfigRequired(t *testing.T) {
	for _, c := range []string{"projects", "list", "remove"} {
		cmd := projectsCmd
		if c != "projects" {
			sub, _, err := projectsCmd.Find([]string{c})
			if err != nil {
				t.Fatal(err)
			}
			cmd = sub
		}
		if !isSubcommandOf(cmd, "projects") {
			t.Errorf("%s is not exempt from the .sakusen.yml requirement", c)
		}
	}
}
