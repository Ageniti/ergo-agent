package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name, Description, Path string
	DisableModelInvocation  bool
}
type ResourceDiagnostic struct {
	Type, Message, Path string
}
type PromptTemplate struct{ Name, Description, ArgumentHint, Body, Path string }

type Resources struct{ Root string }

const defaultCodingSystemPrompt = `You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
{{TOOLS}}

In addition to the tools above, you may have access to other custom tools depending on the project.

Guidelines:
{{GUIDELINES}}

Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: {{PI_README_PATH}}
- Additional docs: {{PI_DOCS_PATH}}
- Examples: {{PI_EXAMPLES_PATH}} (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md)
- When working on pi topics, read the docs and examples, and follow .md cross-references before implementing
- Always read pi .md files completely and follow links to related docs (e.g., tui.md for TUI API details){{APPEND_SYSTEM_PROMPT}}{{PROJECT_CONTEXT}}{{SKILLS}}
Current working directory: {{CWD}}
`

func (r Resources) Agent(name string) (Agent, error) {
	return r.AgentAt(name, "", "user")
}

func (r Resources) AgentAt(name, cwd, scope string) (Agent, error) {
	if name == "" {
		name = "chief-agent"
	}
	for _, path := range r.agentPathsAt(cwd, scope) {
		definition, loadErr := loadAgentDefinition(path, "")
		if loadErr == nil && definition.Name == name {
			return definition, nil
		}
	}
	return Agent{}, fmt.Errorf("agent role %q not found", name)
}

