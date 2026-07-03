package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"gaal/internal/core/io/secfile"

	"gopkg.in/yaml.v3"
)

// SkillInstallEditResult describes the effect of a registry install YAML edit.
type SkillInstallEditResult struct {
	Changed        bool
	AlreadyCovered bool
	Created        bool
}

// UpsertSkillInstall inserts or updates a registry-backed skill entry in path.
func UpsertSkillInstall(path string, skill ConfigSkill) (SkillInstallEditResult, error) {
	slog.Debug("upserting skill install in config", "path", path, "source", skill.Source, "registry", skill.Registry)
	var result SkillInstallEditResult
	root, created, err := loadConfigNodeForEdit(path)
	if err != nil {
		return result, err
	}
	result.Created = created

	mapping, err := documentMapping(root)
	if err != nil {
		return result, err
	}

	if patchTopLevelDefaults(mapping, created) {
		result.Changed = true
	}

	skillsNode := mappingValue(mapping, "skills")
	if skillsNode == nil {
		skillsNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		appendMappingValue(mapping, "skills", skillsNode)
		result.Changed = true
	}
	if skillsNode.Kind != yaml.SequenceNode {
		return result, fmt.Errorf("skills must be a sequence")
	}

	changed, covered, err := upsertSkillNode(skillsNode, skill)
	if err != nil {
		return result, err
	}
	result.Changed = result.Changed || changed
	result.AlreadyCovered = covered

	if !result.Changed {
		return result, nil
	}
	if err := writeConfigNode(path, root); err != nil {
		return result, err
	}
	return result, nil
}

func loadConfigNodeForEdit(path string) (*yaml.Node, bool, error) {
	slog.Debug("loading config yaml node for edit", "path", path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		root := &yaml.Node{Kind: yaml.DocumentNode}
		root.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		return root, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parsing YAML: %w", err)
	}
	return &root, false, nil
}

func documentMapping(root *yaml.Node) (*yaml.Node, error) {
	slog.Debug("resolving yaml document mapping")
	if root == nil {
		return nil, fmt.Errorf("yaml root is nil")
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			root.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml root is not a mapping")
	}
	return root, nil
}

