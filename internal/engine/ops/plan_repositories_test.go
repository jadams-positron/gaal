package ops_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/getgaal/gaal/internal/config"
	"github.com/getgaal/gaal/internal/content"
	"github.com/getgaal/gaal/internal/engine/ops"
	"github.com/getgaal/gaal/internal/engine/render"
	"github.com/getgaal/gaal/internal/mcp"
	"github.com/getgaal/gaal/internal/repo"
	"github.com/getgaal/gaal/internal/skill"
)

func TestSyncPlan_DirtyGitRepository(t *testing.T) {
	slog.Debug("testing dirty repository plan", "test", t.Name())
	for _, file := range []string{"README.md", "LOCAL.md"} {
		t.Run(file, func(t *testing.T) {
			slog.Debug("testing dirty repository plan entry", "file", file)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Chdir(home)
			dir := filepath.Join(home, "repo")
			r, err := gogit.PlainInit(dir, false)
			if err != nil {
				t.Fatal(err)
			}
			w, err := r.Worktree()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("initial\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Add("README.md"); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Commit("initial", &gogit.CommitOptions{Author: &object.Signature{
				Name: "test", Email: "test@example.test", When: time.Unix(1, 0),
			}}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte("local work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			state := t.TempDir()
			plan, err := ops.SyncPlan(t.Context(),
				repo.NewManager(map[string]config.ConfigRepo{dir: {Type: "git"}}, state),
				skill.NewManager(nil, t.TempDir(), home, home, state, false),
				content.NewManager(nil, t.TempDir(), home, home, state, false),
				mcp.NewManager(nil, home, state), home, home, state)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Repositories) != 1 {
				t.Fatalf("repositories = %v; want one managed repository", plan.Repositories)
			}
			entry := plan.Repositories[0]
			if entry.Action != render.PlanError || entry.Error == "" || !plan.HasErrors || plan.HasChanges {
				t.Errorf("dirty repository plan = %+v, errors=%t changes=%t; want error, no update", entry, plan.HasErrors, plan.HasChanges)
			}
		})
	}
}
