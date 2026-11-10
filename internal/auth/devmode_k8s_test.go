package auth

import (
	"strings"
	"testing"
)

func TestDevMode_RefusedInKubernetes(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("OCTOPUS_JWT_SECRET", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")

	if DevMode() {
		t.Error("DevMode() = true inside Kubernetes")
	}
	err := ValidateStartupConfig("127.0.0.1:8585")
	if err == nil || !strings.Contains(err.Error(), "Kubernetes") {
		t.Errorf("ValidateStartupConfig = %v, want a refusal naming Kubernetes", err)
	}
}

func TestDevMode_StillWorksOutsideKubernetes(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if !DevMode() {
		t.Error("local dev mode broke")
	}
	if err := ValidateStartupConfig("127.0.0.1:8585"); err != nil {
		t.Errorf("loopback dev mode refused: %v", err)
	}
}

func TestValidateStartupConfig_RejectsBadSSODefaultRole(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("OCTOPUS_SSO_DEFAULT_ROLE", "owner")
	if err := ValidateStartupConfig("127.0.0.1:8585"); err == nil {
		t.Error("OCTOPUS_SSO_DEFAULT_ROLE=owner passed startup validation")
	}
}
