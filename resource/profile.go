package resource

// Agent is the single runtime representation for every agent profile.
// Markdown supplies the profile data; Go enforces role delegation semantics.
type Agent struct {
	Name, Description, Provider, Model, ThinkingLevel, SystemPrompt, Path string
	Role                                                                  AgentRole
	Tools, OptionalTools, Delegates                                       []string
	Body                                                                  string
}

// AgentDefinition is kept as a compatibility alias for existing integrations.
type AgentDefinition = Agent

type AgentRole string

const (
	AgentRoleMain AgentRole = "main"
	AgentRoleSub  AgentRole = "sub"
	AgentRoleMeta AgentRole = "meta"
)

func CanDelegateAgent(caller, target AgentRole) bool {
	switch caller {
	case AgentRoleMain:
		return target == AgentRoleSub || target == AgentRoleMeta
	case AgentRoleSub:
		return target == AgentRoleMeta
	default:
		return false
	}
}

// CanDelegateTo applies both the role hierarchy and the caller profile's
// explicit target allowlist. An empty allowlist denies all delegation; "*"
// explicitly allows every visible target that is compatible with the role
// hierarchy.
func CanDelegateTo(caller, target Agent) bool {
	if !CanDelegateAgent(caller.Role, target.Role) {
		return false
	}
	for _, allowed := range caller.Delegates {
		if allowed == "*" || allowed == target.Name {
			return true
		}
	}
	return false
}
