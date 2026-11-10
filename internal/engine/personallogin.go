package engine

const LLMSourcePersonal = "personal_login"

// The daemon mounts ConfigDir but never reads it: Thulu only runs these commands and relays what
// the person types.
type PersonalLogin struct {
	ConfigDirEnv string
	ConfigDir    string

	Login  []string
	Status []string
	Logout []string

	ParseStatus func(out []byte) (LoginStatus, error)

	ClearEnv []string
}

type LoginStatus struct {
	LoggedIn bool   `json:"logged_in"`
	Email    string `json:"email,omitempty"`
	Org      string `json:"org,omitempty"`
	Method   string `json:"method,omitempty"`
}

func PersonalLoginOf(name string) *PersonalLogin {
	if name == "" {
		name = DefaultName
	}
	eng, err := Get(name)
	if err != nil {
		return nil
	}
	return eng.Capabilities().PersonalLogin
}

func PersonalLoginClears(caps Capabilities) []string {
	var out []string
	for _, k := range caps.KeyKinds {
		out = append(out, k.EnvVars...)
		out = append(out, k.Clears...)
	}
	for _, v := range []string{caps.AuthorizationEnv, caps.BaseURLEnv, caps.OrgIDEnv} {
		if v != "" {
			out = append(out, v)
		}
	}
	if caps.PersonalLogin != nil {
		out = append(out, caps.PersonalLogin.ClearEnv...)
	}
	return append(out, "LLM_API_KEY", LLMGatewayEnv, LLMProfileEnv)
}

const LLMSourceRunnerLogin = "runner_login"
