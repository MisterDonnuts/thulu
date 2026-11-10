// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

func daemonSpec() LaunchSpec {
	spec := baseSpec()
	spec.Env[browserrun.DaemonURLEnv] = "http://octopus.octopus.svc:8585"
	return spec
}

func TestWaitNetworkIsTheFirstInitContainer(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true, Browser: browserConfig(t, browserrun.ModePerRun, nil)})

	emptyDir := daemonSpec()
	emptyDir.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app", Branch: "octo/task1234", CredentialEnv: "OCTOPUS_API_KEY"}}

	pvc := daemonSpec()
	pvc.WorktreePVC = "octopus-wt-task1234"
	pvc.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app", Branch: "octo/task1234"}}

	auditor := daemonSpec()
	auditor.Stage = "audit"

	chat := daemonSpec()
	chat.TaskID, chat.InstanceID, chat.Stage = "chat-ab12", "chat-ab12", "chat"
	chat.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app", ReadOnly: true}}
	chat.Browser = true

	runWithBrowser := pvc
	runWithBrowser.Browser = true

	names := []string{"run-emptydir", "run-pvc", "auditor", "chat", "run-browser"}
	specs := []LaunchSpec{emptyDir, pvc, auditor, chat, runWithBrowser}
	for i, name := range names {
		spec := &specs[i]
		pod := rt.buildPod(*spec)
		inits := pod.Spec.InitContainers
		if len(inits) == 0 || inits[0].Name != waitNetworkContainerName {
			t.Errorf("%s: initContainers[0] is not %s: %v", name, waitNetworkContainerName, containerNames(inits))
			continue
		}
		if strings.Count(strings.Join(containerNames(inits), " "), waitNetworkContainerName) != 1 {
			t.Errorf("%s: want exactly one %s: %v", name, waitNetworkContainerName, containerNames(inits))
		}
		if spec.Browser && inits[len(inits)-1].Name != browserContainerName {
			t.Errorf("%s: the browser sidecar must stay last, after wait-network and the loaders: %v",
				name, containerNames(inits))
		}
	}
}

func TestWaitNetworkShape(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true, LoaderImage: "octopus/loader:test"})
	spec := daemonSpec()
	pod := rt.buildPod(spec)
	w := pod.Spec.InitContainers[0]

	if w.Image != "octopus/loader:test" || w.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("image %q pull %q, want the loader image, IfNotPresent", w.Image, w.ImagePullPolicy)
	}
	if w.RestartPolicy != nil {
		t.Error("wait-network must be a plain init container, not a sidecar: the pod waits for it to finish")
	}
	sc := w.SecurityContext
	switch {
	case sc == nil:
		t.Fatal("no security context")
	case sc.RunAsUser == nil || *sc.RunAsUser != 1000:
		t.Errorf("must pin uid 1000 like the loader: %+v", sc.RunAsUser)
	case sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation:
		t.Error("must forbid privilege escalation")
	case sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL":
		t.Errorf("must drop ALL capabilities: %+v", sc.Capabilities)
	}
	if w.Resources.Limits.Memory().IsZero() || w.Resources.Limits.Cpu().IsZero() ||
		w.Resources.Requests.Memory().IsZero() || w.Resources.Requests.Cpu().IsZero() {
		t.Errorf("requests and limits must be set (a quota'd namespace rejects a container without them): %+v", w.Resources)
	}

	if len(w.Env) != 1 || w.Env[0].Name != browserrun.DaemonURLEnv || w.Env[0].ValueFrom == nil ||
		w.Env[0].ValueFrom.SecretKeyRef == nil ||
		w.Env[0].ValueFrom.SecretKeyRef.Name != EnvSecretName(pod.Name) ||
		w.Env[0].ValueFrom.SecretKeyRef.Key != browserrun.DaemonURLEnv {
		t.Errorf("want only %s from the pod's env Secret, got %+v", browserrun.DaemonURLEnv, w.Env)
	}

	cmd := w.Command
	if len(cmd) != 6 || cmd[0] != "sh" || cmd[1] != "-c" ||
		cmd[4] != strconv.Itoa(waitNetworkSeconds) || cmd[5] != strconv.Itoa(ExitNetworkClosed) {
		t.Errorf("command must pass the wait and the exit code as positional parameters: %q", cmd)
	}
	if !strings.Contains(cmd[2], "--connect-timeout") || !strings.Contains(cmd[2], "NetworkPolicy") {
		t.Errorf("script must bound each attempt and name the NetworkPolicy when it gives up: %q", cmd[2])
	}
}

func TestNoWaitNetworkWithoutTheDaemonURL(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app"}}
	for _, c := range rt.buildPod(spec).Spec.InitContainers {
		if c.Name == waitNetworkContainerName {
			t.Fatal("wait-network added to a pod without OCTOPUS_API")
		}
	}
}

func TestTerminalExitCodeReportsTheFailedInitContainer(t *testing.T) {
	terminated := func(name string, code int32) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: "Error"},
		}}
	}
	waiting := corev1.ContainerStatus{Name: agentContainerName, State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
	}}
	cases := map[string]struct {
		inits []corev1.ContainerStatus
		want  int
	}{
		"wait-network": {[]corev1.ContainerStatus{terminated(waitNetworkContainerName, ExitNetworkClosed)}, ExitNetworkClosed},
		"clone": {
			[]corev1.ContainerStatus{terminated(waitNetworkContainerName, 0), terminated("clone-app", 128)}, 128,
		},
		"browser-skipped": {
			[]corev1.ContainerStatus{terminated(browserContainerName, 137), terminated("clone-app", 128)}, 128,
		},
		"only-browser": {[]corev1.ContainerStatus{terminated(browserContainerName, 137)}, ExitDeleted},
		"evicted":      {nil, ExitDeleted},
	}
	for name, tc := range cases {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			Phase:                 corev1.PodFailed,
			InitContainerStatuses: tc.inits,
			ContainerStatuses:     []corev1.ContainerStatus{waiting},
		}}
		if code, done := terminalExitCode(pod); !done || code != tc.want {
			t.Errorf("%s: terminalExitCode = %d, %v; want %d, true", name, code, done, tc.want)
		}
	}
}

func containerNames(cs []corev1.Container) []string {
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].Name
	}
	return out
}
