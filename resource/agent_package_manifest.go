package resource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentPackageManifest is the Ergo-specific contract embedded in package.json.
// Pi-only resource packages remain supported and do not need this section.
type AgentPackageManifest struct {
	EntryAgent        string   `json:"entryAgent"`
	AgentDependencies []string `json:"agentDependencies"`
	RequiredTools     []string `json:"requiredTools"`
	OptionalTools     []string `json:"optionalTools,omitempty"`
}

func isAgentPackageRoot(root string) bool {
	info, err := os.Stat(filepath.Join(root, "package.json"))
	return err == nil && !info.IsDir()
}

func resolvePackageFile(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("package resource path must be relative")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	if !inside(root, candidate) {
		return "", fmt.Errorf("package resource escapes package root: %s", relative)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	canonicalCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !inside(canonicalRoot, canonicalCandidate) {
		return "", fmt.Errorf("package resource symlink escapes package root: %s", relative)
	}
	info, err := os.Stat(canonicalCandidate)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("package resource is not a regular file: %s", relative)
	}
	return canonicalCandidate, nil
}

func loadAgentPackageManifest(root string) (AgentPackageManifest, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if os.IsNotExist(err) {
		return AgentPackageManifest{}, false, nil
	}
	if err != nil {
		return AgentPackageManifest{}, false, err
	}
	var raw struct {
		Ergo        json.RawMessage `json:"ergo"`
		LegacyChico json.RawMessage `json:"chico"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentPackageManifest{}, false, fmt.Errorf("decode package.json: %w", err)
	}
	manifestData := raw.Ergo
	if len(manifestData) == 0 || string(manifestData) == "null" {
		manifestData = raw.LegacyChico
	}
	if len(manifestData) == 0 || string(manifestData) == "null" {
		return AgentPackageManifest{}, false, nil
	}
	var manifest AgentPackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return AgentPackageManifest{}, false, fmt.Errorf("decode package.json ergo manifest: %w", err)
	}
	return manifest, true, nil
}

// AgentPackageManifestForProfile returns the package contract that owns an
// Agent profile. The bool is false for embedded, user, project, and Pi-only
// profiles without a Ergo Agent Package manifest.
func AgentPackageManifestForProfile(profilePath string) (AgentPackageManifest, bool, error) {
	if profilePath == "" {
		return AgentPackageManifest{}, false, nil
	}
	root := agentResourceRoot(profilePath)
	manifest, ok, err := loadAgentPackageManifest(root)
	if err != nil || !ok {
		return manifest, ok, err
	}
	relative, err := filepath.Rel(root, profilePath)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return AgentPackageManifest{}, false, errors.New("Agent profile is outside its package root")
	}
	if _, err := resolvePackageFile(root, filepath.ToSlash(relative)); err != nil {
		return AgentPackageManifest{}, false, fmt.Errorf("validate Agent profile path: %w", err)
	}
	if err := ValidateAgentPackage(root); err != nil {
		return AgentPackageManifest{}, false, err
	}
	return manifest, true, nil
}

// AgentPackageRootForProfile returns the validated Ergo package root owning a
// profile. The bool is false for non-package and Pi-only profiles.
func AgentPackageRootForProfile(profilePath string) (string, bool, error) {
	if _, ok, err := AgentPackageManifestForProfile(profilePath); err != nil || !ok {
		return "", ok, err
	}
	return agentResourceRoot(profilePath), true, nil
}

// AgentInPackage resolves an Agent strictly within one validated package,
// preventing another installed package from shadowing a bundled dependency.
func AgentInPackage(root, name string) (Agent, error) {
	if err := ValidateAgentPackage(root); err != nil {
		return Agent{}, err
	}
	patterns := packageManifestEntries(root, "agents")
	for _, path := range applyPackagePatterns(discoverPackageResources(root, "agents"), patterns, root) {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		safePath, err := resolvePackageFile(root, filepath.ToSlash(relative))
		if err != nil {
			continue
		}
		definition, err := loadAgentDefinition(safePath, "")
		if err == nil && definition.Name == name {
			return definition, nil
		}
	}
	return Agent{}, fmt.Errorf("Agent %q not found in package %s", name, root)
}

// AgentRequiredTools returns the tools that must be supplied by the host for a
// profile. Tools explicitly listed in optional-tools are deliberately omitted.
func AgentRequiredTools(agent Agent) []string {
	var required []string
	for _, name := range agent.Tools {
		if !contains(agent.OptionalTools, name) {
			required = append(required, name)
		}
	}
	return required
}

// ValidateAgentPackage verifies a Ergo Agent Package before it is installed.
// Generic Pi resource packages without an ergo manifest are left untouched.
func ValidateAgentPackage(root string) error {
	manifest, ok, err := loadAgentPackageManifest(root)
	if err != nil || !ok {
		return err
	}
	if !packageAgentNamePattern.MatchString(manifest.EntryAgent) {
		return errors.New("ergo.entryAgent is missing or invalid")
	}
	for field, values := range map[string][]string{
		"agentDependencies": manifest.AgentDependencies,
		"requiredTools":     manifest.RequiredTools,
		"optionalTools":     manifest.OptionalTools,
	} {
		seen := map[string]bool{}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || seen[value] {
				return fmt.Errorf("ergo.%s contains an empty or duplicate value", field)
			}
			seen[value] = true
		}
	}

	patterns := packageManifestEntries(root, "agents")
	if len(patterns) == 0 {
		return errors.New("Ergo Agent Package must declare pi.agents")
	}
	paths := applyPackagePatterns(discoverPackageResources(root, "agents"), patterns, root)
	agents := map[string]Agent{}
	for _, path := range paths {
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		safePath, safeErr := resolvePackageFile(root, filepath.ToSlash(relative))
		if safeErr != nil {
			return fmt.Errorf("validate packaged Agent %s: %w", relative, safeErr)
		}
		definition, loadErr := loadAgentDefinition(safePath, "")
		if loadErr != nil {
			return fmt.Errorf("load packaged Agent %s: %w", relative, loadErr)
		}
		if _, duplicate := agents[definition.Name]; duplicate {
			return fmt.Errorf("duplicate packaged Agent name %q", definition.Name)
		}
		agents[definition.Name] = definition
	}
	if _, exists := agents[manifest.EntryAgent]; !exists {
		return fmt.Errorf("ergo.entryAgent %q is not exported by pi.agents", manifest.EntryAgent)
	}

	expectedDependencies := make([]string, 0, len(agents)-1)
	requiredSet, optionalSet := map[string]bool{}, map[string]bool{}
	for name, definition := range agents {
		if name != manifest.EntryAgent {
			expectedDependencies = append(expectedDependencies, name)
		}
		if contains(definition.Delegates, "*") {
			return fmt.Errorf("packaged Agent %q must use a frozen delegates allowlist", name)
		}
		for _, delegate := range definition.Delegates {
			target, exists := agents[delegate]
			if !exists {
				return fmt.Errorf("packaged Agent %q delegates to undeclared Agent %q", name, delegate)
			}
			if !CanDelegateTo(definition, target) {
				return fmt.Errorf("packaged Agent %q cannot delegate to %q", name, delegate)
			}
		}
		for _, tool := range definition.Tools {
			if contains(definition.OptionalTools, tool) {
				optionalSet[tool] = true
			} else {
				requiredSet[tool] = true
			}
		}
		if definition.SystemPrompt == "" {
			return fmt.Errorf("packaged Agent %q must declare a package-local system-prompt", name)
		}
		if _, promptErr := resolvePackageFile(root, definition.SystemPrompt); promptErr != nil {
			return fmt.Errorf("Agent %q system-prompt: %w", name, promptErr)
		}
	}
	sort.Strings(expectedDependencies)
	actualDependencies := append([]string(nil), manifest.AgentDependencies...)
	sort.Strings(actualDependencies)
	if strings.Join(expectedDependencies, "\x00") != strings.Join(actualDependencies, "\x00") {
		return fmt.Errorf("ergo.agentDependencies=%v, expected %v", manifest.AgentDependencies, expectedDependencies)
	}
	expectedRequired := sortedSet(requiredSet)
	for name := range requiredSet {
		delete(optionalSet, name)
	}
	expectedOptional := sortedSet(optionalSet)
	if !sameStrings(manifest.RequiredTools, expectedRequired) {
		return fmt.Errorf("ergo.requiredTools=%v, expected %v", manifest.RequiredTools, expectedRequired)
	}
	if !sameStrings(manifest.OptionalTools, expectedOptional) {
		return fmt.Errorf("ergo.optionalTools=%v, expected %v", manifest.OptionalTools, expectedOptional)
	}
	return nil
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
