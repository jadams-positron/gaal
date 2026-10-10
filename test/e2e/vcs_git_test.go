//go:build e2e

// Hermetic VCS coverage — issue #143.
//
// Until this file landed the e2e suite never exercised the actual VCS
// backends (git via go-git, hg/svn/bzr/tar/zip via subprocess). Every
// `repositories:` entry the suite ever wrote was a no-op because nothing
// in the test configs used one. A regression in internal/core/vcs/git.go
// (broken ref resolution, wrong checksum, partial clone recovery) would
// ship green.
//
// This file covers the **git** backend hermetically: it bootstraps a bare
// repo inside the container, clones from it via the gaal git backend, and
// verifies update-after-upstream-change. The hg/svn/bzr/tar/zip backends
// require additional packages and a HTTP fileserver; tracked as follow-up
// scope under #143.
package e2e

import (
	"log/slog"
	"path"
	"strings"
	"testing"
)

// gitConfigEnv returns the env vars needed to make `git commit` work in a
// fresh container — git complains without a configured user.
var gitConfigEnv = Env{
	"GIT_AUTHOR_NAME":     "gaal-e2e",
	"GIT_AUTHOR_EMAIL":    "e2e@example.test",
	"GIT_COMMITTER_NAME":  "gaal-e2e",
	"GIT_COMMITTER_EMAIL": "e2e@example.test",
}

// initBareGitRepo creates a bare git repo at <root>/<name>.git with one
// commit (a README) so it can be cloned. Returns the absolute path to the
// bare repo, suitable for use as a gaal repository url.
func initBareGitRepo(t *testing.T, env *testEnv, root, name string) string {
	t.Helper()
	bare := path.Join(root, name+".git")
	work := path.Join(root, name+"-work")

	// `git -c init.defaultBranch=main` per call sets the initial branch
	// without ever touching the host's global git config (AGENTS.md
	// forbids global mutations) and silences the "Using 'master'" hint
	// that would otherwise pollute verbose / failure logs.
	env.c.MustExec(t, nil, "", "git", "-c", "init.defaultBranch=main", "init", "--bare", bare)
	env.c.MustExec(t, nil, "", "git", "-c", "init.defaultBranch=main", "init", work)
	env.c.WriteFile(t, path.Join(work, "README.md"), "# initial\n")
	env.c.MustExec(t, gitConfigEnv, work, "git", "add", "README.md")
	env.c.MustExec(t, gitConfigEnv, work, "git", "commit", "-m", "initial")
	env.c.MustExec(t, gitConfigEnv, work, "git", "remote", "add", "origin", bare)
	env.c.MustExec(t, gitConfigEnv, work, "git", "push", "-u", "origin", "main")
	// Re-point HEAD on the bare repo so go-git's PlainClone resolves the
	// default branch. Even with init.defaultBranch=main on `git init --bare`,
	// the bare's HEAD is unborn until the first push lands.
	env.c.MustExec(t, nil, "", "git", "--git-dir", bare,
		"symbolic-ref", "HEAD", "refs/heads/main")
	return bare
}

// addCommitToBare makes a new commit on the bare repo's main branch via a
// fresh worktree. Used to test update-after-upstream-change. Returns the
// short hash of the new commit.
func addCommitToBare(t *testing.T, env *testEnv, bare, fileName, content string) {
	t.Helper()
	work := bare + "-update-work"
	env.c.MustExec(t, nil, "", "rm", "-rf", work)
	env.c.MustExec(t, nil, "", "git", "clone", bare, work)
	env.c.WriteFile(t, path.Join(work, fileName), content)
	env.c.MustExec(t, gitConfigEnv, work, "git", "add", fileName)
	env.c.MustExec(t, gitConfigEnv, work, "git", "commit", "-m", "add "+fileName)
	env.c.MustExec(t, gitConfigEnv, work, "git", "push", "origin", "main")
}

// TestVCS_GitBackend_CloneAndCheckout asserts gaal can clone a hermetic
// bare repo via the git backend (go-git, no binary required) and lay out
// the repository at the configured path.
func TestVCS_GitBackend_CloneAndCheckout(t *testing.T) {
	env := newTestEnv(t)

	reposRoot := path.Join(env.home, "test-repos")
	env.c.MustExec(t, nil, "", "mkdir", "-p", reposRoot)
	bare := initBareGitRepo(t, env, reposRoot, "myrepo")

	cfg := newConfig().
		AddRepository("src/myrepo", "git", bare, "").
		String()
	cfgPath := env.writeProjectConfig(t, cfg)

	env.mustGaal(t, cfgPath, "sync")

	// The README from the initial commit must exist at the expected path.
	dst := path.Join(env.workdir, "src", "myrepo", "README.md")
	if !env.c.FileExists(t, dst) {
		t.Fatalf("expected cloned README at %s after sync", dst)
	}
	content := env.c.ReadFile(t, dst)
	if content != "# initial\n" {
		t.Errorf("README content mismatch: got %q", content)
	}
}