func (r Resources) AgentDefinitionsAt(cwd, scope string) []Agent {
	definitions := map[string]Agent{}
	for _, path := range r.agentPathsAt(cwd, scope) {
		definition, err := loadAgentDefinition(path, "")
		if err != nil {
			continue
		}
		if _, exists := definitions[definition.Name]; !exists {
			definitions[definition.Name] = definition
		}
	}
	result := make([]Agent, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r Resources) agentPathsAt(cwd, scope string) []string {
	var packagePaths, loosePaths []string
	if (scope == "project" || scope == "both") && cwd != "" {
		projectPackages, _ := packageResourcePathsScope(cwd, "agents", "project")
		packagePaths = append(packagePaths, projectPackages...)
		if dir := nearest(cwd, filepath.Join(".pi", "agents")); dir != "" {
			loosePaths = append(loosePaths, agentDefinitionPathsInDir(dir)...)
		}
	}
	if scope == "user" || scope == "both" || scope == "" {
		userPackages, _ := packageResourcePathsScope(cwd, "agents", "user")
		packagePaths = append(packagePaths, userPackages...)
		loosePaths = append(loosePaths, agentDefinitionPathsInDir(filepath.Join(agentConfigDir(), "agents"))...)
	}
	// Versioned packages override embedded defaults so a packaged Agent with the
	// same public name is reachable. Loose project/user profiles remain below
	// embedded defaults to preserve the built-in-role lock.
	paths := append(packagePaths, agentDefinitionPathsInDir(filepath.Join(r.Root, "agents"))...)
	paths = append(paths, loosePaths...)
	return paths
}

func agentDefinitionPathsInDir(dir string) []string {
	entries, _ := os.ReadDir(dir)
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths
}

func (r Resources) IsBundledAgent(name string) bool {
	if name == "" {
		return false
	}
	for _, definition := range agentDefinitionsInDir(filepath.Join(r.Root, "agents")) {
		if definition.Name == name {
			return true
		}
	}
	return false
}

// IsBundledAgentAt reports whether normal precedence resolution selects the
// embedded profile itself, rather than a same-name package profile.
func (r Resources) IsBundledAgentAt(name, cwd, scope string) bool {
	definition, err := r.AgentAt(name, cwd, scope)
	if err != nil {
		return false
	}
	bundledRoot, err := filepath.Abs(filepath.Join(r.Root, "agents"))
	if err != nil {
		return false
	}
	profilePath, err := filepath.Abs(definition.Path)
	return err == nil && inside(bundledRoot, profilePath)
}

func agentDefinitionsInDir(dir string) []Agent {
	paths := agentDefinitionPathsInDir(dir)
	definitions := make([]Agent, 0, len(paths))
	for _, path := range paths {
		definition, err := loadAgentDefinition(path, "")
		if err == nil {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

func loadAgentDefinition(path, fallbackName string) (Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, err
	}
	meta, body := frontmatter(string(data))
	name := first(meta["name"], fallbackName)
	if name == "" || strings.TrimSpace(meta["description"]) == "" {
		return Agent{}, fmt.Errorf("agent frontmatter requires name and description")
	}
	tools := splitCSV(meta["tools"])
	if len(tools) == 0 {
		tools = []string{"read", "bash", "edit", "write"}
	}
	optionalTools := uniqueStrings(splitCSV(meta["optional-tools"]))
	for _, optional := range optionalTools {
		if !contains(tools, optional) {
			return Agent{}, fmt.Errorf("agent optional tool %q is not listed in tools", optional)
		}
	}
	delegates := uniqueStrings(splitCSV(meta["delegates"]))
	role, err := agentRole(meta["role"], tools)
	if err != nil {
		return Agent{}, err
	}
	return Agent{Name: name, Description: meta["description"], Provider: meta["provider"], Model: meta["model"], ThinkingLevel: meta["thinking-level"], SystemPrompt: meta["system-prompt"], Path: path, Role: role, Tools: tools, OptionalTools: optionalTools, Delegates: delegates, Body: strings.TrimSpace(body)}, nil
}

func agentRole(value string, tools []string) (AgentRole, error) {
	role := AgentRole(strings.ToLower(strings.TrimSpace(value)))
	if role == "" {
		if contains(tools, "subagent") {
			return AgentRoleSub, nil
		}
		return AgentRoleMeta, nil
	}
	switch role {
	case AgentRoleMain, AgentRoleSub, AgentRoleMeta:
		return role, nil
	default:
		return "", fmt.Errorf("agent role must be main, sub, or meta")
	}
}

func (r Resources) Skills(cwd string, scope string) ([]Skill, error) {
	dirs := r.skillDirectories(cwd, scope)
	seen := map[string]Skill{}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && !info.IsDir() && filepath.Ext(dir) == ".md" {
			if skill, ok := loadSkill(dir, strings.TrimSuffix(filepath.Base(dir), ".md")); ok {
				if _, exists := seen[skill.Name]; !exists {
					seen[skill.Name] = skill
				}
			}
			continue
		}
		for _, skill := range scanSkills(dir, true) {
			if _, exists := seen[skill.Name]; !exists {
				seen[skill.Name] = skill
			}
		}
	}
	result := make([]Skill, 0, len(seen))
	for _, s := range seen {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (r Resources) skillDirectories(cwd, scope string) []string {
	dirs := []string{filepath.Join(r.Root, "skills")}
	packagePaths, _ := packageResourcePathsScope(cwd, "skills", scope)
	dirs = append(dirs, packagePaths...)
	configDir := agentConfigDir()
	if scope == "user" || scope == "both" {
		dirs = append(dirs, filepath.Join(configDir, "skills"))
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
		}
	}
	if scope == "project" || scope == "both" {
		if dir := nearest(cwd, filepath.Join(".pi", "skills")); dir != "" {
			dirs = append(dirs, dir)
		}
		if dir := nearest(cwd, filepath.Join(".agents", "skills")); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func (r Resources) Templates() []PromptTemplate {
	return r.TemplatesAt("")
}

func (r Resources) TemplatesAt(cwd string) []PromptTemplate {
	return r.TemplatesAtScope(cwd, true)
}

func (r Resources) TemplatesAtScope(cwd string, projectTrusted bool) []PromptTemplate {
	dirs := []string{filepath.Join(r.Root, "prompts", "templates"), filepath.Join(agentConfigDir(), "prompts")}
	scope := "user"
	if projectTrusted {
		scope = "both"
	}
	packagePaths, _ := packageResourcePathsScope(cwd, "prompts", scope)
	dirs = append(dirs, packagePaths...)
	if projectTrusted && cwd != "" {
		if dir := nearest(cwd, filepath.Join(".pi", "prompts")); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	seen := map[string]PromptTemplate{}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && !info.IsDir() && filepath.Ext(dir) == ".md" {
			if item, ok := loadPromptTemplate(dir); ok {
				if _, exists := seen[item.Name]; !exists {
					seen[item.Name] = item
				}
			}
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			if item, ok := loadPromptTemplate(path); ok {
				if _, exists := seen[item.Name]; !exists {
					seen[item.Name] = item
				}
			}
		}
	}
	var out []PromptTemplate
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadPromptTemplate(path string) (PromptTemplate, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptTemplate{}, false
	}
	meta, body := frontmatter(string(data))
	description := meta["description"]
	if description == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			description = line
			if len(description) > 60 {
				description = description[:60] + "..."
			}
			break
		}
	}
	// Pi uses the filename as the command name; a frontmatter "name" field
	// does not rename prompt templates.
	return PromptTemplate{
		Name:         strings.TrimSuffix(filepath.Base(path), ".md"),
		Description:  description,
		ArgumentHint: meta["argument-hint"],
		Body:         body,
		Path:         path,
	}, true
}

func loadTemplatesFromPaths(paths []string) []PromptTemplate {
	var result []PromptTemplate
	for _, root := range paths {
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			if template, ok := loadPromptTemplate(root); ok {
				result = append(result, template)
			}
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			if template, ok := loadPromptTemplate(filepath.Join(root, entry.Name())); ok {
				result = append(result, template)
			}
		}
	}
	return result
}

func agentConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("AGENT_CONFIG_DIR")); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".pi", "agent")
	}
	return ".pi/agent"
}

