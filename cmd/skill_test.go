package cmd

import (
	"strings"
	"testing"

	skillreg "github.com/getgaal/gaal/internal/registry"
)

func TestResolveSkillInstallAgents(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		prompt    bool
		want      []string
		wantGiven bool
		wantErr   string
	}{
		{"prompt", "", true, nil, false, ""},
		{"non-interactive default", "", false, []string{"*"}, true, ""},
		{"wildcard", "*", true, []string{"*"}, true, ""},
		{"explicit", "codex,claude-code", true, []string{"codex", "claude-code"}, true, ""},
		{"mixed wildcard", "*,codex", true, nil, true, "cannot combine"},
		{"unknown", "no-such-agent", true, nil, true, "unknown agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, given, err := resolveSkillInstallAgents(tt.raw, tt.prompt)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSkillInstallAgents: %v", err)
			}
			if given != tt.wantGiven {
				t.Fatalf("given = %v, want %v", given, tt.wantGiven)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("agents = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatAmbiguousInstallError(t *testing.T) {
	err := formatAmbiguousInstallError(&skillreg.AmbiguousError{
		Query: "frontend",
		Candidates: []skillreg.SkillResult{
			{Registry: "skills.sh", Source: "a/skills", Slug: "frontend"},
			{Registry: "skills.sh", Source: "b/skills", Slug: "frontend"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"ambiguous skill", "skills.sh/a/skills:frontend", "skills.sh/b/skills:frontend"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %s", want, err.Error())
		}
	}
}

func TestEnsureResolvedSkillSupported(t *testing.T) {
	tests := []struct {
		name    string
		skill   skillreg.ResolvedSkill
		wantErr bool
	}{
		{"github source type", skillreg.ResolvedSkill{SkillResult: skillreg.SkillResult{Name: "x", Source: "owner/repo", SourceType: "github"}}, false},
		{"github shorthand", skillreg.ResolvedSkill{SkillResult: skillreg.SkillResult{Name: "x", Source: "owner/repo"}}, false},
		{"url", skillreg.ResolvedSkill{SkillResult: skillreg.SkillResult{Name: "x", Source: "https://github.com/owner/repo"}}, false},
		{"well-known", skillreg.ResolvedSkill{SkillResult: skillreg.SkillResult{Name: "x", Source: "mintlify.com", SourceType: "well-known"}}, true},
		{"unrepresentable", skillreg.ResolvedSkill{SkillResult: skillreg.SkillResult{Name: "x", Source: "mintlify.com"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureResolvedSkillSupported(tt.skill)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ensureResolvedSkillSupported err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
