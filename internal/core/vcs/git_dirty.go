package vcs

import (
	"context"
	"fmt"
	"log/slog"
)

func (g *VcsGit) CheckUpdate(ctx context.Context, path string) error {
	slog.DebugContext(ctx, "checking update eligibility", "path", shortPath(path), "shallow", g.Shallow)
	if g.Shallow {
		return nil
	}
	dirty, err := g.HasChanges(ctx, path)
	if err != nil {
		return err
	}
	if dirty {
		return &DirtyWorktreeError{Path: path}
	}
	return nil
}

// DirtyWorktreeError reports an update refused to preserve local changes.
type DirtyWorktreeError struct {
	Path string
}

func (e *DirtyWorktreeError) Error() string {
	slog.Debug("reporting dirty worktree", "path", shortPath(e.Path))
	return fmt.Sprintf("repository %s has uncommitted local changes; refusing to update — commit, stash, or move them first", shortPath(e.Path))
}