func scanSkills(dir string, includeRootMarkdown bool) []Skill {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	return scanSkillsVisited(dir, includeRootMarkdown, map[string]bool{}, root, nil)
}

func scanSkillsVisited(dir string, includeRootMarkdown bool, visited map[string]bool, root string, inheritedPatterns []string) []Skill {
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil || visited[canonical] {
		return nil
	}
	visited[canonical] = true
	patterns := append([]string(nil), inheritedPatterns...)
	relativeDir, _ := filepath.Rel(root, dir)
	prefix := ""
	if relativeDir != "." {
		prefix = filepath.ToSlash(relativeDir) + "/"
	}
	for _, name := range []string{".gitignore", ".ignore", ".fdignore"} {
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(normalizeLF(string(data)), "\n") {
			if pattern, ok := prefixIgnorePattern(line, prefix); ok {
				patterns = append(patterns, pattern)
			}
		}
	}
	matcher := gitignore.CompileIgnoreLines(patterns...)
	ignored := func(path string, directory bool) bool {
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return false
		}
		value := filepath.ToSlash(relative)
		if directory {
			value += "/"
		}
		return matcher.MatchesPath(value)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Stat(path)
		if entry.Name() == "SKILL.md" && statErr == nil && info.Mode().IsRegular() && !ignored(path, false) {
			if skill, ok := loadSkill(filepath.Join(dir, entry.Name()), filepath.Base(dir)); ok {
				return []Skill{skill}
			}
		}
	}
	var result []Skill
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			if !ignored(path, true) {
				result = append(result, scanSkillsVisited(path, false, visited, root, patterns)...)
			}
			continue
		}
		if includeRootMarkdown && info.Mode().IsRegular() && filepath.Ext(entry.Name()) == ".md" && !ignored(path, false) {
			if skill, ok := loadSkill(path, strings.TrimSuffix(entry.Name(), ".md")); ok {
				result = append(result, skill)
			}
		}
	}
	return result
}

func prefixIgnorePattern(line, prefix string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || (strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, `\#`)) {
		return "", false
	}
	negated := strings.HasPrefix(line, "!")
	if negated {
		line = strings.TrimPrefix(line, "!")
	} else {
		line = strings.TrimPrefix(line, `\!`)
	}
	line = strings.TrimPrefix(line, "/")
	line = prefix + line
	if negated {
		line = "!" + line
	}
	return line, true
}

func loadSkill(path, _ string) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	meta, _ := frontmatter(string(data))
	name := first(meta["name"], filepath.Base(filepath.Dir(path)))
	if len(validateSkillMetadata(name, meta["description"])) > 0 {
		return Skill{}, false
	}
	return Skill{Name: name, Description: meta["description"], Path: path, DisableModelInvocation: strings.EqualFold(meta["disable-model-invocation"], "true")}, true
}

func skillBaseDir(skill Skill) string {
	dir := filepath.Dir(skill.Path)
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	return filepath.Clean(dir)
}

func expandSkillContent(skill Skill, content string) string {
	return strings.ReplaceAll(content, "{baseDir}", skillBaseDir(skill))
}

