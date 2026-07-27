package engine

import (
	"io"

	resourcepkg "github.com/ageniti/ergo-agent/resource"
)

// These aliases and forwarding helpers form the private bridge to resource.
// Resource discovery and package behavior have one implementation in the
// resource package; the engine does not duplicate or wrap that state.
type (
	Agent                    = resourcepkg.Agent
	AgentDefinition          = resourcepkg.AgentDefinition
	AgentRole                = resourcepkg.AgentRole
	Skill                    = resourcepkg.Skill
	ResourceDiagnostic       = resourcepkg.ResourceDiagnostic
	PromptTemplate           = resourcepkg.PromptTemplate
	Resources                = resourcepkg.Resources
	PackageSource            = resourcepkg.PackageSource
	PackageDiagnostic        = resourcepkg.PackageDiagnostic
	PackageManager           = resourcepkg.PackageManager
	AgentPackageManifest     = resourcepkg.AgentPackageManifest
	AgentPackageBuildOptions = resourcepkg.AgentPackageBuildOptions
	configuredPackage        = resourcepkg.ConfiguredPackage
	projectContextFile       = resourcepkg.ProjectContextFile
)

const (
	AgentRoleMain = resourcepkg.AgentRoleMain
	AgentRoleSub  = resourcepkg.AgentRoleSub
	AgentRoleMeta = resourcepkg.AgentRoleMeta
)

func canDelegateAgent(caller, target AgentRole) bool {
	return resourcepkg.CanDelegateAgent(caller, target)
}

func canDelegateTo(caller, target Agent) bool {
	return resourcepkg.CanDelegateTo(caller, target)
}

func agentPackageManifestForProfile(path string) (AgentPackageManifest, bool, error) {
	return resourcepkg.AgentPackageManifestForProfile(path)
}

func agentPackageRootForProfile(path string) (string, bool, error) {
	return resourcepkg.AgentPackageRootForProfile(path)
}

func agentInPackage(root, name string) (Agent, error) {
	return resourcepkg.AgentInPackage(root, name)
}

func agentRequiredTools(agent Agent) []string {
	return resourcepkg.AgentRequiredTools(agent)
}

func loadSkillsFromPaths(paths []string) []Skill {
	return resourcepkg.LoadSkillsFromPaths(paths)
}

func loadTemplatesFromPaths(paths []string) []PromptTemplate {
	return resourcepkg.LoadTemplatesFromPaths(paths)
}

func frontmatter(content string) (map[string]string, string) {
	return resourcepkg.ParseFrontmatter(content)
}

func formatSkillsForSystemPrompt(skills []Skill) string {
	return resourcepkg.FormatSkillsForSystemPrompt(skills)
}

func skillBaseDir(skill Skill) string {
	return resourcepkg.SkillBaseDir(skill)
}

func expandSkillContent(skill Skill, content string) string {
	return resourcepkg.ExpandSkillContent(skill, content)
}

func loadAgentDefinition(path, fallbackName string) (Agent, error) {
	return resourcepkg.LoadAgentDefinition(path, fallbackName)
}

func scanSkills(dir string, includeRootMarkdown bool) []Skill {
	return resourcepkg.ScanSkills(dir, includeRootMarkdown)
}

func loadProjectContextFiles(cwd string) []projectContextFile {
	return resourcepkg.LoadProjectContextFiles(cwd)
}

func nearest(start, relative string) string {
	return resourcepkg.Nearest(start, relative)
}

func configuredPackages(cwd string) ([]configuredPackage, []PackageDiagnostic) {
	return resourcepkg.ConfiguredPackages(cwd)
}

func packageRoot(source PackageSource, base string) string {
	return resourcepkg.PackageRoot(source, base)
}

func applyPackagePatterns(allPaths, patterns []string, root string) []string {
	return resourcepkg.ApplyPackagePatterns(allPaths, patterns, root)
}

func extractPackageTarGZ(source io.Reader, target string) error {
	return resourcepkg.ExtractPackageTarGZ(source, target)
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func hasTool(tools []ToolDefinition, name string) bool {
	for _, item := range tools {
		if item.Name == name {
			return true
		}
	}
	return false
}
