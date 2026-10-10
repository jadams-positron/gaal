package vcs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/getgaal/gaal/internal/core/vcs"
)

func TestVcsGit_Clone_RejectsUnsafeSubmoduleURL(t *testing.T) {
	slog.Debug("testing unsafe submodule clones", "test", t.Name())
	for _, raw := range []string{
		"file:///secret.git", "file://user@localhost:22/secret.git", filepath.Join(t.TempDir(), "secret.git"),
		`C:\secret.git`, `C:/secret.git`, `\\host\secret.git`,
		"http://example.test/secret.git", "http://user@example.test:80/secret.git", "http://169.254.169.254/secret.git",
		"svn://example.test/secret.git", "bzr://example.test/secret.git",
		"gopher://example.test/secret.git", "ftp://example.test/secret.git",
		"dict://example.test/secret.git", "%invalid",
	} {
		for _, depth := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/depth%d", raw, depth), func(t *testing.T) {
				slog.Debug("testing submodule rejection", "url", raw, "depth", depth)
				t.Setenv("PATH", t.TempDir())
				leaf := submoduleFixture(t)
				loader := &submoduleLoader{MapLoader: server.MapLoader{}}
				if ep, err := transport.NewEndpoint(raw); err == nil {
					loader.forbidden = ep.String()
					loader.MapLoader[ep.String()] = leaf.Storer
				}
				parent := submoduleFixture(t)
				setFixtureSubmodule(t, parent, raw, leaf)
				contentPath := filepath.Join("sub", "README.md")
				for i := 0; i < depth; i++ {
					url := fmt.Sprintf("https://example.test/level%d.git", i)
					loader.MapLoader[url] = parent.Storer
					next := submoduleFixture(t)
					setFixtureSubmodule(t, next, url, parent)
					parent = next
					contentPath = filepath.Join("sub", contentPath)
				}
				loader.MapLoader["https://example.test/root.git"] = parent.Storer
				installSubmoduleTransport(t, loader)
				dest := filepath.Join(t.TempDir(), "clone")
				err := (&vcs.VcsGit{}).Clone(t.Context(), "https://example.test/root.git", dest, "")
				content, readErr := os.ReadFile(filepath.Join(dest, contentPath))
				t.Logf("Clone error: %v; submodule content: %q", err, content)
				if err == nil {
					t.Error("Clone accepted an unsafe submodule URL")
				} else if !strings.Contains(err.Error(), "submodule") { // Requirement: identify the offending submodule.
					t.Errorf("error lacks submodule context: %v", err)
				}
				if !errors.Is(readErr, fs.ErrNotExist) {
					t.Errorf("unsafe submodule content is present or inaccessible: %q, %v", content, readErr)
				}
				// Requirement: rejected URLs must not reach the transport.
				if loader.fetchedForbidden {
					t.Error("unsafe submodule was fetched")
				}
			})
		}
	}
}

func TestVcsGit_Clone_AllowsSafeSubmoduleURL(t *testing.T) {
	slog.Debug("testing allowed submodule clones", "test", t.Name())
	for _, raw := range []string{"../leaf.git", "https://example.test/leaf.git", "http://127.0.0.1/leaf.git"} {
		t.Run(raw, func(t *testing.T) {
			slog.Debug("testing allowed submodule", "url", raw)
			t.Setenv("PATH", t.TempDir())
			leaf := submoduleFixture(t)
			middle := submoduleFixture(t)
			setFixtureSubmodule(t, middle, raw, leaf)
			root := submoduleFixture(t)
			setFixtureSubmodule(t, root, "../middle.git", middle)
			installSubmoduleTransport(t, &submoduleLoader{MapLoader: server.MapLoader{
				"https://example.test/root.git":   root.Storer,
				"https://example.test/middle.git": middle.Storer,
				"https://example.test/leaf.git":   leaf.Storer,
				"http://127.0.0.1/leaf.git":       leaf.Storer,
			}})
			dest := filepath.Join(t.TempDir(), "clone")
			if err := (&vcs.VcsGit{}).Clone(t.Context(), "https://example.test/root.git", dest, ""); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(dest, "sub", "sub", "README.md"))
			if err != nil || string(content) != "fixture content" {
				t.Fatalf("nested submodule content = %q, %v", content, err)
			}
		})
	}
}