func validateSkillMetadata(name, description string) []string {
	var diagnostics []string
	if len(name) > 64 {
		diagnostics = append(diagnostics, "name exceeds 64 characters")
	}
	if matched, _ := regexp.MatchString(`^[a-z0-9-]+$`, name); !matched {
		diagnostics = append(diagnostics, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		diagnostics = append(diagnostics, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		diagnostics = append(diagnostics, "name must not contain consecutive hyphens")
	}
	if strings.TrimSpace(description) == "" {
		diagnostics = append(diagnostics, "description is required")
	} else if len(description) > 1024 {
		diagnostics = append(diagnostics, "description exceeds 1024 characters")
	}
	return diagnostics
}

func (r Resources) SkillDiagnostics(cwd, scope string) []ResourceDiagnostic {
	var result []ResourceDiagnostic
	seenPaths := map[string]bool{}
	for _, root := range r.skillDirectories(cwd, scope) {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		candidates := []string{}
		if !info.IsDir() {
			candidates = append(candidates, root)
		} else {
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if entry.IsDir() {
					if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.Name() == "SKILL.md" || (filepath.Dir(path) == root && filepath.Ext(path) == ".md") {
					candidates = append(candidates, path)
				}
				return nil
			})
		}
		for _, path := range candidates {
			canonical, _ := filepath.EvalSymlinks(path)
			if canonical == "" {
				canonical = path
			}
			if seenPaths[canonical] {
				continue
			}
			seenPaths[canonical] = true
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				result = append(result, ResourceDiagnostic{Type: "warning", Message: readErr.Error(), Path: path})
				continue
			}
			meta, _ := frontmatter(string(data))
			name := first(meta["name"], filepath.Base(filepath.Dir(path)))
			for _, message := range validateSkillMetadata(name, meta["description"]) {
				result = append(result, ResourceDiagnostic{Type: "warning", Message: message, Path: path})
			}
		}
	}
	return result
}

func (r Resources) BuildSystemPrompt(def AgentDefinition, cwd string, tools []ToolDefinition, planMode bool) (string, error) {
	return r.buildSystemPrompt(def, cwd, tools, planMode, true, nil)
}

func (r Resources) BuildSystemPromptTrusted(def AgentDefinition, cwd string, tools []ToolDefinition, planMode, projectTrusted bool) (string, error) {
	return r.buildSystemPrompt(def, cwd, tools, planMode, projectTrusted, nil)
}

