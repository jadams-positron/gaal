package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertSkillInstall_CreatesMinimalConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaal.yaml")
	result, err := UpsertSkillInstall(path, ConfigSkill{
		Source:   "anthropics/skills",
		Registry: "skills.sh",
		Select:   []string{"frontend-design"},
		Agents:   []string{"*"},
		Global:   true,
	})
	if err != nil {
		t.Fatalf("UpsertSkillInstall: %v", err)
	}
	if !result.Created || !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	out := string(data)
	for _, want := range []string{"schema: 1", "repositories: {}", "registry: skills.sh", "global: true", "mcps: []"} {
		if !strings.Contains(out, want) {
			t.Fatalf("created config missing %q:\n%s", want, out)
		}
	}
	cfg, err := LoadStrict(path)
	if err != nil {
		t.Fatalf("LoadStrict: %v", err)
	}
	if len(cfg.Skills) != 1 || cfg.Skills[0].Registry != "skills.sh" {
		t.Fatalf("skills = %+v", cfg.Skills)
	}
}

func TestUpsertSkillInstall_MergesSelectIdempotently(t *testing.T) {
	path := writeYAML(t, `
schema: 1
skills:
  - source: anthropics/skills
    registry: skills.sh
    agents: ["codex", "claude-code"]
    global: true
    select:
      - backend-design
`)
	result, err := UpsertSkillInstall(path, ConfigSkill{
		Source:   "anthropics/skills",
		Registry: "skills.sh",
		Select:   []string{"frontend-design"},
		Agents:   []string{"claude-code", "codex"},
		Global:   true,
	})
	if err != nil {
		t.Fatalf("UpsertSkillInstall: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected changed result")
	}
	cfg, err := LoadStrict(path)
	if err != nil {
		t.Fatalf("LoadStrict: %v", err)
	}
	got := cfg.Skills[0].Select
	if len(got) != 2 || got[0] != "backend-design" || got[1] != "frontend-design" {
		t.Fatalf("select = %v", got)
	}

	again, err := UpsertSkillInstall(path, ConfigSkill{
		Source:   "anthropics/skills",
		Registry: "skills.sh",
		Select:   []string{"frontend-design"},
		Agents:   []string{"codex", "claude-code"},
		Global:   true,
	})
	if err != nil {
		t.Fatalf("second UpsertSkillInstall: %v", err)
	}
	if again.Changed {
		t.Fatalf("expected idempotent second install, got %+v", again)
	}
}

func TestUpsertSkillInstall_SelectOmittedAlreadyCovered(t *testing.T) {
	path := writeYAML(t, `
schema: 1
skills:
  - source: anthropics/skills
    registry: skills.sh
    agents: ["*"]
    global: true
`)
	result, err := UpsertSkillInstall(path, ConfigSkill{
		Source:   "anthropics/skills",
		Registry: "skills.sh",
		Select:   []string{"frontend-design"},
		Agents:   []string{"*"},
		Global:   true,
	})
	if err != nil {
		t.Fatalf("UpsertSkillInstall: %v", err)
	}
	if result.Changed || !result.AlreadyCovered {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpsertSkillInstall_DifferentGlobalAddsEntry(t *testing.T) {
	path := writeYAML(t, `
schema: 1
skills:
  - source: anthropics/skills
    registry: skills.sh
    agents: ["*"]
    global: false
    select: [project-skill]
`)
	if _, err := UpsertSkillInstall(path, ConfigSkill{
		Source:   "anthropics/skills",
		Registry: "skills.sh",
		Select:   []string{"global-skill"},
		Agents:   []string{"*"},
		Global:   true,
	}); err != nil {
		t.Fatalf("UpsertSkillInstall: %v", err)
	}
	cfg, err := LoadStrict(path)
	if err != nil {
		t.Fatalf("LoadStrict: %v", err)
	}
	if len(cfg.Skills) != 2 {
		t.Fatalf("expected separate entries, got %+v", cfg.Skills)
	}
}