func TestVcsGit_Clone_ValidatesSubmodulesAtVersion(t *testing.T) {
	slog.Debug("testing pinned submodule validation", "test", t.Name())
	for _, unsafeVersion := range []bool{false, true} {
		t.Run(fmt.Sprintf("unsafeVersion=%v", unsafeVersion), func(t *testing.T) {
			slog.Debug("testing pinned submodules", "unsafe_version", unsafeVersion)
			t.Setenv("PATH", t.TempDir())
			leaf := submoduleFixture(t)
			root := submoduleFixture(t)
			pinnedURL, headURL := "../leaf.git", "file:///secret.git"
			if unsafeVersion {
				pinnedURL, headURL = headURL, pinnedURL
			}
			setFixtureSubmodule(t, root, pinnedURL, leaf)
			pinned, err := root.Head()
			if err != nil {
				t.Fatal(err)
			}
			setFixtureSubmodule(t, root, headURL, leaf)
			loader := &submoduleLoader{MapLoader: server.MapLoader{
				"https://example.test/root.git": root.Storer,
				"https://example.test/leaf.git": leaf.Storer,
				"file:///secret.git":            leaf.Storer,
			}, forbidden: "file:///secret.git"}
			installSubmoduleTransport(t, loader)
			dest := filepath.Join(t.TempDir(), "clone")
			err = (&vcs.VcsGit{}).Clone(t.Context(), "https://example.test/root.git", dest, pinned.Hash().String())
			if (err != nil) != unsafeVersion {
				t.Fatalf("Clone error = %v, want rejection = %v", err, unsafeVersion)
			}
			// Requirement: neither the default nor pinned revision may fetch rejected URLs.
			if loader.fetchedForbidden {
				t.Error("unsafe submodule was fetched")
			}
			content, readErr := os.ReadFile(filepath.Join(dest, "sub", "README.md"))
			if unsafeVersion {
				if !errors.Is(readErr, fs.ErrNotExist) {
					t.Errorf("unsafe content = %q, %v", content, readErr)
				}
			} else if readErr != nil || string(content) != "fixture content" {
				t.Errorf("safe content = %q, %v", content, readErr)
			}
		})
	}
}

func TestVcsGit_Clone_FailsClosedAtSubmoduleRecursionLimit(t *testing.T) {
	slog.Debug("testing submodule recursion limit", "test", t.Name())
	limit := int(gogit.DefaultSubmoduleRecursionDepth) + 1
	for _, depth := range []int{limit, limit + 1} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			slog.Debug("testing bounded submodule traversal", "depth", depth)
			t.Setenv("PATH", t.TempDir())
			parent := submoduleFixture(t)
			loader := &submoduleLoader{MapLoader: server.MapLoader{}}
			contentPath := "README.md"
			for i := 0; i < depth; i++ {
				url := fmt.Sprintf("https://example.test/level%d.git", i)
				loader.MapLoader[url] = parent.Storer
				next := submoduleFixture(t)
				setFixtureSubmodule(t, next, url, parent)
				parent = next
				contentPath = filepath.Join("sub", contentPath)
			}
			loader.MapLoader["https://example.test/root.git"] = parent.Storer
			installSubmoduleTransport(t, loader)
			dest := filepath.Join(t.TempDir(), "clone")
			err := (&vcs.VcsGit{}).Clone(t.Context(), "https://example.test/root.git", dest, "")
			if (err != nil) != (depth > limit) {
				t.Fatalf("Clone error = %v, want rejection = %v", err, depth > limit)
			}
			content, readErr := os.ReadFile(filepath.Join(dest, contentPath))
			if depth > limit {
				if !errors.Is(readErr, fs.ErrNotExist) {
					t.Errorf("over-limit content = %q, %v", content, readErr)
				}
			} else if readErr != nil || string(content) != "fixture content" {
				t.Errorf("allowed-depth content = %q, %v", content, readErr)
			}
		})
	}
}

func submoduleFixture(t *testing.T) *gogit.Repository {
	t.Helper()
	slog.Debug("building in-memory git fixture", "test", t.Name())
	r, err := gogit.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		t.Fatal(err)
	}
	commitFixtureFile(t, r, "README.md", "fixture content")
	return r
}

func setFixtureSubmodule(t *testing.T, r *gogit.Repository, raw string, child *gogit.Repository) {
	t.Helper()
	slog.Debug("adding fixture submodule", "url", raw)
	head, err := child.Head()
	if err != nil {
		t.Fatal(err)
	}
	idx, err := r.Storer.Index()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Entry("sub"); errors.Is(err, index.ErrEntryNotFound) {
		idx.Entries = append(idx.Entries, &index.Entry{Name: "sub", Mode: filemode.Submodule, Hash: head.Hash()})
	}
	if err := r.Storer.SetIndex(idx); err != nil {
		t.Fatal(err)
	}
	commitFixtureFile(t, r, ".gitmodules", fmt.Sprintf("[submodule \"sub\"]\npath = sub\nurl = %q\n", raw))
}

func commitFixtureFile(t *testing.T, r *gogit.Repository, name, content string) {
	t.Helper()
	slog.Debug("committing fixture file", "name", name)
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	f, err := w.Filesystem.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("fixture", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.test", When: time.Unix(1, 0)},
	}); err != nil {
		t.Fatal(err)
	}
}

type submoduleLoader struct {
	server.MapLoader
	forbidden        string
	fetchedForbidden bool
}

func (l *submoduleLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	slog.Debug("loading fixture repository", "endpoint", ep.String())
	repo, err := l.MapLoader.Load(ep)
	if ep.String() == l.forbidden || err != nil {
		l.fetchedForbidden = true
	}
	return repo, err
}

func installSubmoduleTransport(t *testing.T, loader *submoduleLoader) {
	t.Helper()
	slog.Debug("installing in-process git transport", "test", t.Name())
	for _, scheme := range []string{"file", "http", "https", "ssh", "git", "svn", "bzr", "gopher", "ftp", "dict"} {
		previous := client.Protocols[scheme]
		client.InstallProtocol(scheme, server.NewClient(loader))
		t.Cleanup(func() {
			slog.Debug("restoring git transport", "scheme", scheme)
			client.InstallProtocol(scheme, previous)
		})
	}
}