func (r Resources) buildSystemPrompt(def AgentDefinition, cwd string, tools []ToolDefinition, planMode, projectTrusted bool, extraSkills []Skill) (string, error) {
	path := def.SystemPrompt
	usesDefault := path == ""
	if path == "" {
		// Pi subagent definitions are append-system-prompt files. When they do
		// not explicitly replace the system prompt, they keep the full coding
		// harness and append their role body. Prefer an application-owned
		// override when present, otherwise use the Runtime's built-in harness.
		path = "prompts/system/coding-agent.md"
	}
	if !filepath.IsAbs(path) {
		rootPath := filepath.Join(r.Root, path)
		packagePath := ""
		if def.Path != "" {
			resourceRoot := agentResourceRoot(def.Path)
			if isAgentPackageRoot(resourceRoot) {
				resolved, resolveErr := resolvePackageFile(resourceRoot, path)
				if resolveErr != nil {
					if def.SystemPrompt != "" {
						return "", fmt.Errorf("resolve packaged system prompt: %w", resolveErr)
					}
				} else {
					packagePath = resolved
				}
			} else {
				packagePath = filepath.Join(resourceRoot, path)
			}
		}
		if packagePath != "" {
			if info, statErr := os.Stat(packagePath); statErr == nil && !info.IsDir() {
				path = packagePath
			} else {
				path = rootPath
			}
		} else {
			path = rootPath
		}
	} else if def.Path != "" && isAgentPackageRoot(agentResourceRoot(def.Path)) {
		return "", errors.New("packaged system prompt must be relative to the package root")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !usesDefault || !os.IsNotExist(err) {
			return "", fmt.Errorf("read system prompt: %w", err)
		}
		data = []byte(defaultCodingSystemPrompt)
	}
	var lines []string
	for _, tool := range tools {
		if tool.PromptSnippet != "" {
			lines = append(lines, "- "+tool.Name+": "+tool.PromptSnippet)
		}
	}
	if len(lines) == 0 {
		lines = []string{"(none)"}
	}
	var guidelines []string
	seenGuideline := map[string]bool{}
	addGuideline := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !seenGuideline[value] {
			seenGuideline[value] = true
			guidelines = append(guidelines, value)
		}
	}
	if hasTool(tools, "bash") && !hasTool(tools, "grep") && !hasTool(tools, "find") && !hasTool(tools, "ls") {
		addGuideline("Use bash for file operations like ls, rg, find")
	}
	for _, tool := range tools {
		for _, guideline := range tool.PromptGuidelines {
			addGuideline(guideline)
		}
	}
	addGuideline("Be concise in your responses")
	addGuideline("Show file paths clearly when working with files")
	skillScope := "user"
	if projectTrusted {
		skillScope = "both"
	}
	skills, _ := r.Skills(cwd, skillScope)
	knownSkills := map[string]bool{}
	for _, skill := range skills {
		knownSkills[skill.Name] = true
	}
	for _, skill := range extraSkills {
		if !knownSkills[skill.Name] {
			knownSkills[skill.Name] = true
			skills = append(skills, skill)
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	skillText := ""
	if hasTool(tools, "read") {
		skillText = formatSkillsForSystemPrompt(skills)
		if skillText != "" {
			skillText = "\n\n" + skillText
		}
	}
	projectContext := ""
	projectFiles := ""
	if projectTrusted {
		for _, contextFile := range loadProjectContextFiles(cwd) {
			projectFiles += "<project_instructions path=\"" + contextFile.Path + "\">\n" + contextFile.Content + "\n</project_instructions>\n\n"
		}
	}
	if projectFiles != "" {
		projectContext = "\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n" + projectFiles + "</project_context>\n"
	}
	appendSystemPrompt := ""
	if def.Body != "" {
		appendSystemPrompt = "\n\n" + def.Body
	}
	prompt := string(data)
	hasAppendPlaceholder := strings.Contains(prompt, "{{APPEND_SYSTEM_PROMPT}}")
	replacements := map[string]string{"{{TOOLS}}": strings.Join(lines, "\n"), "{{GUIDELINES}}": "- " + strings.Join(guidelines, "\n- "), "{{APPEND_SYSTEM_PROMPT}}": appendSystemPrompt, "{{PROJECT_CONTEXT}}": projectContext, "{{SKILLS}}": skillText, "{{CWD}}": filepath.ToSlash(cwd), "{{PI_README_PATH}}": filepath.Join(r.Root, "docs", "PI-PARITY.md"), "{{PI_DOCS_PATH}}": filepath.Join(r.Root, "docs"), "{{PI_EXAMPLES_PATH}}": filepath.Join(r.Root, "docs")}
	for old, newValue := range replacements {
		prompt = strings.ReplaceAll(prompt, old, newValue)
	}
	if !hasAppendPlaceholder {
		prompt += appendSystemPrompt
	}
	return prompt, nil
}

func agentResourceRoot(profilePath string) string {
	dir := filepath.Dir(profilePath)
	for {
		if filepath.Base(dir) == "agents" {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(profilePath)
		}
		dir = parent
	}
}

func loadSkillsFromPaths(paths []string) []Skill {
	var result []Skill
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			if skill, ok := loadSkill(path, ""); ok {
				result = append(result, skill)
			}
			continue
		}
		result = append(result, scanSkills(path, true)...)
	}
	return result
}

