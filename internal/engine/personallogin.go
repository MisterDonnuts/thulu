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
	var extra []string
	if caps.PersonalLogin != nil {
		extra = caps.PersonalLogin.ClearEnv
	}
	return loginClears(caps, extra)
}

// loginClears lists what outranks a stored CLI login: every credential, endpoint and gateway var.
func loginClears(caps Capabilities, extra []string) []string {
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
	out = append(out, extra...)
	return append(out, "LLM_API_KEY", LLMGatewayEnv, LLMProfileEnv)
}

const LLMSourceRunnerLogin = "runner_login"

// MachineLogin is how a runner finds an engine's CLI on a person's machine and reads whether it is
// signed in. The machine's own login pays for the run, so the daemon never sends a credential.
type MachineLogin struct {
	Binary  string
	Version []string
	// Prints the login state; ParseStatus reads it whatever the exit code.
	Status      []string
	ParseStatus func(out []byte) (LoginStatus, error)
	ClearEnv    []string
	// What the person runs on the machine to sign in; the UI proposes it when the login is missing.
	Login []string
}

func MachineLoginOf(name string) *MachineLogin {
	if name == "" {
		name = DefaultName
	}
	eng, err := Get(name)
	if err != nil {
		return nil
	}
	return eng.Capabilities().MachineLogin
}

// MachineLoginClears is what a runner removes from the person's shell before it starts the engine:
// a key exported there would outrank the machine's login and bill someone else.
func MachineLoginClears(caps Capabilities) []string {
	var extra []string
	if caps.MachineLogin != nil {
		extra = caps.MachineLogin.ClearEnv
	}
	return loginClears(caps, extra)
}
