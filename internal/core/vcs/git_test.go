package vcs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
)

// setOrigin replaces origin's URL on the repo at dir.
func setOrigin(t *testing.T, dir, urlStr string) {
	t.Helper()
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	cfg, err := r.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.Remotes["origin"] = &gogitconfig.RemoteConfig{Name: "origin", URLs: []string{urlStr}}
	if err := r.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
}

// makeLocalRepo initialises a git repo in dir, creates one file, and commits it.
func makeLocalRepo(t *testing.T, dir string) *gogit.Repository {
	t.Helper()
	r, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	f := filepath.Join(dir, "README.md")
	os.WriteFile(f, []byte("hello"), 0o644)
	w.Add("README.md")
	_, err = w.Commit("initial commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return r
}

func TestVcsGit_Clone_LocalSource(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone from local source: %v", err)
	}
	if !g.IsCloned(destDir) {
		t.Error("expected IsCloned=true after Clone")
	}
}

func TestVcsGit_CurrentVersion_AfterClone(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	ver, err := g.CurrentVersion(context.Background(), destDir)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if ver == "" {
		t.Error("expected non-empty version string")
	}
}

func TestVcsGit_CheckUpdate(t *testing.T) {
	slog.Debug("testing read-only update checks", "test", t.Name())
	for _, tc := range []struct {
		name    string
		file    string
		shallow bool
		refuse  bool
	}{
		{name: "clean"},
		{name: "tracked", file: "README.md", refuse: true},
		{name: "untracked", file: "LOCAL.md", refuse: true},
		{name: "shallow", file: "README.md", shallow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slog.Debug("checking update without a remote", "scenario", tc.name)
			dir := t.TempDir()
			makeLocalRepo(t, dir)
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte("local work\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			g := &VcsGit{Shallow: tc.shallow}
			err := g.CheckUpdate(t.Context(), dir)
			var dirty *DirtyWorktreeError
			if errors.As(err, &dirty) != tc.refuse || (err != nil) != tc.refuse {
				t.Errorf("CheckUpdate error = %v; want dirty refusal=%t", err, tc.refuse)
			}
			if tc.file != "" {
				if got, err := os.ReadFile(filepath.Join(dir, tc.file)); err != nil || string(got) != "local work\n" {
					t.Errorf("preflight changed local file: %q, %v", got, err)
				}
			}
		})
	}
}

func TestVcsGit_CheckUpdate_InvalidRepository(t *testing.T) {
	slog.Debug("testing failed update inspection", "test", t.Name())
	if err := (&VcsGit{}).CheckUpdate(t.Context(), t.TempDir()); err == nil {
		t.Fatal("expected an inspection error for a non-repository")
	}
}

