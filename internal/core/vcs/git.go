package vcs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/getgaal/gaal/internal/urlx"
)

// VcsGit implements the VCS interface for Git repositories using go-git
// (pure Go — no git binary required).
//
// Authentication: HTTPS public repositories work without configuration.
// Private repos (SSH or authenticated HTTPS) are not yet supported.
type VcsGit struct {
	// Shallow requests a depth-1 clone (suitable for skill caches that never
	// need history). Update will hard-reset to origin/HEAD instead of pulling.
	Shallow bool
}

func (g *VcsGit) depth() int {
	if g.Shallow {
		return 1
	}
	return 0
}

func (g *VcsGit) Clone(ctx context.Context, url, path, version string) error {
	if err := urlx.ValidateRepoURL(url); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating parent directory: %w", err)
	}

	safeURL := urlx.Redact(url)
	slog.DebugContext(ctx, "cloning", "url", safeURL, "path", shortPath(path), "version", version)

	r, err := gogit.PlainCloneContext(ctx, path, false, &gogit.CloneOptions{
		URL:               url,
		RecurseSubmodules: gogit.NoRecurseSubmodules,
		Tags:              gogit.AllTags,
		Depth:             g.depth(),
	})
	if err != nil {
		return fmt.Errorf("cloning %s: %w", safeURL, err)
	}

	if version != "" {
		if err := checkoutVersion(r, version); err != nil {
			return err
		}
	}
	// go-git's recursion depth counts descendants below each direct submodule.
	return cloneGitSubmodules(ctx, r, gogit.DefaultSubmoduleRecursionDepth+1)
}

