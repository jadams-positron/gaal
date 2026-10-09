package vcs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

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
		RecurseSubmodules: gogit.DefaultSubmoduleRecursionDepth,
		Tags:              gogit.AllTags,
		Depth:             g.depth(),
	})
	if err != nil {
		return fmt.Errorf("cloning %s: %w", safeURL, err)
	}

	if version != "" {
		return g.checkoutVersion(r, version)
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

	if err := g.CheckUpdate(ctx, path); err != nil {
		return err
	}

	slog.DebugContext(ctx, "fetching", "path", shortPath(path))

	options := &gogit.FetchOptions{
		RemoteName: "origin",
		Tags:       gogit.AllTags,
		Force:      true,
		Prune:      true,
	}
	if version == "" && !g.Shallow {
		remote, err := r.Remote("origin")
		if err != nil {
			return err
		}
		options.RefSpecs = append([]gitconfig.RefSpec(nil), remote.Config().Fetch...)
		options.RefSpecs = append(options.RefSpecs, "+HEAD:refs/remotes/origin/HEAD")
	}
	fetchErr := r.FetchContext(ctx, options)
	if fetchErr != nil && !errors.Is(fetchErr, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetching: %w", fetchErr)
	}

	if err := g.CheckUpdate(ctx, path); err != nil {
		return err
	}
	if version != "" {
		return g.checkoutVersion(r, version)
	}

	if g.Shallow {
		// Shallow / cache mode: hard-reset to remote HEAD so force-pushes and
		// history rewrites are always reflected.
		return g.resetToRemoteHEAD(ctx, r)
	}

	ref, err := r.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), true)
	if err != nil {
		return err
	}
	return fastForward(r, ref)
}

func fastForward(r *gogit.Repository, ref *plumbing.Reference) error {
	slog.Debug("fast-forwarding", "ref", ref.Name(), "hash", ref.Hash())
	w, err := r.Worktree()
	if err != nil {
		return err
	}
	if err := r.Merge(*ref, gogit.MergeOptions{Strategy: gogit.FastForwardMerge}); err != nil {
		return fmt.Errorf("fast-forwarding to %s: %w", ref.Name(), err)
	}
	return w.Reset(&gogit.ResetOptions{Commit: ref.Hash(), Mode: gogit.MergeReset})
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
// tracked files (staged or unstaged) or untracked files, including ignored files.
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
	// Ignored files also need protection from checkout/reset.
	w.Excludes = append(w.Excludes, gitignore.ParsePattern("!**", nil))
	st, err := w.Status()
	if err != nil {
		return false, fmt.Errorf("reading status: %w", err)
	}
	return !st.IsClean(), nil
}

// checkoutVersion resolves version (branch, tag, or commit hash) inside r
// and checks it out. Resolution order: local branch → remote branch → tag → commit hash.
func (g *VcsGit) checkoutVersion(r *gogit.Repository, version string) error {
	slog.Debug("checking out version", "version", version, "shallow", g.Shallow)
	w, err := r.Worktree()
	if err != nil {
		return err
	}

	// 1. Local branch.
	if err = w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(version),
		Force:  g.Shallow,
	}); err == nil {
		if g.Shallow {
			return nil
		}
		ref, err := r.Reference(plumbing.NewRemoteReferenceName("origin", version), true)
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return fastForward(r, ref)
	}

	// 2. Remote-tracking branch — create a local branch pointing at origin/<version>.
	if ref, e := r.Reference(plumbing.NewRemoteReferenceName("origin", version), true); e == nil {
		return w.Checkout(&gogit.CheckoutOptions{
			Hash:   ref.Hash(),
			Branch: plumbing.NewBranchReferenceName(version),
			Create: true,
			Force:  g.Shallow,
		})
	}

	// 3. Tag.
	if err = w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewTagReferenceName(version),
		Force:  g.Shallow,
	}); err == nil {
		return nil
	}

	// 4. Commit hash.
	if hash := plumbing.NewHash(version); !hash.IsZero() {
		return w.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: g.Shallow})
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
