package runner

import "testing"

// Steering needs the steer loop, so only a runner that speaks its protocol may take the job.
func TestMinProtocolCountsSteering(t *testing.T) {
	cases := []struct {
		name string
		spec JobSpec
		want int
	}{
		{"folders", JobSpec{Projects: []JobProject{{ID: "a", Path: "/x"}}}, ProtocolFolders},
		{"steering off", JobSpec{Env: map[string]string{SteerEnabledEnv: "0"}}, ProtocolFolders},
		{"steering", JobSpec{Env: map[string]string{SteerEnabledEnv: "1"}}, ProtocolSteer},
		{"steering and daemon code", JobSpec{Env: map[string]string{SteerEnabledEnv: "1"},
			Projects: []JobProject{{ID: "b", Source: SourceDaemon}}}, max(ProtocolSteer, ProtocolDaemonCode)},
		{"chat turn", JobSpec{LaunchKind: LaunchKindChatTurn}, ProtocolChatTurn},
	}
	for _, c := range cases {
		if got := c.spec.MinProtocol(); got != c.want {
			t.Errorf("%s: MinProtocol = %d, want %d", c.name, got, c.want)
		}
	}
}
