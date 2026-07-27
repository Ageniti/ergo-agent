package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// PackageSource accepts Pi's string and filtered object settings forms.
type PackageSource struct {
	Source                              string
	Autoload                            *bool
	Extensions, Agents, Skills, Prompts []string
}

func (p *PackageSource) UnmarshalJSON(data []byte) error {
	var source string
	if json.Unmarshal(data, &source) == nil {
		p.Source = source
		return nil
	}
	var value struct {
		Source     string   `json:"source"`
		Autoload   *bool    `json:"autoload"`
		Extensions []string `json:"extensions"`
		Skills     []string `json:"skills"`
		Prompts    []string `json:"prompts"`
		Agents     []string `json:"agents"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	p.Source, p.Autoload, p.Extensions, p.Skills, p.Prompts, p.Agents = value.Source, value.Autoload, value.Extensions, value.Skills, value.Prompts, value.Agents
	return nil
}

type PackageDiagnostic struct{ Source, Scope, Message string }
type configuredPackage struct {
	PackageSource
	Scope, Base string
}

type ConfiguredPackage = configuredPackage

type packageSettings struct {
	Packages []PackageSource `json:"packages"`
}

func configuredPackages(cwd string) ([]configuredPackage, []PackageDiagnostic) {
	return configuredPackagesScope(cwd, true)
}

func configuredPackagesScope(cwd string, includeProject bool) ([]configuredPackage, []PackageDiagnostic) {
	var result []configuredPackage
	var diagnostics []PackageDiagnostic
	files := []struct{ path, scope, base string }{{filepath.Join(agentConfigDir(), "settings.json"), "user", agentConfigDir()}}
	if includeProject {
		if dir := nearest(cwd, ".pi"); dir != "" {
			files = append(files, struct{ path, scope, base string }{filepath.Join(dir, "settings.json"), "project", filepath.Dir(dir)})
		}
	}
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, PackageDiagnostic{Scope: file.scope, Message: err.Error()})
			continue
		}
		var settings packageSettings
		if err := json.Unmarshal(data, &settings); err != nil {
			diagnostics = append(diagnostics, PackageDiagnostic{Scope: file.scope, Message: "decode " + file.path + ": " + err.Error()})
			continue
		}
		for _, source := range settings.Packages {
			result = append(result, configuredPackage{source, file.scope, file.base})
		}
	}
	return result, diagnostics
}

func packageRoot(source PackageSource, base string) string {
	value := os.ExpandEnv(strings.TrimSpace(source.Source))
	if value == "" {
		return ""
	}
	if isLocalPackageSource(value) {
		if strings.HasPrefix(value, "file:") {
			if parsed, err := url.Parse(value); err == nil {
				value = parsed.Path
			}
		}
		if value == "~" || strings.HasPrefix(value, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(base, value)
		}
		path, _ := filepath.Abs(value)
		return path
	}
	sum := sha256.Sum256([]byte(value))
	if base != "" && filepath.Clean(base) != filepath.Clean(agentConfigDir()) {
		return filepath.Join(base, ".pi", "packages", hex.EncodeToString(sum[:8]))
	}
	return filepath.Join(agentConfigDir(), "packages", hex.EncodeToString(sum[:8]))
}

func packageResourcePathsScope(cwd, kind, scope string) ([]string, []PackageDiagnostic) {
	configured, diagnostics := configuredPackagesScope(cwd, scope != "user")
	seen := map[string]bool{}
	var paths []string
	for _, item := range configured {
		if (scope == "user" && item.Scope != "user") || (scope == "project" && item.Scope != "project") {
			continue
		}
		root := packageRoot(item.PackageSource, item.Base)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			diagnostics = append(diagnostics, PackageDiagnostic{Source: item.Source, Scope: item.Scope, Message: "package is not installed at " + root})
			continue
		}
		patterns := item.Skills
		if kind == "prompts" {
			patterns = item.Prompts
		} else if kind == "agents" {
			patterns = item.Agents
		}
		autoload := item.Autoload == nil || *item.Autoload
		if len(patterns) == 0 && autoload {
			patterns = packageManifestEntries(root, kind)
			if len(patterns) == 0 {
				patterns = []string{kind}
			}
		}
		if len(patterns) == 0 {
			continue
		}
		validPatterns := make([]string, 0, len(patterns))
		for _, pattern := range patterns {
			value := strings.TrimLeft(pattern, "!+-")
			if !doublestar.ValidatePattern(filepath.ToSlash(value)) {
				diagnostics = append(diagnostics, PackageDiagnostic{Source: item.Source, Scope: item.Scope, Message: "invalid " + kind + " pattern: " + pattern})
				continue
			}
			validPatterns = append(validPatterns, pattern)
		}
		for _, path := range applyPackagePatterns(discoverPackageResources(root, kind), validPatterns, root) {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				diagnostics = append(diagnostics, PackageDiagnostic{Source: item.Source, Scope: item.Scope, Message: "invalid package resource path: " + path})
				continue
			}
			if _, safeErr := resolvePackageFile(root, filepath.ToSlash(relative)); safeErr != nil {
				diagnostics = append(diagnostics, PackageDiagnostic{Source: item.Source, Scope: item.Scope, Message: safeErr.Error()})
				continue
			}
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths, diagnostics
}

func discoverPackageResources(root, kind string) []string {
	var result []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if kind == "skills" {
			if entry.Name() == "SKILL.md" {
				result = append(result, path)
			}
		} else if strings.EqualFold(filepath.Ext(entry.Name()), ".md") && entry.Name() != "SKILL.md" {
			result = append(result, path)
		}
		return nil
	})
	sort.Strings(result)
	return result
}

func applyPackagePatterns(allPaths, patterns []string, root string) []string {
	includes, excludes, forceIncludes, forceExcludes := []string{}, []string{}, []string{}, []string{}
	for _, pattern := range patterns {
		switch {
		case strings.HasPrefix(pattern, "+"):
			forceIncludes = append(forceIncludes, pattern[1:])
		case strings.HasPrefix(pattern, "-"):
			forceExcludes = append(forceExcludes, pattern[1:])
		case strings.HasPrefix(pattern, "!"):
			excludes = append(excludes, pattern[1:])
		default:
			includes = append(includes, pattern)
		}
	}
	selected := map[string]bool{}
	for _, path := range allPaths {
		if len(includes) == 0 || packagePathMatches(path, includes, root, false) {
			selected[path] = true
		}
	}
	for _, path := range allPaths {
		if packagePathMatches(path, excludes, root, false) {
			delete(selected, path)
		}
		if packagePathMatches(path, forceIncludes, root, true) {
			selected[path] = true
		}
		if packagePathMatches(path, forceExcludes, root, true) {
			delete(selected, path)
		}
	}
	result := make([]string, 0, len(selected))
	for _, path := range allPaths {
		if selected[path] {
			result = append(result, path)
		}
	}
	return result
}

func packagePathMatches(filePath string, patterns []string, root string, exact bool) bool {
	relative, _ := filepath.Rel(root, filePath)
	relative = filepath.ToSlash(relative)
	parent := filepath.ToSlash(filepath.Dir(relative))
	for _, pattern := range patterns {
		normalized := strings.TrimPrefix(filepath.ToSlash(pattern), "./")
		if exact {
			if normalized == relative || (filepath.Base(filePath) == "SKILL.md" && normalized == parent) {
				return true
			}
			continue
		}
		if matched, _ := doublestar.Match(normalized, relative); matched {
			return true
		}
		if !strings.ContainsAny(normalized, "*?[") && strings.HasPrefix(relative, strings.TrimSuffix(normalized, "/")+"/") {
			return true
		}
		if filepath.Base(filePath) == "SKILL.md" {
			if matched, _ := doublestar.Match(normalized, parent); matched {
				return true
			}
		}
	}
	return false
}

func packageManifestEntries(root, kind string) []string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var manifest struct {
		Pi struct {
			Skills  []string `json:"skills"`
			Prompts []string `json:"prompts"`
			Agents  []string `json:"agents"`
		} `json:"pi"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	if kind == "skills" {
		return manifest.Pi.Skills
	}
	if kind == "agents" {
		return manifest.Pi.Agents
	}
	return manifest.Pi.Prompts
}

func (r Resources) PackageDiagnostics(cwd string) []PackageDiagnostic {
	return r.PackageDiagnosticsScope(cwd, "both")
}

func (r Resources) PackageDiagnosticsScope(cwd, scope string) []PackageDiagnostic {
	_, skillDiagnostics := packageResourcePathsScope(cwd, "skills", scope)
	_, promptDiagnostics := packageResourcePathsScope(cwd, "prompts", scope)
	_, agentDiagnostics := packageResourcePathsScope(cwd, "agents", scope)
	seen := map[string]bool{}
	result := []PackageDiagnostic{}
	diagnostics := append(skillDiagnostics, promptDiagnostics...)
	diagnostics = append(diagnostics, agentDiagnostics...)
	for _, item := range diagnostics {
		key := item.Source + "\x00" + item.Scope + "\x00" + item.Message
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	return result
}

func ConfiguredPackages(cwd string) ([]ConfiguredPackage, []PackageDiagnostic) {
	return configuredPackages(cwd)
}

func PackageRoot(source PackageSource, base string) string {
	return packageRoot(source, base)
}

func ApplyPackagePatterns(allPaths, patterns []string, root string) []string {
	return applyPackagePatterns(allPaths, patterns, root)
}
