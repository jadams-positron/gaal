package registry

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type commandRunner interface {
	LookPath(file string) (string, error)
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) LookPath(file string) (string, error) {
	slog.Debug("looking up command", "file", file)
	return exec.LookPath(file)
}

func (execCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	slog.DebugContext(ctx, "running command", "name", name, "args", args)
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// SkillsSHClient implements skills.sh discovery through the public npm CLI.
type SkillsSHClient struct {
	runner commandRunner
}

// NewSkillsSH creates a skills.sh registry client.
func NewSkillsSH(runner commandRunner) *SkillsSHClient {
	slog.Debug("creating skills.sh registry client")
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &SkillsSHClient{runner: runner}
}

func (c *SkillsSHClient) Name() string {
	slog.Debug("returning skills.sh registry name")
	return SkillsSH
}

func (c *SkillsSHClient) Search(ctx context.Context, query string, opts SearchOptions) ([]SkillResult, error) {
	slog.DebugContext(ctx, "searching skills.sh through npx", "query", query, "limit", opts.Limit)
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, fmt.Errorf("search query must be at least 2 characters")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	out, err := c.runFind(ctx, query)
	if err != nil {
		return nil, err
	}
	results := parseSkillsSHFindOutput(string(out), limit)
	return results, nil
}

func (c *SkillsSHClient) Resolve(ctx context.Context, ref SkillRef) (ResolvedSkill, error) {
	slog.DebugContext(ctx, "resolving skills.sh ref", "source", ref.Source, "skill", ref.Skill)
	if ref.Source != "" {
		return resolvedFromParts(ref.Source, ref.Skill), nil
	}
	results, err := c.Search(ctx, ref.Skill, SearchOptions{Limit: 50})
	if err != nil {
		return ResolvedSkill{}, err
	}
	if len(results) == 0 {
		return ResolvedSkill{}, fmt.Errorf("%w: %s", ErrNotFound, ref.Skill)
	}
	matches := exactMatches(ref.Skill, results)
	switch {
	case len(matches) == 1:
		return c.ResolveResult(ctx, matches[0])
	case len(matches) > 1:
		sortCandidates(matches)
		return ResolvedSkill{}, &AmbiguousError{Query: ref.Skill, Candidates: matches}
	case len(results) == 1:
		return c.ResolveResult(ctx, results[0])
	default:
		return ResolvedSkill{}, &AmbiguousError{Query: ref.Skill, Candidates: results}
	}
}

func (c *SkillsSHClient) ResolveResult(ctx context.Context, result SkillResult) (ResolvedSkill, error) {
	slog.DebugContext(ctx, "resolving skills.sh result", "source", result.Source, "slug", result.Slug)
	if result.Source == "" || result.Slug == "" {
		return ResolvedSkill{}, fmt.Errorf("skills.sh result missing source or skill slug")
	}
	if result.Registry == "" {
		result.Registry = SkillsSH
	}
	if result.Name == "" {
		result.Name = result.Slug
	}
	return ResolvedSkill{SkillResult: result, Select: result.Slug}, nil
}

func (c *SkillsSHClient) runFind(ctx context.Context, query string) ([]byte, error) {
	slog.DebugContext(ctx, "running skills.sh find command", "query", query)
	if _, err := c.runner.LookPath("npx"); err != nil {
		return nil, fmt.Errorf("skills.sh registry requires npx on PATH: %w", err)
	}
	out, err := c.runner.CombinedOutput(ctx, "npx", "-y", "skills", "find", query)
	if err != nil {
		clean := strings.TrimSpace(stripANSI(string(out)))
		if clean != "" {
			return nil, fmt.Errorf("running npx skills find: %w: %s", err, clean)
		}
		return nil, fmt.Errorf("running npx skills find: %w", err)
	}
	return out, nil
}

func resolvedFromParts(source, skill string) ResolvedSkill {
	slog.Debug("resolving skills.sh source-qualified ref", "source", source, "skill", skill)
	return ResolvedSkill{
		SkillResult: SkillResult{
			ID:       joinSkillID(source, skill),
			Slug:     skill,
			Name:     skill,
			Source:   source,
			Registry: SkillsSH,
			URL:      skillsSHWebURL(source, skill),
		},
		Select: skill,
	}
}

func parseSkillsSHFindOutput(raw string, limit int) []SkillResult {
	slog.Debug("parsing skills.sh find output", "limit", limit)
	clean := stripANSI(raw)
	lines := strings.Split(clean, "\n")
	results := make([]SkillResult, 0, limit)
	for i := 0; i < len(lines); i++ {
		result, ok := parseSkillsSHResultLine(lines[i])
		if !ok {
			continue
		}
		if i+1 < len(lines) {
			if url := parseSkillsSHURLLine(lines[i+1]); url != "" {
				result.URL = url
				i++
			}
		}
		results = append(results, result)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

func parseSkillsSHResultLine(line string) (SkillResult, bool) {
	slog.Debug("parsing skills.sh result line")
	line = strings.TrimSpace(line)
	if !strings.Contains(line, " installs") {
		return SkillResult{}, false
	}
	at := strings.LastIndex(line, "@")
	if at <= 0 {
		return SkillResult{}, false
	}
	source := strings.TrimSpace(line[:at])
	rest := strings.TrimSpace(line[at+1:])
	installsAt := strings.LastIndex(rest, " installs")
	if installsAt < 0 {
		return SkillResult{}, false
	}
	beforeInstalls := strings.TrimSpace(rest[:installsAt])
	fields := strings.Fields(beforeInstalls)
	if len(fields) < 2 {
		return SkillResult{}, false
	}
	installToken := fields[len(fields)-1]
	slug := strings.Join(fields[:len(fields)-1], " ")
	if source == "" || slug == "" {
		return SkillResult{}, false
	}
	installs := parseInstalls(installToken)
	return SkillResult{
		ID:       joinSkillID(source, slug),
		Slug:     slug,
		Name:     slug,
		Source:   source,
		Registry: SkillsSH,
		Installs: installs,
		URL:      skillsSHWebURL(source, slug),
	}, true
}

func parseSkillsSHURLLine(line string) string {
	slog.Debug("parsing skills.sh url line")
	line = strings.TrimSpace(line)
	idx := strings.Index(line, "https://skills.sh/")
	if idx < 0 {
		return ""
	}
	return strings.Fields(line[idx:])[0]
}

func parseInstalls(raw string) int {
	slog.Debug("parsing installs count", "raw", raw)
	raw = strings.TrimSpace(strings.ToUpper(raw))
	multiplier := 1.0
	switch {
	case strings.HasSuffix(raw, "K"):
		multiplier = 1000
		raw = strings.TrimSuffix(raw, "K")
	case strings.HasSuffix(raw, "M"):
		multiplier = 1000000
		raw = strings.TrimSuffix(raw, "M")
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
	if err != nil {
		return 0
	}
	return int(math.Round(n * multiplier))
}

func stripANSI(s string) string {
	slog.Debug("stripping ansi escapes", "bytes", len(s))
	s = ansiEscapeRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func joinSkillID(source, skill string) string {
	slog.Debug("joining skills.sh skill id", "source", source, "skill", skill)
	return strings.Trim(strings.Trim(source, "/")+"/"+strings.Trim(skill, "/"), "/")
}

func skillsSHWebURL(source, skill string) string {
	slog.Debug("building skills.sh web url", "source", source, "skill", skill)
	id := joinSkillID(source, skill)
	if id == "" {
		return ""
	}
	return "https://skills.sh/" + id
}

func firstNonEmpty(values ...string) string {
	slog.Debug("choosing first non-empty string", "count", len(values))
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

var _ commandRunner = execCommandRunner{}
