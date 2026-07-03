package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

const SkillsSH = "skills.sh"

// SearchOptions controls registry search.
type SearchOptions struct {
	Limit int
}

// SkillRef is a parsed install reference.
type SkillRef struct {
	Registry string
	Source   string
	Skill    string
}

// SkillResult is one registry search result.
type SkillResult struct {
	ID          string    `json:"id,omitempty"`
	Slug        string    `json:"slug,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Source      string    `json:"source"`
	Registry    string    `json:"registry"`
	Path        string    `json:"path,omitempty"`
	Installs    int       `json:"installs,omitempty"`
	Verified    bool      `json:"verified,omitempty"`
	Audited     bool      `json:"audited,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	URL         string    `json:"url,omitempty"`
	InstallURL  string    `json:"install_url,omitempty"`
	SourceType  string    `json:"source_type,omitempty"`
}

// ResolvedSkill carries the config-ready install intent.
type ResolvedSkill struct {
	SkillResult
	Select string
}

// Registry searches and resolves skill references.
type Registry interface {
	Name() string
	Search(ctx context.Context, query string, opts SearchOptions) ([]SkillResult, error)
	Resolve(ctx context.Context, ref SkillRef) (ResolvedSkill, error)
	ResolveResult(ctx context.Context, result SkillResult) (ResolvedSkill, error)
}

// ErrNotFound is returned when a registry ref has no candidates.
var ErrNotFound = errors.New("skill not found")

// AmbiguousError reports multiple matching candidates.
type AmbiguousError struct {
	Query      string
	Candidates []SkillResult
}

func (e *AmbiguousError) Error() string {
	slog.Debug("formatting ambiguous registry error", "query", e.Query, "candidates", len(e.Candidates))
	return fmt.Sprintf("ambiguous skill %q (%d candidates)", e.Query, len(e.Candidates))
}

// ParseRef parses a registry install reference.
func ParseRef(raw string) (SkillRef, error) {
	slog.Debug("parsing skill registry ref", "ref", raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SkillRef{}, fmt.Errorf("skill reference is required")
	}
	var ref SkillRef
	if before, after, ok := strings.Cut(raw, ":"); ok {
		if after == "" {
			return SkillRef{}, fmt.Errorf("skill reference %q is missing skill name after ':'", raw)
		}
		ref.Skill = after
		if reg, rest, ok := strings.Cut(before, "/"); ok && isKnownRegistry(reg) {
			ref.Registry = reg
			ref.Source = rest
		} else {
			ref.Source = before
		}
		if ref.Source == "" {
			return SkillRef{}, fmt.Errorf("skill reference %q is missing source", raw)
		}
		return ref, nil
	}
	if reg, rest, ok := strings.Cut(raw, "/"); ok && isKnownRegistry(reg) {
		if rest == "" {
			return SkillRef{}, fmt.Errorf("skill reference %q is missing skill name", raw)
		}
		ref.Registry = reg
		ref.Skill = rest
		return ref, nil
	}
	ref.Skill = raw
	return ref, nil
}

// ForName returns the configured registry provider.
func ForName(name string) (Registry, error) {
	slog.Debug("resolving registry provider", "name", name)
	switch name {
	case "", SkillsSH:
		return NewSkillsSH(nil), nil
	default:
		return nil, fmt.Errorf("unknown registry %q; supported registries: %s", name, SkillsSH)
	}
}

func isKnownRegistry(name string) bool {
	slog.Debug("checking known registry", "name", name)
	return name == SkillsSH
}

func exactMatches(query string, results []SkillResult) []SkillResult {
	slog.Debug("filtering exact registry matches", "query", query, "results", len(results))
	q := strings.ToLower(query)
	var out []SkillResult
	for _, r := range results {
		if strings.ToLower(r.Slug) == q || strings.ToLower(r.Name) == q || strings.ToLower(r.ID) == q || strings.HasSuffix(strings.ToLower(r.ID), "/"+q) {
			out = append(out, r)
		}
	}
	return out
}

func sortCandidates(candidates []SkillResult) {
	slog.Debug("sorting registry candidates", "count", len(candidates))
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Source != candidates[j].Source {
			return candidates[i].Source < candidates[j].Source
		}
		return candidates[i].Slug < candidates[j].Slug
	})
}