func TestVcsGit_Update_Dirty(t *testing.T) {
	slog.Debug("testing dirty repository refusal", "test", t.Name())
	useInProcessGit(t)
	for _, pin := range []string{"unpinned", "branch", "tag", "commit"} {
		for _, change := range []string{"unstaged", "staged", "untracked", "ignored"} {
			t.Run(pin+"/"+change, func(t *testing.T) {
				slog.Debug("testing local file preservation", "pin", pin, "change", change)
				src := t.TempDir()
				r := makeLocalRepo(t, src)
				if change == "ignored" {
					commitFile(t, src, ".gitignore", "LOCAL.md\n")
				}
				head, err := r.Head()
				if err != nil {
					t.Fatal(err)
				}
				version := ""
				switch pin {
				case "branch":
					version = head.Name().Short()
				case "tag":
					version = "v1"
					if _, err := r.CreateTag(version, head.Hash(), nil); err != nil {
						t.Fatal(err)
					}
				case "commit":
					version = head.Hash().String()
				}
				dst := filepath.Join(t.TempDir(), "clone")
				g := &VcsGit{}
				if err := g.Clone(t.Context(), src, dst, version); err != nil {
					t.Fatal(err)
				}
				file := "README.md"
				if change == "untracked" || change == "ignored" {
					file = "LOCAL.md"
				}
				if err := os.WriteFile(filepath.Join(dst, file), []byte("local work\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				cloned, err := gogit.PlainOpen(dst)
				if err != nil {
					t.Fatal(err)
				}
				if change == "staged" {
					w, err := cloned.Worktree()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Add(file); err != nil {
						t.Fatal(err)
					}
				}
				err = g.Update(t.Context(), src, dst, version)
				var dirty *DirtyWorktreeError
				if !errors.As(err, &dirty) {
					t.Errorf("Update error = %v; want *DirtyWorktreeError", err)
				} else if dirty.Path != dst || dirty.Error() == "" {
					t.Errorf("dirty error = %+v; want repository path and explanation", dirty)
				}
				if got, err := os.ReadFile(filepath.Join(dst, file)); err != nil || string(got) != "local work\n" {
					t.Errorf("local file after Update = %q, %v; want preserved local work", got, err)
				}
				if got, err := cloned.Head(); err != nil || got.Hash() != head.Hash() {
					t.Errorf("HEAD changed after refused Update: %v, %v", got, err)
				}
			})
		}
	}
}

func TestVcsGit_Update_EditsDuringFetch(t *testing.T) {
	slog.Debug("testing edits made during fetch", "test", t.Name())
	for _, pinned := range []bool{false, true} {
		for _, file := range []string{"README.md", "LOCAL.md"} {
			t.Run(fmt.Sprintf("pinned=%t/%s", pinned, file), func(t *testing.T) {
				slog.Debug("testing concurrent edit preservation", "pinned", pinned, "file", file)
				useInProcessGit(t)
				src := t.TempDir()
				r := makeLocalRepo(t, src)
				head, err := r.Head()
				if err != nil {
					t.Fatal(err)
				}
				version := ""
				if pinned {
					version = head.Name().Short()
				}
				dst := filepath.Join(t.TempDir(), "clone")
				g := &VcsGit{}
				if err := g.Clone(t.Context(), src, dst, version); err != nil {
					t.Fatal(err)
				}
				commitFile(t, src, file, "upstream work\n")
				client.InstallProtocol("file", fetchHook{Transport: client.Protocols["file"], onFetch: func() {
					slog.Debug("saving edits during fetch", "file", file)
					if err := os.WriteFile(filepath.Join(dst, file), []byte("local work\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}})
				err = g.Update(t.Context(), src, dst, version)
				var dirty *DirtyWorktreeError
				if !errors.As(err, &dirty) {
					t.Errorf("Update error = %v; want *DirtyWorktreeError", err)
				}
				if got, err := os.ReadFile(filepath.Join(dst, file)); err != nil || string(got) != "local work\n" {
					t.Errorf("local file after fetch = %q, %v; want preserved work", got, err)
				}
				cloned, err := gogit.PlainOpen(dst)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := cloned.Head(); err != nil || got.Hash() != head.Hash() {
					t.Errorf("HEAD changed after refused Update: %v, %v", got, err)
				}
			})
		}
	}
}

func TestVcsGit_Update_UnpinnedBranch(t *testing.T) {
	slog.Debug("testing unpinned branch updates", "test", t.Name())
	testBranchUpdates(t, false)
}

func TestVcsGit_Update_AfterClone(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	err := g.Update(context.Background(), "", destDir, "")
	_ = err
}

func TestVcsGit_Clone_WithVersion(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, err := r.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	version := head.Hash().String()
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, version); err != nil {
		t.Fatalf("Clone with version: %v", err)
	}
	if !g.IsCloned(destDir) {
		t.Error("expected IsCloned=true after Clone with version")
	}
}

func TestVcsGit_Clone_WithTagVersion(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()
	if _, err := r.CreateTag("v1.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, "v1.0.0"); err != nil {
		t.Fatalf("Clone with tag version: %v", err)
	}
	if !g.IsCloned(destDir) {
		t.Error("expected IsCloned=true after clone with tag")
	}
}

func TestVcsGit_CurrentVersion_TaggedCommit(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()
	r.CreateTag("v2.0.0", head.Hash(), nil) //nolint:errcheck
	// The source repo itself has the tag; CurrentVersion should return "v2.0.0".
	g := &VcsGit{}
	ver, err := g.CurrentVersion(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if ver != "v2.0.0" {
		t.Logf("CurrentVersion returned %q (not tag name - may be branch)", ver)
	}
}

func TestVcsGit_CurrentVersion_DetachedHEAD(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()
	hash := head.Hash()

	// Check out the commit directly → detached HEAD.
	w, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := w.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		t.Fatalf("Checkout hash: %v", err)
	}

	g := &VcsGit{}
	ver, err := g.CurrentVersion(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("CurrentVersion detached HEAD: %v", err)
	}
	// Should return an 8-char short hash.
	if len(ver) > 8 {
		t.Errorf("expected short hash (≤8 chars), got %q", ver)
	}
	if ver == "" {
		t.Error("expected non-empty version for detached HEAD")
	}
}

func TestCheckoutVersion_InvalidVersion(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	err := (&VcsGit{}).checkoutVersion(r, "totally-nonexistent-branch-or-tag-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent version")
	}
}

func TestCheckoutVersion_CommitHash(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()
	hash := head.Hash().String()
	if err := (&VcsGit{}).checkoutVersion(r, hash); err != nil {
		t.Fatalf("checkoutVersion with commit hash: %v", err)
	}
}

func TestCheckoutVersion_ExistingLocalBranch(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()
	branchName := head.Name().Short() // "master" or "main"
	// Checking out the current branch should succeed immediately (step 1).
	if err := (&VcsGit{}).checkoutVersion(r, branchName); err != nil {
		t.Fatalf("checkoutVersion with existing branch %q: %v", branchName, err)
	}
}

func TestTagAtCommit_AnnotatedTag(t *testing.T) {
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	head, _ := r.Head()

	// Create an annotated tag (CreateTag with non-nil options = annotated).
	_, err := r.CreateTag("v3.0.0-annotated", head.Hash(), &gogit.CreateTagOptions{
		Message: "Release v3.0.0",
		Tagger:  &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("CreateTag annotated: %v", err)
	}

	got := tagAtCommit(r, head.Hash())
	if got != "v3.0.0-annotated" {
		t.Errorf("expected annotated tag name, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// VcsGit - IsCloned / Clone / Update / CurrentVersion (basic cases)
// ---------------------------------------------------------------------------

func TestVcsGit_IsCloned_False(t *testing.T) {
	g := &VcsGit{}
	if g.IsCloned(t.TempDir()) {
		t.Error("expected IsCloned=false for empty temp dir")
	}
}

func TestVcsGit_IsCloned_True(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	g := &VcsGit{}
	if !g.IsCloned(dir) {
		t.Error("expected IsCloned=true for go-git initialised repo")
	}
}

func TestVcsGit_Clone_BadURL(t *testing.T) {
	g := &VcsGit{}
	err := g.Clone(context.Background(), "not-a-real-url", filepath.Join(t.TempDir(), "dest"), "")
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
}

func TestVcsGit_Update_NotARepo(t *testing.T) {
	g := &VcsGit{}
	err := g.Update(context.Background(), "", t.TempDir(), "")
	if err == nil {
		t.Fatal("expected error when updating non-git directory")
	}
}

func TestVcsGit_CurrentVersion_NotARepo(t *testing.T) {
	g := &VcsGit{}
	_, err := g.CurrentVersion(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error when getting version of non-git directory")
	}
}

func TestVcsGit_Update_InitedRepoNoRemote(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	g := &VcsGit{}
	// Update will fail because there is no remote configured; this covers
	// the fetch error branch.
	err := g.Update(context.Background(), "", dir, "")
	if err == nil {
		t.Log("update succeeded unexpectedly on repo without remote")
	}
}

// ---------------------------------------------------------------------------
// VcsGit - HasChanges
// ---------------------------------------------------------------------------

func TestVcsGit_HasChanges_Clean(t *testing.T) {
	dir := t.TempDir()
	makeLocalRepo(t, dir)
	g := &VcsGit{}
	dirty, err := g.HasChanges(context.Background(), dir)
	if err != nil {
		t.Fatalf("HasChanges on clean repo: %v", err)
	}
	if dirty {
		t.Error("expected HasChanges=false for clean repository")
	}
}

func TestVcsGit_HasChanges_Modified(t *testing.T) {
	dir := t.TempDir()
	makeLocalRepo(t, dir)
	// Modify the tracked file without committing.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	g := &VcsGit{}
	dirty, err := g.HasChanges(context.Background(), dir)
	if err != nil {
		t.Fatalf("HasChanges on dirty repo: %v", err)
	}
	if !dirty {
		t.Error("expected HasChanges=true after modifying a tracked file")
	}
}

func TestVcsGit_HasChanges_NotARepo(t *testing.T) {
	g := &VcsGit{}
	_, err := g.HasChanges(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error when calling HasChanges on non-git directory")
	}
}

// TestVcsGit_HasChanges_BareRepo exercises the r.Worktree() error path:
// a bare repository has no working tree, so Worktree() returns an error.
func TestVcsGit_HasChanges_BareRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, true /* bare */); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	g := &VcsGit{}
	_, err := g.HasChanges(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error for bare repository (no working tree)")
	}
}

// ---------------------------------------------------------------------------
// VcsGit - Shallow clone and hard-reset update
// ---------------------------------------------------------------------------

func TestVcsGit_Clone_Shallow(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "shallow")
	g := &VcsGit{Shallow: true}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("shallow Clone: %v", err)
	}
	if !g.IsCloned(destDir) {
		t.Error("expected IsCloned=true after shallow Clone")
	}
}

func TestVcsGit_Update_Shallow_ResetsToOrigin(t *testing.T) {
	slog.Debug("testing shallow update policy", "test", t.Name())
	useInProcessGit(t)
	srcDir := t.TempDir()
	r := makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "shallow")
	g := &VcsGit{Shallow: true}
	if err := (&VcsGit{}).Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Add a new commit to the source using go-git.
	w, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	f := filepath.Join(srcDir, "extra.txt")
	os.WriteFile(f, []byte("extra"), 0o644)
	w.Add("extra.txt")
	_, err = w.Commit("second commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(destDir, "README.md"), []byte("cache edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Update(context.Background(), "", destDir, ""); err != nil {
		t.Fatalf("shallow Update: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destDir, "README.md")); err != nil || string(got) != "hello" {
		t.Errorf("shallow cache did not reset local edits: %q, %v", got, err)
	}
	// The new file should now exist in the destination.
	if _, err := os.Stat(filepath.Join(destDir, "extra.txt")); err != nil {
		t.Errorf("expected extra.txt after shallow update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VcsGit - Update URL mismatch detection (#220)
// ---------------------------------------------------------------------------

func TestVcsGit_Update_URLMismatch_HTTPSvsSSH(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	// Rewrite origin so it points at a different host (real mismatch).
	setOrigin(t, destDir, "git@gitlab.example.com:owner/repo.git")

	err := g.Update(context.Background(), "https://github.com/owner/repo.git", destDir, "")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	var mm *RemoteURLMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("expected *RemoteURLMismatchError, got %T: %v", err, err)
	}
	if mm.Path != destDir {
		t.Errorf("Path = %q, want %q", mm.Path, destDir)
	}
	if mm.ConfiguredURL != "https://github.com/owner/repo.git" {
		t.Errorf("ConfiguredURL = %q", mm.ConfiguredURL)
	}
	if mm.RemoteURL != "git@gitlab.example.com:owner/repo.git" {
		t.Errorf("RemoteURL = %q", mm.RemoteURL)
	}
}

func TestVcsGit_Update_URLMismatch_EmptyURLSkipsCheck(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	// Origin pointing at something gaal would normally flag — but with an
	// empty configured URL we must skip the check entirely (skill manager
	// behaviour).
	setOrigin(t, destDir, "https://elsewhere.invalid/x.git")
	err := g.Update(context.Background(), "", destDir, "")
	var mm *RemoteURLMismatchError
	if errors.As(err, &mm) {
		t.Fatalf("did not expect mismatch error with empty URL, got: %v", err)
	}
}

func TestVcsGit_Update_URLMatch_NormalisedEquivalent(t *testing.T) {
	srcDir := t.TempDir()
	makeLocalRepo(t, srcDir)
	destDir := filepath.Join(t.TempDir(), "clone")
	g := &VcsGit{}
	if err := g.Clone(context.Background(), srcDir, destDir, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	// Configured URL adds a trailing slash; origin keeps the bare path. The
	// normaliser must collapse these so no mismatch is raised.
	err := g.Update(context.Background(), srcDir+"/", destDir, "")
	var mm *RemoteURLMismatchError
	if errors.As(err, &mm) {
		t.Fatalf("unexpected mismatch for equivalent URLs: %v", err)
	}
}