func cloneGitSubmodules(ctx context.Context, r *gogit.Repository, remaining gogit.SubmoduleRescursivity) error {
	slog.DebugContext(ctx, "cloning validated submodules", "remaining_depth", remaining)
	w, err := r.Worktree()
	if err != nil {
		return err
	}
	submodules, err := w.Submodules()
	if err != nil {
		return fmt.Errorf("reading submodules: %w", err)
	}
	for _, sub := range submodules {
		cfg := sub.Config()
		if err := urlx.ValidateRepoURL(cfg.URL); err != nil {
			return fmt.Errorf("submodule %q: %w", cfg.Path, err)
		}
		// Reject absolute paths from untrusted metadata, including foreign Windows paths.
		if filepath.IsAbs(cfg.URL) || strings.HasPrefix(cfg.URL, `\`) || (len(cfg.URL) >= 2 && cfg.URL[1] == ':') {
			return fmt.Errorf("submodule %q: absolute local URLs are not allowed", cfg.Path)
		}
		if remaining == 0 {
			return fmt.Errorf("submodule %q: recursion limit exceeded", cfg.Path)
		}
		if err := sub.UpdateContext(ctx, &gogit.SubmoduleUpdateOptions{
			Init:              true,
			RecurseSubmodules: gogit.NoRecurseSubmodules,
		}); err != nil {
			return fmt.Errorf("updating submodule %q: %w", cfg.Path, err)
		}
		child, err := sub.Repository()
		if err != nil {
			return fmt.Errorf("opening submodule %q: %w", cfg.Path, err)
		}
		if err := cloneGitSubmodules(ctx, child, remaining-1); err != nil {
			return fmt.Errorf("submodule %q: %w", cfg.Path, err)
		}
	}
	return nil
}

func (g *VcsGit) Update(ctx context.Context, url, path, version string) error {
	r, err := gogit.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("opening repository at %s: %w", shortPath(path), err)
	}

	if url != "" {
		if remote, rerr := r.Remote("origin"); rerr == nil && len(remote.Config().URLs) > 0 {
			actual := remote.Config().URLs[0]
			if normalizeGitURL(actual) != normalizeGitURL(url) {
				return &RemoteURLMismatchError{
					Path:          path,
					ConfiguredURL: url,
					RemoteURL:     actual,
				}
			}
		}
	}

	slog.DebugContext(ctx, "fetching", "path", shortPath(path))

	fetchErr := r.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: "origin",
		Tags:       gogit.AllTags,
		Force:      true,
		Prune:      true,
	})
	if fetchErr != nil && !errors.Is(fetchErr, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetching: %w", fetchErr)
	}

	if version != "" {
		return checkoutVersion(r, version)
	}

	if g.Shallow {
		// Shallow / cache mode: hard-reset to remote HEAD so force-pushes and
		// history rewrites are always reflected.
		return g.resetToRemoteHEAD(ctx, r)
	}

	// No pinned version: fast-forward the current tracking branch.
	w, err := r.Worktree()
	if err != nil {
		return err
	}
	if err = w.PullContext(ctx, &gogit.PullOptions{
		RemoteName: "origin",
		Force:      true,
	}); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("pulling: %w", err)
	}
	return nil
}

// resetToRemoteHEAD hard-resets the working tree to the tip of origin's default
// branch. It tries refs/remotes/origin/HEAD first, then falls back to
// origin/main and origin/master.
func (g *VcsGit) resetToRemoteHEAD(ctx context.Context, r *gogit.Repository) error {
	var hash plumbing.Hash
	var found bool
	for _, refName := range []string{"HEAD", "main", "master"} {
		ref, err := r.Reference(plumbing.NewRemoteReferenceName("origin", refName), true)
		if err == nil {
			hash = ref.Hash()
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("could not resolve origin HEAD")
	}

	w, err := r.Worktree()
	if err != nil {
		return err
	}
	slog.DebugContext(ctx, "hard-resetting to remote HEAD", "hash", hash)
	return w.Reset(&gogit.ResetOptions{
		Commit: hash,
		Mode:   gogit.HardReset,
	})
}

func (g *VcsGit) IsCloned(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func (g *VcsGit) CurrentVersion(_ context.Context, path string) (string, error) {
	r, err := gogit.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("opening repository: %w", err)
	}

	head, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("reading HEAD: %w", err)
	}

	// Prefer a tag name when HEAD coincides with a tag.
	if tag := tagAtCommit(r, head.Hash()); tag != "" {
		return tag, nil
	}

	// Return branch name or short hash (detached HEAD).
	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}
	h := head.Hash().String()
	if len(h) > 8 {
		h = h[:8]
	}
	return h, nil
}

// HasChanges reports whether the working tree contains modifications to
// tracked files (staged or unstaged). Untracked files are ignored.
func (g *VcsGit) HasChanges(ctx context.Context, path string) (bool, error) {
	slog.DebugContext(ctx, "checking for local changes", "path", shortPath(path))

	r, err := gogit.PlainOpen(path)
	if err != nil {
		return false, fmt.Errorf("opening repository: %w", err)
	}
	w, err := r.Worktree()
	if err != nil {
		return false, fmt.Errorf("reading worktree: %w", err)
	}
	st, err := w.Status()
	if err != nil {
		return false, fmt.Errorf("reading status: %w", err)
	}
	return !st.IsClean(), nil
}

// checkoutVersion resolves version (branch, tag, or commit hash) inside r
// and checks it out. Resolution order: local branch → remote branch → tag → commit hash.
func checkoutVersion(r *gogit.Repository, version string) error {
	w, err := r.Worktree()
	if err != nil {
		return err
	}

	// 1. Local branch.
	if err = w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(version),
		Force:  true,
	}); err == nil {
		return nil
	}

	// 2. Remote-tracking branch — create a local branch pointing at origin/<version>.
	if ref, e := r.Reference(plumbing.NewRemoteReferenceName("origin", version), true); e == nil {
		return w.Checkout(&gogit.CheckoutOptions{
			Hash:   ref.Hash(),
			Branch: plumbing.NewBranchReferenceName(version),
			Create: true,
			Force:  true,
		})
	}

	// 3. Tag.
	if err = w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewTagReferenceName(version),
		Force:  true,
	}); err == nil {
		return nil
	}

	// 4. Commit hash.
	if hash := plumbing.NewHash(version); !hash.IsZero() {
		return w.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: true})
	}

	return fmt.Errorf("could not resolve %q as a branch, tag, or commit", version)
}

// errTagFound is used as a sentinel to break out of go-git's ForEach iterator.
var errTagFound = errors.New("tag found")

// tagAtCommit returns the short name of the first tag (lightweight or annotated)
// that points to hash, or "" if none is found.
func tagAtCommit(r *gogit.Repository, hash plumbing.Hash) string {
	tags, err := r.Tags()
	if err != nil {
		return ""
	}
	var found string
	_ = tags.ForEach(func(ref *plumbing.Reference) error {
		// Lightweight tag: the ref hash is the commit hash directly.
		if ref.Hash() == hash {
			found = ref.Name().Short()
			return errTagFound
		}
		// Annotated tag: the ref points to a tag object; dereference to get the commit.
		if obj, e := r.TagObject(ref.Hash()); e == nil && obj.Target == hash {
			found = ref.Name().Short()
			return errTagFound
		}
		return nil
	})
	return found
}
