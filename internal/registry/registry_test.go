package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want SkillRef
	}{
		{"loose", "frontend-design", SkillRef{Skill: "frontend-design"}},
		{"registry", "skills.sh/frontend-design", SkillRef{Registry: SkillsSH, Skill: "frontend-design"}},
		{"source", "anthropics/skills:frontend-design", SkillRef{Source: "anthropics/skills", Skill: "frontend-design"}},
		{"full", "skills.sh/anthropics/skills:frontend-design", SkillRef{Registry: SkillsSH, Source: "anthropics/skills", Skill: "frontend-design"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRef(tt.raw)
			if err != nil {
				t.Fatalf("ParseRef: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseRef() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSkillsSHSearchUsesNPXFindAndParsesOutput(t *testing.T) {
	runner := &fakeCommandRunner{
		path: "/usr/local/bin/npx",
		output: "\x1b[32manthropics/skills@frontend-design 619K installs\x1b[0m\n" +
			"  https://skills.sh/anthropics/skills/frontend-design\n" +
			"google-labs-code/stitch-skills@react:components 50.4K installs\n" +
			"  https://skills.sh/google-labs-code/stitch-skills/react:components\n",
	}
	client := NewSkillsSH(runner)

	got, err := client.Search(context.Background(), "react", SearchOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if runner.lookedUp != "npx" {
		t.Fatalf("lookedUp = %q, want npx", runner.lookedUp)
	}
	if runner.command != "npx" || strings.Join(runner.args, " ") != "-y skills find react" {
		t.Fatalf("command = %s %v", runner.command, runner.args)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results", len(got))
	}
	if got[0].Source != "anthropics/skills" || got[0].Slug != "frontend-design" {
		t.Fatalf("unexpected result: %+v", got[0])
	}
	if got[0].Installs != 619000 {
		t.Fatalf("installs = %d, want 619000", got[0].Installs)
	}
	if got[0].URL != "https://skills.sh/anthropics/skills/frontend-design" {
		t.Fatalf("url = %q", got[0].URL)
	}
}

func TestSkillsSHSearchRequiresNPX(t *testing.T) {
	client := NewSkillsSH(&fakeCommandRunner{lookPathErr: errors.New("missing")})

	_, err := client.Search(context.Background(), "react", SearchOptions{Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "requires npx on PATH") {
		t.Fatalf("err = %v, want npx requirement", err)
	}
}

func TestSkillsSHResolveAmbiguousLooseName(t *testing.T) {
	runner := &fakeCommandRunner{
		path: "/usr/local/bin/npx",
		output: "a/skills@frontend 1K installs\n" +
			"  https://skills.sh/a/skills/frontend\n" +
			"b/skills@frontend 2K installs\n" +
			"  https://skills.sh/b/skills/frontend\n",
	}
	client := NewSkillsSH(runner)

	_, err := client.Resolve(context.Background(), SkillRef{Skill: "frontend"})
	if err == nil {
		t.Fatal("expected ambiguity")
	}
	amb, ok := err.(*AmbiguousError)
	if !ok {
		t.Fatalf("err = %T, want *AmbiguousError", err)
	}
	if len(amb.Candidates) != 2 {
		t.Fatalf("candidates = %d", len(amb.Candidates))
	}
}

func TestSkillsSHResolveSourceQualified(t *testing.T) {
	runner := &fakeCommandRunner{lookPathErr: errors.New("must not run")}
	client := NewSkillsSH(runner)

	got, err := client.Resolve(context.Background(), SkillRef{Source: "vercel-labs/skills", Skill: "find-skills"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != "vercel-labs/skills" || got.Select != "find-skills" {
		t.Fatalf("resolved = %+v", got)
	}
	if runner.lookedUp != "" || runner.command != "" {
		t.Fatalf("source-qualified resolve should not run npx")
	}
}

func TestParseInstalls(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"521.3K", 521300},
		{"2.3M", 2300000},
		{"834", 834},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := parseInstalls(tt.raw); got != tt.want {
				t.Fatalf("parseInstalls(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

type fakeCommandRunner struct {
	path        string
	output      string
	lookPathErr error
	commandErr  error
	lookedUp    string
	command     string
	args        []string
}

func (r *fakeCommandRunner) LookPath(file string) (string, error) {
	r.lookedUp = file
	if r.lookPathErr != nil {
		return "", r.lookPathErr
	}
	return r.path, nil
}

func (r *fakeCommandRunner) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	r.command = name
	r.args = args
	return []byte(r.output), r.commandErr
}
