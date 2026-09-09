package config

import (
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/git"
)

// ProjectRootKind says how FindProjectRoot chose the root.
type ProjectRootKind int

const (
	// ProjectRootNone means no project could be resolved: the start directory
	// is not inside a git repository and holds no .sakusen.yml.
	ProjectRootNone ProjectRootKind = iota
	// ProjectRootMarker means a .sakusen.yml was found at the returned root.
	ProjectRootMarker
	// ProjectRootGitToplevel means no marker was found and the returned root
	// is the git toplevel.
	ProjectRootGitToplevel
)

// FindProjectRoot resolves the project root for start (normally os.Getwd()):
// the nearest ancestor of start containing a .sakusen.yml, with the walk
// bounded by the git toplevel (which is itself checked), falling back to the
// git toplevel when no marker is found.
//
// The walk never crosses the toplevel: ~/.sakusen.yml is the global config
// file, so ascending past a repo boundary would register the home directory as
// a project. Outside a git repository only start itself is checked, for the
// same reason.
//
// Git errors are not surfaced — "not a git repository" is a normal outcome.
// The only error returned is a failure to make start absolute.
func FindProjectRoot(start string) (root string, kind ProjectRootKind, err error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", ProjectRootNone, err
	}

	top, gitErr := git.GetRepoRoot(abs)
	if gitErr != nil {
		if hasMarker(abs) {
			return abs, ProjectRootMarker, nil
		}
		return "", ProjectRootNone, nil
	}

	for dir := abs; ; {
		if hasMarker(dir) {
			return dir, ProjectRootMarker, nil
		}
		if sameDir(dir, top) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return top, ProjectRootGitToplevel, nil
}

func hasMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".sakusen.yml"))
	return err == nil
}

// sameDir reports whether a and b are the same directory. It compares inodes
// rather than strings because `git rev-parse --show-toplevel` returns a
// symlink-resolved path while os.Getwd() may not (macOS /tmp vs /private/tmp),
// and a string comparison would walk straight past the toplevel.
func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return a == b
	}
	bi, err := os.Stat(b)
	if err != nil {
		return a == b
	}
	return os.SameFile(ai, bi)
}
