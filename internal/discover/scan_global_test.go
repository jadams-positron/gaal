package discover_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getgaal/gaal/internal/discover"
)

func TestScanPackageManagerSkills(t *testing.T) {
	slog.DebugContext(t.Context(), "testing package-manager skill discovery", "test", t.Name())
	// Requirement: package-manager discovery includes depth 4, but not depth 5.
	for _, tt := range []struct {
		name  string
		path  string
		agent string
		want  bool
	}{
		{"cursor_extension", ".cursor/extensions/ext1/skills/demo", "cursor", true},
		{"below_cap", ".claude/plugins/cache/plugin/v1/skills/demo", "claude-code", true},
		{"at_cap", ".claude/plugins/cache/market/plugin/v1/skills/demo", "claude-code", true},
		{"beyond_cap", ".claude/plugins/cache/market/plugin/v1/extra/skills/demo", "", false},
		{"node_modules", ".cursor/extensions/ext1/node_modules/skills/demo", "", false},
		{"vendor", ".cursor/extensions/ext1/vendor/skills/demo", "", false},
		{"dist", ".cursor/extensions/ext1/dist/skills/demo", "", false},
		{"cache", ".cursor/extensions/ext1/.cache/skills/demo", "", false},
		{"bin", ".cursor/extensions/ext1/bin/skills/demo", "", false},
		{"canonical", ".claude/skills/demo", "claude-code", true},
		{"canonical_named_node_modules", ".cursor/skills/node_modules", "cursor", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			slog.DebugContext(t.Context(), "checking skill location", "path", tt.path)
			home := t.TempDir()
			dir := filepath.Join(home, filepath.FromSlash(tt.path))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			resources, err := discover.Scan(t.Context(), home, t.TempDir(), discover.ScanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !tt.want {
				if len(resources) != 0 {
					t.Fatalf("excluded skill was discovered: %v", resources)
				}
				return
			}
			if len(resources) != 1 {
				t.Fatalf("expected one skill, got %v", resources)
			}
			r := resources[0]
			if r.Path != dir || r.Name != "demo" || r.Type != discover.ResourceSkill || r.Scope != discover.ScopeGlobal || r.Meta["agent"] != tt.agent {
				t.Errorf("unexpected skill: %+v", r)
			}
		})
	}
}

func TestScanCanceledSkipsSkills(t *testing.T) {
	slog.DebugContext(t.Context(), "testing canceled skill discovery", "test", t.Name())
	for _, path := range []string{
		".cursor/skills/demo",
		".cursor/extensions/ext1/skills/demo",
	} {
		t.Run(path, func(t *testing.T) {
			slog.DebugContext(t.Context(), "checking canceled scan", "path", path)
			home := t.TempDir()
			dir := filepath.Join(home, filepath.FromSlash(path))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			workDir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			start := time.Now()
			resources, err := discover.Scan(ctx, home, workDir, discover.ScanOptions{})
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("canceled scan took %s", elapsed)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(resources) != 0 {
				t.Errorf("canceled scan discovered skills: %v", resources)
			}
		})
	}
}
