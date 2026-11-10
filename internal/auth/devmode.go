package auth

import (
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Dev mode inside a container can bind all interfaces, since a loopback bind is unreachable through
// a published port.
// Safe only because `thulu init` publishes on 127.0.0.1 and inContainer() cannot be faked by env.
const DevModeContainerEnv = "OCTOPUS_DEV_MODE_CONTAINER"

// Always false in Kubernetes: the pod is reachable cluster-wide through its Service.
// ValidateStartupConfig refuses too; this covers callers that ask before or without it.
func DevMode() bool {
	if !devModeRequested() {
		return false
	}
	if inKubernetes() {
		devModeRefusedOnce.Do(func() {
			log.Printf("[auth] %s", i18n.Log("auth.log.dev_mode_ignored_in_kubernetes"))
		})
		return false
	}
	return true
}

var devModeRefusedOnce sync.Once

func devModeRequested() bool {
	return os.Getenv("OCTOPUS_DEV_MODE") == "1"
}

func inKubernetes() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func ValidateStartupConfig(listenAddr string) error {
	secret := strings.TrimSpace(os.Getenv("OCTOPUS_JWT_SECRET"))
	if devModeRequested() && inKubernetes() {
		return apperr.Precondition("auth.errors.dev_mode_in_kubernetes")
	}
	if _, err := SSODefaultRole(); err != nil {
		return err
	}
	if !validSignupModeEnv() {
		return apperr.Precondition("auth.errors.signup_mode_invalid", "value", os.Getenv("OCTOPUS_SIGNUP_MODE"))
	}
	if err := validateSaaSConfig(); err != nil {
		return err
	}
	if !validMaxOwnedWorkspacesEnv() {
		return apperr.Precondition("auth.errors.max_owned_workspaces_invalid", "value", os.Getenv("OCTOPUS_MAX_OWNED_WORKSPACES"))
	}
	if DevMode() {
		if !listenAddrIsLoopback(listenAddr) && !DevModeContainerBind() {
			return apperr.Precondition("auth.errors.dev_mode_not_loopback", "addr", listenAddr, "env", DevModeContainerEnv)
		}
		return nil
	}
	if secret == "" {
		return apperr.Precondition("auth.errors.jwt_secret_required")
	}
	if len(jwtSecret) < 16 {
		return apperr.Precondition("auth.errors.jwt_secret_too_short")
	}
	return nil
}

func listenAddrIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func DevModeContainerBind() bool {
	return os.Getenv(DevModeContainerEnv) == "1" && inContainer()
}

// Only runtime-placed signals count, so DevModeContainerEnv can't become a plain "disable the
// loopback check" switch.
func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	cgroup, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := string(cgroup)
	for _, engine := range []string{"docker", "containerd", "kubepods", "podman", "lxc"} {
		if strings.Contains(text, engine) {
			return true
		}
	}
	return false
}
