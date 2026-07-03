package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"gaal/internal/config"
	"gaal/internal/core/agent"
	"gaal/internal/engine"
	"gaal/internal/engine/render"
	skillreg "gaal/internal/registry"
	"gaal/internal/telemetry"
)

var (
	skillRegistryFlag string
	skillSearchLimit  int

	skillInstallAgents  string
	skillInstallGlobal  bool
	skillInstallProject bool
	skillInstallNoSync  bool
	skillInstallYes     bool
)

var skillCmd = &cobra.Command{
	Use:         "skill",
	Short:       "Search and install registry-backed skills",
	Annotations: map[string]string{"config": "optional"},
}

var skillSearchCmd = &cobra.Command{
	Use:          "search <query>",
	Short:        "Search registry-backed skills",
	SilenceUsage: true,
	Annotations:  map[string]string{"config": "optional"},
	Args:         cobra.ExactArgs(1),
	RunE:         runSkillSearch,
}

var skillInstallCmd = &cobra.Command{
	Use:          "install <ref>",
	Short:        "Install a registry-backed skill",
	SilenceUsage: true,
	Annotations:  map[string]string{"config": "optional"},
	Args:         cobra.ExactArgs(1),
	RunE:         runSkillInstall,
}

func init() {
	slog.Debug("initialising skill commands")
	skillSearchCmd.Flags().StringVar(&skillRegistryFlag, "registry", skillreg.SkillsSH, "registry to search")
	skillSearchCmd.Flags().IntVar(&skillSearchLimit, "limit", 10, "maximum search results to return (1-100)")

	skillInstallCmd.Flags().StringVar(&skillRegistryFlag, "registry", skillreg.SkillsSH, "registry to resolve")
	skillInstallCmd.Flags().StringVar(&skillInstallAgents, "agents", "", "comma-separated target agents, or *")
	skillInstallCmd.Flags().BoolVar(&skillInstallGlobal, "global", false, "install into global agent skill directories")
	skillInstallCmd.Flags().BoolVar(&skillInstallProject, "project", false, "install into project-local agent skill directories")
	skillInstallCmd.Flags().BoolVar(&skillInstallNoSync, "no-sync", false, "update config without running sync")
	skillInstallCmd.Flags().BoolVar(&skillInstallYes, "yes", false, "confirm prompts automatically")

	skillCmd.AddCommand(skillSearchCmd, skillInstallCmd)
	rootCmd.AddCommand(skillCmd)
}

func runSkillSearch(_ *cobra.Command, args []string) error {
	slog.Debug("running skill search", "query", args[0], "registry", skillRegistryFlag, "limit", skillSearchLimit)
	telemetry.Track("skill-search")
	if skillSearchLimit < 1 || skillSearchLimit > 100 {
		return fmt.Errorf("--limit must be between 1 and 100")
	}
	reg, err := skillreg.ForName(skillRegistryFlag)
	if err != nil {
		return err
	}
	results, err := reg.Search(context.Background(), args[0], skillreg.SearchOptions{Limit: skillSearchLimit})
	if err != nil {
		telemetry.TrackError("skill-search", err)
		return err
	}
	return renderSkillSearch(os.Stdout, results, engine.OutputFormat(effectiveOutputFormat()))
}

