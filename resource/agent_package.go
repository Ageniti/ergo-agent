package resource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

var packageAgentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// AgentPackageBuildOptions describes one independently publishable Agent
// bundle. The builder includes the entry Agent and the transitive closure of
// its explicit delegates.
type AgentPackageBuildOptions struct {
	EntryAgent  string
	PackageName string
	Version     string
	License     string
}

// AgentPackageBuildResult reports the self-contained declarative resources
// written by BuildAgentPackage.
type AgentPackageBuildResult struct {
	Root              string   `json:"root"`
	PackageName       string   `json:"packageName"`
	Version           string   `json:"version"`
	EntryAgent        string   `json:"entryAgent"`
	Agents            []string `json:"agents"`
	AgentDependencies []string `json:"agentDependencies"`
	RequiredTools     []string `json:"requiredTools"`
	OptionalTools     []string `json:"optionalTools,omitempty"`
}

// BuildAgentPackage writes an npm/git-ready Agent Package directory. It does
// not overwrite an existing destination. Exact delegates and explicit "*"
// policies are resolved against the source resource root, and every selected
// Agent receives a packaged system prompt so the bundle does not depend on the
// host's built-in Agent profiles or prompt files.
func (r Resources) BuildAgentPackage(output string, options AgentPackageBuildOptions) (AgentPackageBuildResult, error) {
	entry := strings.TrimSpace(options.EntryAgent)
	if !packageAgentNamePattern.MatchString(entry) {
		return AgentPackageBuildResult{}, errors.New("entry Agent must use letters, digits, dot, underscore, or hyphen")
	}
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = "0.1.0"
	}
	if _, err := semver.StrictNewVersion(version); err != nil {
		return AgentPackageBuildResult{}, fmt.Errorf("invalid package version %q: %w", version, err)
	}
	packageName := strings.TrimSpace(options.PackageName)
	if packageName == "" {
		packageName = "@ageniti/" + entry
	}
	license := strings.TrimSpace(options.License)
	if license == "" {
		if _, err := os.Stat(filepath.Join(r.Root, "LICENSE")); err == nil {
			license = "SEE LICENSE IN LICENSE"
		} else {
			license = "UNLICENSED"
		}
	}
	if output == "" {
		return AgentPackageBuildResult{}, errors.New("package output directory is required")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return AgentPackageBuildResult{}, err
	}
	if _, err := os.Stat(output); err == nil {
		return AgentPackageBuildResult{}, fmt.Errorf("package output already exists: %s", output)
	} else if !os.IsNotExist(err) {
		return AgentPackageBuildResult{}, err
	}

	definitions := r.AgentDefinitionsAt("", "project")
	available := make(map[string]Agent, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = definition
	}
	selected := map[string]Agent{}
	visiting := map[string]bool{}
	var collect func(string) error
	collect = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("Agent delegate cycle includes %q", name)
		}
		if _, exists := selected[name]; exists {
			return nil
		}
		definition, exists := available[name]
		if !exists {
			return fmt.Errorf("Agent dependency %q is not available in resource root %s", name, r.Root)
		}
		if !packageAgentNamePattern.MatchString(definition.Name) {
			return fmt.Errorf("Agent %q cannot be used as a package filename", definition.Name)
		}
		visiting[name] = true
		if contains(definition.Tools, "subagent") {
			targetNames := append([]string(nil), definition.Delegates...)
			if contains(targetNames, "*") {
				targetNames = targetNames[:0]
				for _, candidate := range definitions {
					if CanDelegateAgent(definition.Role, candidate.Role) {
						targetNames = append(targetNames, candidate.Name)
					}
				}
			}
			sort.Strings(targetNames)
			definition.Delegates = uniqueStrings(targetNames)
			for _, targetName := range targetNames {
				target, exists := available[targetName]
				if !exists {
					return fmt.Errorf("Agent %q delegates to missing dependency %q", name, targetName)
				}
				if !CanDelegateAgent(definition.Role, target.Role) {
					return fmt.Errorf("Agent %q cannot delegate from role %q to %q with role %q", name, definition.Role, targetName, target.Role)
				}
				if err := collect(targetName); err != nil {
					return err
				}
			}
		}
		selected[name] = definition
		visiting[name] = false
		return nil
	}
	if err := collect(entry); err != nil {
		return AgentPackageBuildResult{}, err
	}

	agentNames := make([]string, 0, len(selected))
	requiredToolSet := map[string]bool{}
	optionalToolSet := map[string]bool{}
	for name, definition := range selected {
		agentNames = append(agentNames, name)
		for _, tool := range definition.Tools {
			if contains(definition.OptionalTools, tool) {
				optionalToolSet[tool] = true
			} else {
				requiredToolSet[tool] = true
			}
		}
	}
	sort.Strings(agentNames)
	dependencies := make([]string, 0, len(agentNames)-1)
	for _, name := range agentNames {
		if name != entry {
			dependencies = append(dependencies, name)
		}
	}
	requiredTools := make([]string, 0, len(requiredToolSet))
	for name := range requiredToolSet {
		requiredTools = append(requiredTools, name)
	}
	sort.Strings(requiredTools)
	optionalTools := make([]string, 0, len(optionalToolSet))
	for name := range optionalToolSet {
		if !requiredToolSet[name] {
			optionalTools = append(optionalTools, name)
		}
	}
	sort.Strings(optionalTools)

	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return AgentPackageBuildResult{}, err
	}
	staging, err := os.MkdirTemp(parent, ".agent-package-*")
	if err != nil {
		return AgentPackageBuildResult{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Join(staging, "agents"), 0755); err != nil {
		return AgentPackageBuildResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(staging, "prompts", "system"), 0755); err != nil {
		return AgentPackageBuildResult{}, err
	}
	for _, name := range agentNames {
		definition := selected[name]
		sourcePrompt, err := r.agentSystemPromptSource(definition)
		if err != nil {
			return AgentPackageBuildResult{}, err
		}
		promptRelative := filepath.ToSlash(filepath.Join("prompts", "system", name+".md"))
		promptData, err := os.ReadFile(sourcePrompt)
		if err != nil {
			return AgentPackageBuildResult{}, fmt.Errorf("read system prompt for Agent %q: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(staging, filepath.FromSlash(promptRelative)), promptData, 0644); err != nil {
			return AgentPackageBuildResult{}, err
		}
		definition.SystemPrompt = promptRelative
		definition.Path = ""
		if err := os.WriteFile(filepath.Join(staging, "agents", name+".md"), []byte(renderAgentProfile(definition)), 0644); err != nil {
			return AgentPackageBuildResult{}, err
		}
	}

	manifest := struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		License string   `json:"license"`
		Files   []string `json:"files"`
		Pi      struct {
			Agents []string `json:"agents"`
		} `json:"pi"`
		Ergo struct {
			EntryAgent        string   `json:"entryAgent"`
			AgentDependencies []string `json:"agentDependencies"`
			RequiredTools     []string `json:"requiredTools"`
			OptionalTools     []string `json:"optionalTools,omitempty"`
		} `json:"ergo"`
	}{Name: packageName, Version: version, License: license, Files: []string{"agents", "prompts/system"}}
	if license == "SEE LICENSE IN LICENSE" {
		for _, name := range []string{"LICENSE", "LICENSE-COMMERCIAL.md", "NOTICE"} {
			source := filepath.Join(r.Root, name)
			data, readErr := os.ReadFile(source)
			if os.IsNotExist(readErr) {
				continue
			}
			if readErr != nil {
				return AgentPackageBuildResult{}, readErr
			}
			if err := os.WriteFile(filepath.Join(staging, name), data, 0644); err != nil {
				return AgentPackageBuildResult{}, err
			}
			manifest.Files = append(manifest.Files, name)
		}
	}
	manifest.Pi.Agents = []string{"agents/*.md"}
	manifest.Ergo.EntryAgent = entry
	manifest.Ergo.AgentDependencies = dependencies
	manifest.Ergo.RequiredTools = requiredTools
	manifest.Ergo.OptionalTools = optionalTools
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return AgentPackageBuildResult{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "package.json"), append(data, '\n'), 0644); err != nil {
		return AgentPackageBuildResult{}, err
	}
	readme := fmt.Sprintf("# %s\n\nEntry Agent: `%s`\n\nIncluded Agent dependencies: %s\n\nRequired host tools: %s\n\nOptional host tools: %s\n", packageName, entry, markdownList(dependencies), markdownList(requiredTools), markdownList(optionalTools))
	if err := os.WriteFile(filepath.Join(staging, "README.md"), []byte(readme), 0644); err != nil {
		return AgentPackageBuildResult{}, err
	}
	if err := ValidateAgentPackage(staging); err != nil {
		return AgentPackageBuildResult{}, fmt.Errorf("validate generated Agent Package: %w", err)
	}
	if err := os.Rename(staging, output); err != nil {
		return AgentPackageBuildResult{}, err
	}
	return AgentPackageBuildResult{
		Root:              output,
		PackageName:       packageName,
		Version:           version,
		EntryAgent:        entry,
		Agents:            agentNames,
		AgentDependencies: dependencies,
		RequiredTools:     requiredTools,
		OptionalTools:     optionalTools,
	}, nil
}