func formatSkillsForSystemPrompt(skills []Skill) string {
	visible := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if !skill.DisableModelInvocation {
			visible = append(visible, skill)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	b.WriteString("Read the full skill file when the task matches its description.\n")
	b.WriteString("When a skill file references a relative path, resolve it against the absolute skill directory shown in base_dir.\n")
	b.WriteString("Replace every literal {baseDir} placeholder in skill instructions with that base_dir before executing commands.\n\n")
	b.WriteString("<available_skills>\n")
	for _, skill := range visible {
		fmt.Fprintf(&b, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n    <base_dir>%s</base_dir>\n  </skill>\n", xmlEscape(skill.Name), xmlEscape(skill.Description), xmlEscape(skill.Path), xmlEscape(skillBaseDir(skill)))
	}
	b.WriteString("</available_skills>")
	return b.String()
}

type projectContextFile struct{ Path, Content string }

type ProjectContextFile = projectContextFile

func loadProjectContextFiles(cwd string) []projectContextFile {
	candidates := []string{"AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}
	load := func(dir string) (projectContextFile, bool) {
		for _, name := range candidates {
			path := filepath.Join(dir, name)
			if data, err := os.ReadFile(path); err == nil {
				return projectContextFile{Path: path, Content: string(data)}, true
			}
		}
		return projectContextFile{}, false
	}
	result := []projectContextFile{}
	seen := map[string]bool{}
	if file, ok := load(agentConfigDir()); ok {
		result = append(result, file)
		seen[file.Path] = true
	}
	dir, _ := filepath.Abs(cwd)
	ancestors := []string{}
	for {
		ancestors = append(ancestors, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		if file, ok := load(ancestors[i]); ok && !seen[file.Path] {
			result = append(result, file)
			seen[file.Path] = true
		}
	}
	return result
}

func (r Resources) ModePrompt(planMode, executing bool, userPrompt string) (string, error) {
	name := ""
	if planMode {
		name = "plan.md"
	} else if executing {
		name = "execute-plan.md"
	}
	if name == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(r.Root, "prompts", "modes", name))
	if err != nil {
		return "", err
	}
	text := string(data)
	if executing {
		remaining := userPrompt
		if _, tail, ok := strings.Cut(userPrompt, "\nPlan:\n"); ok {
			remaining = tail
		}
		text = strings.ReplaceAll(text, "{{REMAINING_STEPS}}", strings.TrimSpace(remaining))
	}
	return strings.TrimSpace(text), nil
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

func frontmatter(input string) (map[string]string, string) {
	meta := map[string]string{}
	normalized := normalizeLF(input)
	if !strings.HasPrefix(normalized, "---") {
		return meta, normalized
	}
	end := strings.Index(normalized[3:], "\n---")
	if end < 0 {
		return meta, normalized
	}
	end += 3
	yamlText := normalized[4:end]
	parsed := map[string]any{}
	if strings.TrimSpace(yamlText) != "" && yaml.Unmarshal([]byte(yamlText), &parsed) == nil {
		for key, value := range parsed {
			switch item := value.(type) {
			case string:
				meta[key] = item
			case bool:
				meta[key] = strconv.FormatBool(item)
			case int:
				meta[key] = strconv.Itoa(item)
			case int64:
				meta[key] = strconv.FormatInt(item, 10)
			case float64:
				meta[key] = strconv.FormatFloat(item, 'f', -1, 64)
			}
		}
	}
	return meta, strings.TrimSpace(normalized[end+4:])
}
func splitCSV(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func hasTool(tools []ToolDefinition, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
func nearest(start, relative string) string {
	dir, _ := filepath.Abs(start)
	for {
		candidate := filepath.Join(dir, relative)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// BuildSystemPromptWithSkills keeps extension-discovered skills on the same
// prompt-construction path as built-in and project skills.
func (r Resources) BuildSystemPromptWithSkills(def AgentDefinition, cwd string, tools []ToolDefinition, planMode, projectTrusted bool, extraSkills []Skill) (string, error) {
	return r.buildSystemPrompt(def, cwd, tools, planMode, projectTrusted, extraSkills)
}

func LoadSkillsFromPaths(paths []string) []Skill {
	return loadSkillsFromPaths(paths)
}

func LoadTemplatesFromPaths(paths []string) []PromptTemplate {
	return loadTemplatesFromPaths(paths)
}

func ParseFrontmatter(content string) (map[string]string, string) {
	return frontmatter(content)
}

func FormatSkillsForSystemPrompt(skills []Skill) string {
	return formatSkillsForSystemPrompt(skills)
}

func SkillBaseDir(skill Skill) string {
	return skillBaseDir(skill)
}

func ExpandSkillContent(skill Skill, content string) string {
	return expandSkillContent(skill, content)
}

func LoadAgentDefinition(path, fallbackName string) (Agent, error) {
	return loadAgentDefinition(path, fallbackName)
}

func ScanSkills(dir string, includeRootMarkdown bool) []Skill {
	return scanSkills(dir, includeRootMarkdown)
}

func LoadProjectContextFiles(cwd string) []ProjectContextFile {
	return loadProjectContextFiles(cwd)
}

func AgentConfigDir() string {
	return agentConfigDir()
}

func Nearest(start, relative string) string {
	return nearest(start, relative)
}