func runSkillInstall(_ *cobra.Command, args []string) error {
	slog.Debug("running skill install", "ref", args[0], "registry", skillRegistryFlag)
	telemetry.Track("skill-install")
	if skillInstallGlobal && skillInstallProject {
		return fmt.Errorf("cannot use --global and --project together")
	}
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	if !isTTY && !skillInstallYes {
		return fmt.Errorf("non-interactive install requires --yes")
	}
	ref, err := skillreg.ParseRef(args[0])
	if err != nil {
		return err
	}
	if ref.Registry != "" && skillRegistryFlag != "" && ref.Registry != skillRegistryFlag {
		return fmt.Errorf("registry mismatch: ref uses %s but --registry uses %s", ref.Registry, skillRegistryFlag)
	}
	registryName := skillRegistryFlag
	if ref.Registry != "" {
		registryName = ref.Registry
	}
	reg, err := skillreg.ForName(registryName)
	if err != nil {
		return err
	}
	agents, agentsProvided, err := resolveSkillInstallAgents(skillInstallAgents, isTTY && !skillInstallYes)
	if err != nil {
		return err
	}
	resolved, err := resolveSkillForInstall(context.Background(), reg, ref, isTTY)
	if err != nil {
		telemetry.TrackError("skill-install", err)
		return err
	}
	if err := ensureResolvedSkillSupported(resolved); err != nil {
		return err
	}
	if !agentsProvided {
		agents, err = promptSkillInstallAgents()
		if err != nil {
			return &ExitCodeError{Code: 130, Cause: err}
		}
	}
	global := true
	if skillInstallProject {
		global = false
	}
	entry := config.ConfigSkill{
		Source:   normalizedInstallSource(resolved),
		Registry: reg.Name(),
		Agents:   agents,
		Global:   global,
		Select:   []string{resolved.Select},
	}
	action := "update " + cfgFile + " and run gaal sync"
	if skillInstallNoSync {
		action = "update " + cfgFile + " only; sync skipped"
	}
	if !skillInstallYes {
		ok, err := confirmSkillInstall(resolved, entry, action)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stdout, "install cancelled")
			return nil
		}
	}
	if err := validateExistingConfigForInstall(cfgFile); err != nil {
		return err
	}
	edit, err := config.UpsertSkillInstall(cfgFile, entry)
	if err != nil {
		telemetry.TrackError("skill-install", err)
		return err
	}
	reportConfigEdit(edit, resolved)
	if skillInstallNoSync {
		fmt.Fprintln(os.Stdout, "sync skipped")
		return nil
	}
	reloaded, err := config.LoadChain(cfgFile)
	if err != nil {
		return fmt.Errorf("loading updated config: %w", err)
	}
	if err := runInstallSync(reloaded); err != nil {
		telemetry.TrackError("skill-install-sync", err)
		return &ExitCodeError{Code: 2, Cause: err}
	}
	telemetry.Track("skill-install")
	return nil
}

func renderSkillSearch(w io.Writer, results []skillreg.SkillResult, format engine.OutputFormat) error {
	slog.Debug("rendering skill search", "results", len(results), "format", format)
	if format == engine.FormatJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Skills []skillreg.SkillResult `json:"skills"`
		}{results})
	}
	data := pterm.TableData{{"SKILL", "SOURCE", "REGISTRY", "INSTALLS", "DESCRIPTION"}}
	for _, r := range results {
		data = append(data, []string{
			truncateSearchCell(firstNonEmpty(r.Name, r.Slug), 28),
			truncateSearchCell(r.Source, 28),
			r.Registry,
			formatInstalls(r.Installs),
			truncateSearchCell(r.Description, 60),
		})
	}
	return render.BoxedTable(w, data)
}

func resolveSkillInstallAgents(raw string, isTTY bool) ([]string, bool, error) {
	slog.Debug("resolving skill install agents", "raw", raw, "isTTY", isTTY)
	if strings.TrimSpace(raw) == "" {
		if isTTY {
			return nil, false, nil
		}
		return []string{"*"}, true, nil
	}
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	var agents []string
	hasWildcard := false
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		if name == "*" {
			hasWildcard = true
		} else if _, ok := agent.Lookup(name); !ok {
			return nil, true, fmt.Errorf("unknown agent %q", name)
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			agents = append(agents, name)
		}
	}
	if len(agents) == 0 {
		return nil, true, fmt.Errorf("--agents must not be empty")
	}
	if hasWildcard && len(agents) > 1 {
		return nil, true, fmt.Errorf("--agents cannot combine \"*\" with explicit agent names")
	}
	return agents, true, nil
}

