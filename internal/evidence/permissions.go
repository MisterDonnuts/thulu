package evidence

// The API's refusals and the UI's disabled controls both come from these functions so they never
// disagree.

// Must match auth.Role's values.
const (
	RoleViewer    = "viewer"
	RoleDeveloper = "developer"
	RoleAdmin     = "admin"
	RoleOwner     = "owner"
)

const (
	ActionRun     = "run"
	ActionRecord  = "record"
	ActionEdit    = "edit"
	ActionSecrets = "secrets"
	ActionDelete  = "delete"
	ActionLink    = "link"
)

const (
	ActionCreateCase                  = "create_case"
	ActionCreateEnvironment           = "create_environment"
	ActionCreateProductionEnvironment = "create_production_environment"
)

// The UI translates these codes; renaming one breaks its text.
const (
	ReasonViewer             = "viewer"
	ReasonProduction         = "production"
	ReasonAdminOnly          = "admin_only"
	ReasonDeleteOnProject    = "delete_on_project"
	ReasonSandboxFixed       = "sandbox_fixed"
	ReasonSandboxNoRecord    = "sandbox_no_record"
	ReasonNoURL              = "no_url"
	ReasonNoInitScript       = "no_init_script"
	ReasonNoPreviewPort      = "no_preview_port"
	ReasonSandboxUnavailable = "sandbox_unavailable"
	ReasonRepoNotGitHub      = "repo_not_github"
)

type Permission struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

var granted = Permission{Allowed: true}

func denied(reason string) Permission { return Permission{Reason: reason} }

func roleRank(role string) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleDeveloper:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

func isAdmin(role string) bool { return roleRank(role) >= roleRank(RoleAdmin) }

func isWriter(role string) bool { return roleRank(role) >= roleRank(RoleDeveloper) }

// The API must fill env.Sandbox first: a nil Sandbox lets a writer run it.
func EnvPermissions(role string, env Environment) map[string]Permission {
	out := make(map[string]Permission, 6)
	if !isWriter(role) {
		for _, a := range []string{ActionRun, ActionRecord, ActionEdit, ActionSecrets, ActionDelete, ActionLink} {
			out[a] = denied(ReasonViewer)
		}
		return out
	}
	out[ActionRun] = runPermission(env)
	switch {
	case env.Kind == KindSandbox:
		out[ActionRecord] = denied(ReasonSandboxNoRecord)
	case env.BaseURL == "":
		out[ActionRecord] = denied(ReasonNoURL)
	default:
		out[ActionRecord] = granted
	}
	if env.IsProduction && !isAdmin(role) {
		out[ActionEdit], out[ActionSecrets] = denied(ReasonProduction), denied(ReasonProduction)
	} else {
		out[ActionEdit], out[ActionSecrets] = granted, granted
	}
	out[ActionDelete] = kindFixed(env.Kind)
	if out[ActionDelete].Allowed && !isAdmin(role) {
		out[ActionDelete] = denied(ReasonAdminOnly)
	}
	if isAdmin(role) {
		out[ActionLink] = kindFixed(env.Kind)
	} else {
		out[ActionLink] = denied(ReasonAdminOnly)
	}
	return out
}

func runPermission(env Environment) Permission {
	if env.Kind == KindSandbox {
		if env.Sandbox != nil && !env.Sandbox.Available {
			return denied(env.Sandbox.Reason)
		}
		return granted
	}
	if env.BaseURL == "" {
		return denied(ReasonNoURL)
	}
	return granted
}

func kindFixed(kind string) Permission {
	switch kind {
	case KindProject:
		return denied(ReasonDeleteOnProject)
	case KindSandbox:
		return denied(ReasonSandboxFixed)
	}
	return granted
}

func WorkspacePermissions(role string) map[string]Permission {
	if !isWriter(role) {
		return map[string]Permission{
			ActionCreateCase: denied(ReasonViewer), ActionRun: denied(ReasonViewer),
			ActionCreateEnvironment: denied(ReasonViewer), ActionCreateProductionEnvironment: denied(ReasonViewer),
		}
	}
	prod := granted
	if !isAdmin(role) {
		prod = denied(ReasonProduction)
	}
	return map[string]Permission{
		ActionCreateCase: granted, ActionRun: granted, ActionCreateEnvironment: granted,
		ActionCreateProductionEnvironment: prod,
	}
}

func CanSetProduction(role string) bool { return isAdmin(role) }