func (r Resources) agentSystemPromptSource(definition Agent) (string, error) {
	path := definition.SystemPrompt
	if path == "" {
		path = "prompts/system/coding-agent.md"
	}
	if filepath.IsAbs(path) {
		if definition.Path != "" && isAgentPackageRoot(agentResourceRoot(definition.Path)) {
			return "", errors.New("packaged system prompt must be relative to the package root")
		}
		return path, nil
	}
	if definition.Path != "" {
		resourceRoot := agentResourceRoot(definition.Path)
		candidate := filepath.Join(resourceRoot, filepath.FromSlash(path))
		if isAgentPackageRoot(resourceRoot) {
			resolved, err := resolvePackageFile(resourceRoot, path)
			if err != nil {
				if definition.SystemPrompt != "" {
					return "", err
				}
			} else {
				candidate = resolved
			}
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	candidate := filepath.Join(r.Root, filepath.FromSlash(path))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("system prompt %q for Agent %q was not found", path, definition.Name)
}

func renderAgentProfile(definition Agent) string {
	var lines []string
	add := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, key+": "+strconv.Quote(value))
		}
	}
	add("name", definition.Name)
	add("description", definition.Description)
	add("role", string(definition.Role))
	if len(definition.Tools) > 0 {
		add("tools", strings.Join(definition.Tools, ", "))
	}
	if len(definition.OptionalTools) > 0 {
		add("optional-tools", strings.Join(definition.OptionalTools, ", "))
	}
	if len(definition.Delegates) > 0 {
		add("delegates", strings.Join(definition.Delegates, ", "))
	}
	add("provider", definition.Provider)
	add("model", definition.Model)
	add("thinking-level", definition.ThinkingLevel)
	add("system-prompt", definition.SystemPrompt)
	return "---\n" + strings.Join(lines, "\n") + "\n---\n\n" + strings.TrimSpace(definition.Body) + "\n"
}

func markdownList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "`" + value + "`"
	}
	return strings.Join(quoted, ", ")
}