func resolveSkillForInstall(ctx context.Context, reg skillreg.Registry, ref skillreg.SkillRef, isTTY bool) (skillreg.ResolvedSkill, error) {
	slog.DebugContext(ctx, "resolving skill for install", "registry", reg.Name(), "source", ref.Source, "skill", ref.Skill, "isTTY", isTTY)
	resolved, err := reg.Resolve(ctx, ref)
	var amb *skillreg.AmbiguousError
	if !errors.As(err, &amb) {
		return resolved, err
	}
	if !isTTY {
		return skillreg.ResolvedSkill{}, formatAmbiguousInstallError(amb)
	}
	picked, err := promptAmbiguousSkill(amb.Candidates)
	if err != nil {
		return skillreg.ResolvedSkill{}, err
	}
	return reg.ResolveResult(ctx, picked)
}

func promptAmbiguousSkill(candidates []skillreg.SkillResult) (skillreg.SkillResult, error) {
	slog.Debug("prompting ambiguous skill", "candidates", len(candidates))
	labels := make([]string, 0, len(candidates))
	byLabel := map[string]skillreg.SkillResult{}
	for _, c := range candidates {
		label := fmt.Sprintf("%s/%s  (%s)", c.Source, c.Slug, firstNonEmpty(c.Name, c.Slug))
		labels = append(labels, label)
		byLabel[label] = c
	}
	choice, err := pterm.DefaultInteractiveSelect.
		WithOptions(labels).
		WithDefaultText("Which skill did you mean?").
		Show()
	if err != nil {
		return skillreg.SkillResult{}, err
	}
	return byLabel[choice], nil
}

func promptSkillInstallAgents() ([]string, error) {
	slog.Debug("prompting skill install agents")
	allLabel := `all detected agents ("*")`
	customLabel := "choose specific agents"
	choice, err := pterm.DefaultInteractiveSelect.
		WithOptions([]string{allLabel, customLabel}).
		WithDefaultText("Which agents should receive this skill?").
		Show()
	if err != nil {
		return nil, err
	}
	if choice == allLabel {
		return []string{"*"}, nil
	}
	names := agent.Names()
	selected, err := pterm.DefaultInteractiveMultiselect.
		WithOptions(names).
		WithFilter(false).
		WithDefaultText("Select target agents").
		Show()
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no agents selected")
	}
	sort.Strings(selected)
	return selected, nil
}