// TestVCS_GitBackend_UpdateAfterUpstreamChange asserts a second sync picks
// up a new upstream commit. Regression target for the Update path —
// initial Clone is the easy case; making sure subsequent Updates actually
// pull is the surface that historically breaks (#125).
func TestVCS_GitBackend_UpdateAfterUpstreamChange(t *testing.T) {
	slog.Debug("testing upstream updates", "test", t.Name())
	for _, tc := range []struct{ name, version string }{
		{name: "unpinned"},
		{name: "pinned-branch", version: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slog.Debug("testing upstream branch update", "version", tc.version)
			env := newTestEnv(t)

			reposRoot := path.Join(env.home, "test-repos")
			env.c.MustExec(t, nil, "", "mkdir", "-p", reposRoot)
			bare := initBareGitRepo(t, env, reposRoot, "updaterepo")

			cfg := newConfig().
				AddRepository("src/updaterepo", "git", bare, tc.version).
				String()
			cfgPath := env.writeProjectConfig(t, cfg)

			env.mustGaal(t, cfgPath, "sync")

			addCommitToBare(t, env, bare, "NEWFILE.md", "added later\n")
			result := env.mustGaal(t, cfgPath, "sync")

			dst := path.Join(env.workdir, "src", "updaterepo", "NEWFILE.md")
			if !env.c.FileExists(t, dst) {
				t.Fatalf("expected NEWFILE.md after second sync (update path); not found")
			}
			if got := env.c.ReadFile(t, dst); got != "added later\n" {
				t.Errorf("NEWFILE.md content mismatch: got %q", got)
			}
			t.Logf("second gaal sync: exit=%d; NEWFILE.md contains added later", result.ExitCode)
		})
	}
}

// TestVCS_GitBackend_VersionPin clones the repo at a specific commit hash
// and asserts only the pinned commit's content is present (the later one
// is not). Pins #125's "version-aware checkout" half.
func TestVCS_GitBackend_VersionPin(t *testing.T) {
	env := newTestEnv(t)

	reposRoot := path.Join(env.home, "test-repos")
	env.c.MustExec(t, nil, "", "mkdir", "-p", reposRoot)
	bare := initBareGitRepo(t, env, reposRoot, "pinrepo")

	// Capture the initial commit hash before adding more.
	initial := env.c.MustExec(t, nil, "",
		"git", "--git-dir", bare, "rev-parse", "HEAD").Stdout
	initial = trimNewline(initial)

	addCommitToBare(t, env, bare, "LATER.md", "later commit\n")

	cfg := newConfig().
		AddRepository("src/pinrepo", "git", bare, initial).
		String()
	cfgPath := env.writeProjectConfig(t, cfg)
	env.mustGaal(t, cfgPath, "sync")

	dst := path.Join(env.workdir, "src", "pinrepo", "README.md")
	if !env.c.FileExists(t, dst) {
		t.Fatalf("README missing after pinned sync at %s", dst)
	}
	// The later file MUST NOT be present — version pin held.
	later := path.Join(env.workdir, "src", "pinrepo", "LATER.md")
	if env.c.FileExists(t, later) {
		t.Errorf("version pin (%s) did not hold: LATER.md from a later commit is present",
			initial)
	}
}

func TestVCS_GitBackend_DirtyCloneRefusesUpdate(t *testing.T) {
	slog.Debug("testing dirty clone sync", "test", t.Name())
	for _, version := range []string{"", "main"} {
		for _, file := range []string{"README.md", "LOCAL.md", ".env"} {
			t.Run(version+"/"+file, func(t *testing.T) {
				slog.Debug("testing dirty clone preservation", "version", version, "file", file)
				env := newTestEnv(t)
				bare := initBareGitRepo(t, env, env.home, "dirtyrepo")
				if file == ".env" {
					addCommitToBare(t, env, bare, ".gitignore", ".env\n")
				}
				cfgPath := env.writeProjectConfig(t, newConfig().
					AddRepository("src/dirtyrepo", "git", bare, version).String())
				env.mustGaal(t, cfgPath, "sync")
				dst := path.Join(env.workdir, "src", "dirtyrepo", file)
				env.c.WriteFile(t, dst, "local work\n")

				result := env.gaal(t, cfgPath, "sync")
				if result.ExitCode == 0 || !strings.Contains(result.Combined(), "uncommitted local changes") {
					t.Errorf("expected dirty clone refusal: exit=%d\n%s", result.ExitCode, result.Combined())
				}
				if got := env.c.ReadFile(t, dst); got != "local work\n" {
					t.Errorf("local file = %q; want preserved local work", got)
				}
				t.Logf("gaal sync: exit=%d\n%s\n%s preserved", result.ExitCode, result.Combined(), file)
			})
		}
	}
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