func patchTopLevelDefaults(mapping *yaml.Node, created bool) bool {
	slog.Debug("patching top-level defaults", "created", created)
	changed := false
	if mappingValue(mapping, "schema") == nil {
		appendMappingValue(mapping, "schema", scalarNode("!!int", "1"))
		changed = true
	}
	if created && mappingValue(mapping, "repositories") == nil {
		appendMappingValue(mapping, "repositories", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		changed = true
	}
	if created && mappingValue(mapping, "mcps") == nil {
		appendMappingValue(mapping, "mcps", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
		changed = true
	}
	return changed
}

func upsertSkillNode(skillsNode *yaml.Node, skill ConfigSkill) (bool, bool, error) {
	slog.Debug("upserting skill yaml node", "source", skill.Source, "registry", skill.Registry)
	for _, item := range skillsNode.Content {
		if item.Kind != yaml.MappingNode {
			return false, false, fmt.Errorf("skills entries must be mappings")
		}
		if !skillNodeIdentityMatches(item, skill) {
			continue
		}
		return mergeSkillSelect(item, skill.Select)
	}
	skillsNode.Content = append(skillsNode.Content, skillToNode(skill))
	return true, false, nil
}

func skillNodeIdentityMatches(node *yaml.Node, skill ConfigSkill) bool {
	slog.Debug("comparing skill node identity", "source", skill.Source, "registry", skill.Registry)
	source := scalarValue(mappingValue(node, "source"))
	registry := scalarValue(mappingValue(node, "registry"))
	targetSubdir := scalarValue(mappingValue(node, "target_subdir"))
	global := boolValue(mappingValue(node, "global"))
	agents := agentsFromNode(mappingValue(node, "agents"))
	return source == skill.Source &&
		registry == skill.Registry &&
		normalizedAgentsIdentity(agents) == normalizedAgentsIdentity(skill.Agents) &&
		global == skill.Global &&
		filepath.ToSlash(filepath.Clean(targetSubdir)) == filepath.ToSlash(filepath.Clean(skill.TargetSubdir))
}

func mergeSkillSelect(node *yaml.Node, selectNames []string) (bool, bool, error) {
	slog.Debug("merging skill select", "count", len(selectNames))
	selectNode := mappingValue(node, "select")
	if selectNode == nil || sequenceLen(selectNode) == 0 {
		return false, true, nil
	}
	if selectNode.Kind != yaml.SequenceNode {
		return false, false, fmt.Errorf("select must be a sequence")
	}
	existing := make([]string, 0, len(selectNode.Content)+len(selectNames))
	seen := map[string]struct{}{}
	for _, item := range selectNode.Content {
		value := scalarValue(item)
		existing = append(existing, value)
		seen[value] = struct{}{}
	}
	changed := false
	for _, name := range selectNames {
		if _, ok := seen[name]; ok {
			continue
		}
		existing = append(existing, name)
		seen[name] = struct{}{}
		changed = true
	}
	if !changed {
		return false, false, nil
	}
	sort.Strings(existing)
	selectNode.Content = selectNode.Content[:0]
	for _, name := range existing {
		selectNode.Content = append(selectNode.Content, scalarNode("!!str", name))
	}
	return true, false, nil
}

func skillToNode(skill ConfigSkill) *yaml.Node {
	slog.Debug("building skill yaml node", "source", skill.Source, "registry", skill.Registry)
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendMappingValue(node, "source", scalarNode("!!str", skill.Source))
	if skill.Registry != "" {
		appendMappingValue(node, "registry", scalarNode("!!str", skill.Registry))
	}
	if len(skill.Select) > 0 {
		names := append([]string(nil), skill.Select...)
		sort.Strings(names)
		selectNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, name := range names {
			selectNode.Content = append(selectNode.Content, scalarNode("!!str", name))
		}
		appendMappingValue(node, "select", selectNode)
	}
	agentsNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, agent := range skill.Agents {
		agentsNode.Content = append(agentsNode.Content, scalarNode("!!str", agent))
	}
	appendMappingValue(node, "agents", agentsNode)
	appendMappingValue(node, "global", scalarNode("!!bool", strconv.FormatBool(skill.Global)))
	if skill.TargetSubdir != "" {
		appendMappingValue(node, "target_subdir", scalarNode("!!str", skill.TargetSubdir))
	}
	return node
}

func writeConfigNode(path string, root *yaml.Node) error {
	slog.Debug("writing edited config yaml node", "path", path)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encoding YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("closing YAML encoder: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return fmt.Errorf("creating parent directory: %w", err)
	}
	if err := secfile.Write(path, buf.Bytes()); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := LoadStrict(path); err != nil {
		return fmt.Errorf("validating edited config: %w", err)
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	slog.Debug("looking up yaml mapping value", "key", key)
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func appendMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	slog.Debug("appending yaml mapping value", "key", key)
	mapping.Content = append(mapping.Content, scalarNode("!!str", key), value)
}

func scalarNode(tag, value string) *yaml.Node {
	slog.Debug("building scalar yaml node", "tag", tag)
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func scalarValue(node *yaml.Node) string {
	slog.Debug("reading scalar yaml value")
	if node == nil {
		return ""
	}
	return node.Value
}

func boolValue(node *yaml.Node) bool {
	slog.Debug("reading bool yaml value")
	if node == nil {
		return false
	}
	v, _ := strconv.ParseBool(node.Value)
	return v
}

func agentsFromNode(node *yaml.Node) []string {
	slog.Debug("reading agents from yaml node")
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, item.Value)
			}
		}
		return out
	default:
		return nil
	}
}

func sequenceLen(node *yaml.Node) int {
	slog.Debug("reading yaml sequence length")
	if node == nil || node.Kind != yaml.SequenceNode {
		return 0
	}
	return len(node.Content)
}
