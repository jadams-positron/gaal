package vcs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

func useInProcessGit(t *testing.T) {
	t.Helper()
	slog.Debug("using embedded Git transport", "test", t.Name())
	original := client.Protocols["file"]
	client.InstallProtocol("file", fixtureGitTransport{server.NewClient(gitRepositoryLoader{})})
	t.Cleanup(func() {
		slog.Debug("restoring Git transport", "test", t.Name())
		client.InstallProtocol("file", original)
	})
	t.Setenv("PATH", t.TempDir())
}

type fixtureGitTransport struct{ transport.Transport }

func (f fixtureGitTransport) NewUploadPackSession(endpoint *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	slog.Debug("opening embedded Git session", "path", endpoint.Path)
	session, err := f.Transport.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return nil, err
	}
	store, err := (gitRepositoryLoader{}).Load(endpoint)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return fixtureGitSession{session, store}, nil
}

type fixtureGitSession struct {
	transport.UploadPackSession
	store storer.Storer
}

func (s fixtureGitSession) UploadPack(ctx context.Context, request *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	slog.DebugContext(ctx, "serving Git fixture objects", "haves", len(request.Haves))
	// The embedded server cannot negotiate unknown haves from divergent clients.
	filtered := *request
	filtered.Haves = nil
	for _, hash := range request.Haves {
		if err := s.store.HasEncodedObject(hash); err == nil {
			filtered.Haves = append(filtered.Haves, hash)
		} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, err
		}
	}
	return s.UploadPackSession.UploadPack(ctx, &filtered)
}

type gitRepositoryLoader struct{}

func (gitRepositoryLoader) Load(endpoint *transport.Endpoint) (storer.Storer, error) {
	slog.Debug("opening Git fixture storage", "path", endpoint.Path)
	r, err := gogit.PlainOpen(endpoint.Path)
	if err != nil {
		return nil, err
	}
	return r.Storer, nil
}

type fetchHook struct {
	transport.Transport
	onFetch func()
}

func (f fetchHook) NewUploadPackSession(endpoint *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	slog.Debug("intercepting Git fetch", "path", endpoint.Path)
	f.onFetch()
	return f.Transport.NewUploadPackSession(endpoint, auth)
}

func testBranchUpdates(t *testing.T, pinned bool) {
	t.Helper()
	slog.Debug("testing branch history updates", "test", t.Name(), "pinned", pinned)
	useInProcessGit(t)
	for _, tc := range []struct {
		name     string
		upstream bool
		local    bool
	}{
		{name: "unchanged"},
		{name: "fast-forward", upstream: true},
		{name: "ahead", local: true},
		{name: "diverged", upstream: true, local: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slog.Debug("testing branch history preservation", "scenario", tc.name)
			src := t.TempDir()
			r := makeLocalRepo(t, src)
			initial, err := r.Head()
			if err != nil {
				t.Fatal(err)
			}
			version := ""
			if pinned {
				version = initial.Name().Short()
			}
			dst := filepath.Join(t.TempDir(), "clone")
			g := &VcsGit{}
			if err := g.Clone(t.Context(), src, dst, version); err != nil {
				t.Fatal(err)
			}
			if tc.upstream {
				commitFile(t, src, "UPSTREAM.md", "upstream work\n")
			}
			if tc.local {
				commitFile(t, dst, "LOCAL.md", "local commit\n")
			}
			cloned, err := gogit.PlainOpen(dst)
			if err != nil {
				t.Fatal(err)
			}
			before, err := cloned.Head()
			if err != nil {
				t.Fatal(err)
			}
			err = g.Update(t.Context(), src, dst, version)
			if (err != nil) != tc.local || (tc.local && !errors.Is(err, gogit.ErrFastForwardMergeNotPossible)) {
				t.Errorf("Update error = %v; want fast-forward refusal=%t", err, tc.local)
			}
			want, err := r.Head()
			if err != nil {
				t.Fatal(err)
			}
			if tc.local {
				want = before
				if got, err := os.ReadFile(filepath.Join(dst, "LOCAL.md")); err != nil || string(got) != "local commit\n" {
					t.Errorf("local commit file changed: %q, %v", got, err)
				}
			} else if tc.upstream {
				if got, err := os.ReadFile(filepath.Join(dst, "UPSTREAM.md")); err != nil || string(got) != "upstream work\n" {
					t.Errorf("upstream file = %q, %v; want upstream work", got, err)
				}
			}
			if got, err := cloned.Head(); err != nil || got.Hash() != want.Hash() || got.Name() != want.Name() {
				t.Errorf("HEAD = %v, %v; want %v", got, err, want)
			}
		})
	}
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	slog.Debug("committing test file", "dir", dir, "name", name)
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("add "+name, &gogit.CommitOptions{Author: &object.Signature{
		Name: "test", Email: "test@example.test", When: time.Unix(2, 0),
	}}); err != nil {
		t.Fatal(err)
	}
}