func confirmSkillInstall(resolved skillreg.ResolvedSkill, entry config.ConfigSkill, action string) (bool, error) {
	slog.Debug("confirming skill install", "source", entry.Source, "registry", entry.Registry)
	scope := "global agent skills"
	scopeHint := "default global; pass --project for project-local install"
	if !entry.Global {
		scope = "project agent skills"
		scopeHint = "project-local install"
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Install skill:")
	fmt.Fprintf(os.Stdout, "  %s\n", firstNonEmpty(resolved.Name, resolved.Slug, resolved.Select))
	fmt.Fprintln(os.Stdout, "Source:")
	fmt.Fprintf(os.Stdout, "  %s\n", entry.Source)
	fmt.Fprintln(os.Stdout, "Registry:")
	fmt.Fprintf(os.Stdout, "  %s\n", entry.Registry)
	fmt.Fprintln(os.Stdout, "Target:")
	fmt.Fprintf(os.Stdout, "  %s, agents: %s\n", scope, strings.Join(entry.Agents, ","))
	fmt.Fprintln(os.Stdout, "Config:")
	fmt.Fprintf(os.Stdout, "  %s\n", cfgFile)
	fmt.Fprintln(os.Stdout, "Action:")
	fmt.Fprintf(os.Stdout, "  %s\n", action)
	fmt.Fprintln(os.Stdout, "Scope:")
	fmt.Fprintf(os.Stdout, "  %s\n", scopeHint)
	fmt.Fprintln(os.Stdout)
	return pterm.DefaultInteractiveConfirm.
		WithDefaultValue(false).
		WithDefaultText("Continue?").
		Show()
}

func validateExistingConfigForInstall(path string) error {
	slog.Debug("validating existing config for install", "path", path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := config.LoadStrict(path); err != nil {
		return fmt.Errorf("loading config %q: %w", path, err)
	}
	return nil
}

func runInstallSync(cfg *config.ResolvedConfig) error {
	slog.Debug("running install sync")
	warnMissingTools(cfg.Config)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	eng := engine.NewWithOptions(cfg.Config, engineOpts)
	plan, err := eng.Plan(ctx)
	if err != nil {
		return err
	}
	if err := eng.Hooks().RunPreSync(ctx, plan); err != nil {
		fmt.Fprintln(os.Stderr, "pre-sync hook failed; aborting sync")
		return err
	}
	start := time.Now()
	if err := eng.RunOnce(ctx); err != nil {
		return err
	}
	status, err := eng.Collect(ctx)
	if err == nil {
		if rerr := render.RenderSyncBrief(os.Stdout, plan, status, time.Since(start)); rerr != nil {
			slog.Debug("rendering install sync summary failed", "err", rerr)
		}
	}
	if err := eng.Hooks().RunPostSync(ctx, plan); err != nil {
		fmt.Fprintln(os.Stderr, "post-sync hook failed")
		return err
	}
	return nil
}

func normalizedInstallSource(resolved skillreg.ResolvedSkill) string {
	slog.Debug("normalizing install source", "source", resolved.Source, "installURL", resolved.InstallURL)
	if resolved.Source != "" && resolved.SourceType == "github" {
		return resolved.Source
	}
	if resolved.Source != "" && strings.Count(resolved.Source, "/") == 1 && !strings.Contains(resolved.Source, "://") {
		return resolved.Source
	}
	return firstNonEmpty(resolved.InstallURL, resolved.Source)
}

func ensureResolvedSkillSupported(resolved skillreg.ResolvedSkill) error {
	slog.Debug("ensuring resolved skill is supported", "source", resolved.Source, "sourceType", resolved.SourceType)
	if resolved.SourceType != "" && resolved.SourceType != "github" {
		return fmt.Errorf("registry skill %q uses unsupported source type %q; v1 supports GitHub-backed skills only", firstNonEmpty(resolved.Name, resolved.Select), resolved.SourceType)
	}
	source := normalizedInstallSource(resolved)
	if source == "" {
		return fmt.Errorf("registry skill %q has no installable source", firstNonEmpty(resolved.Name, resolved.Select))
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "git@") || strings.HasPrefix(source, "ssh://") {
		return nil
	}
	if strings.Count(source, "/") == 1 {
		return nil
	}
	return fmt.Errorf("registry skill %q cannot be represented as a gaal source: %s", firstNonEmpty(resolved.Name, resolved.Select), source)
}

func reportConfigEdit(edit config.SkillInstallEditResult, resolved skillreg.ResolvedSkill) {
	slog.Debug("reporting config edit", "changed", edit.Changed, "covered", edit.AlreadyCovered)
	switch {
	case edit.AlreadyCovered:
		fmt.Fprintf(os.Stdout, "%s is already covered by existing config (select omitted)\n", firstNonEmpty(resolved.Name, resolved.Select))
	case edit.Changed:
		fmt.Fprintf(os.Stdout, "updated %s\n", cfgFile)
	default:
		fmt.Fprintf(os.Stdout, "%s is already configured\n", firstNonEmpty(resolved.Name, resolved.Select))
	}
}

func formatAmbiguousInstallError(err *skillreg.AmbiguousError) error {
	slog.Debug("formatting ambiguous install error", "query", err.Query, "candidates", len(err.Candidates))
	var b strings.Builder
	fmt.Fprintf(&b, "ambiguous skill %q; choose one of:\n", err.Query)
	for _, c := range err.Candidates {
		fmt.Fprintf(&b, "  %s/%s:%s\n", c.Registry, c.Source, c.Slug)
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

func formatInstalls(n int) string {
	slog.Debug("formatting installs", "installs", n)
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	case n > 0:
		return fmt.Sprint(n)
	default:
		return ""
	}
}

func truncateSearchCell(s string, max int) string {
	slog.Debug("truncating search cell", "max", max)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func firstNonEmpty(values ...string) string {
	slog.Debug("choosing first non-empty value", "count", len(values))
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
