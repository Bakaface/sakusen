package workflow

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/config"
)

// SyncPathsToWorktree syncs the configured paths from srcRoot into dstRoot.
// Copy paths are fully copied (files and directories). Link paths are hard-linked
// so that Claude Code (which cannot follow symlinks outside its working directory)
// can read the files directly. Symlink sources are replicated as symlinks since
// macOS link(2) refuses to hard-link a symlink (EPERM).
//
// Failures on individual paths are collected and returned as a joined error
// so one bad entry does not prevent the rest from being synced.
func SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig) error {
	var errs []error
	for _, p := range paths.Copy {
		if err := copyPath(srcRoot, dstRoot, p); err != nil {
			errs = append(errs, err)
		}
	}
	for _, p := range paths.Link {
		if err := linkPath(srcRoot, dstRoot, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// copyPath copies a single path (file or directory) from srcRoot to dstRoot.
func copyPath(srcRoot, dstRoot, p string) error {
	srcPath := filepath.Join(srcRoot, p)
	dstPath := filepath.Join(dstRoot, p)

	info, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // skip missing paths silently
		}
		return fmt.Errorf("stat %s: %w", p, err)
	}

	if info.IsDir() {
		if err := syncDir(srcPath, dstPath); err != nil {
			return fmt.Errorf("sync dir %s: %w", p, err)
		}
	} else {
		if err := syncFile(srcPath, dstPath, info.Mode()); err != nil {
			return fmt.Errorf("sync file %s: %w", p, err)
		}
	}
	return nil
}

// linkPath creates hard links from srcRoot/p to dstRoot/p.
// Hard links are used instead of symlinks because Claude Code cannot follow
// symlinks that resolve to paths outside its working directory. Hard links
// share the same inode, so the file appears as a regular file within the worktree.
// For directories, the directory structure is recreated and individual files are hard-linked.
// When the source itself is a symlink, the symlink is replicated (read + recreate)
// rather than hard-linked: macOS link(2) returns EPERM on symlink sources.
// If the destination already exists (e.g. from the worktree checkout), it is removed first.
func linkPath(srcRoot, dstRoot, p string) error {
	srcPath := filepath.Join(srcRoot, p)
	dstPath := filepath.Join(dstRoot, p)

	// Verify source exists
	info, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // skip missing paths silently
		}
		return fmt.Errorf("stat %s: %w", p, err)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("mkdir for link %s: %w", p, err)
	}

	// Remove existing file/dir/symlink at destination
	if err := os.RemoveAll(dstPath); err != nil {
		return fmt.Errorf("remove existing %s: %w", p, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(srcPath)
		if err != nil {
			return fmt.Errorf("readlink %s: %w", p, err)
		}
		if err := os.Symlink(target, dstPath); err != nil {
			return fmt.Errorf("symlink %s: %w", p, err)
		}
		return nil
	}
	if info.IsDir() {
		return hardLinkDir(srcPath, dstPath)
	}
	if err := os.Link(srcPath, dstPath); err != nil {
		return fmt.Errorf("hard link %s: %w", p, err)
	}
	return nil
}

// hardLinkDir recreates the directory structure from src to dst and hard-links
// all individual files. This is needed because hard links cannot target directories.
// Symlinks encountered inside the tree are replicated as symlinks because macOS
// link(2) returns EPERM on symlink sources.
func hardLinkDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}

		return os.Link(path, target)
	})
}

// syncDir recursively copies a directory from src to dst, preserving permissions.
func syncDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode())
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		return syncFile(path, target, info.Mode())
	})
}

// syncFile copies a single file from src to dst with the given permissions.
func syncFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
