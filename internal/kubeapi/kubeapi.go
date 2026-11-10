// Package kubeapi holds the names Thulu puts on what it launches in Kubernetes. They live in the
// core because the infrastructure view reads them with or without the Kubernetes runtime in ee/.
package kubeapi

import (
	"errors"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// LabelTenant and LabelPlane must match deploy/k8s-data-plane/tenant-namespace.template.yaml so
// both provisioning paths select identically in NetworkPolicies.
const (
	LabelTaskID    = "thulu.dev/task-id"
	LabelWorkspace = "thulu.dev/workspace"
	LabelStage     = "thulu.dev/stage"
	LabelManagedBy = "thulu.dev/managed-by"
	LabelTenant    = "thulu.dev/tenant"
	LabelPlane     = "thulu.dev/plane"
	LabelSandbox   = "thulu.dev/sandbox"

	ManagedByValue = "octopus"

	// Annotations, not labels: the pod name is a lossy DNS-1123 sanitization, and Reconcile needs
	// the exact instance id to parse task+stage.
	AnnotationInstanceID = "thulu.dev/instance-id"
	AnnotationAgentType  = "thulu.dev/agent-type"
)

type LogReadiness int

const (
	LogsGone LogReadiness = iota
	LogsPending
	LogsRunning
	LogsDone
)

// SanitizeLabel lowercases s and keeps it a valid DNS-1123 label of at most 63 characters.
func SanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// The cache claim must never carry a task label or an octopus-wt-* name, or ReleaseWorktreeClaims
// and the janitor sweep would delete it.
const (
	CacheClaimName   = "octopus-cache"
	EnvCacheSize     = "OCTOPUS_SANDBOX_CACHE_SIZE"
	DefaultCacheSize = "20Gi"
)

func ParseCacheSize(v string) (size resource.Quantity, enabled bool, err error) {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "":
		return resource.MustParse(DefaultCacheSize), true, nil
	case "off", "0", "false", "disabled", "no":
		return resource.Quantity{}, false, nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, false, apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.cache_size", "value", v, "env", EnvCacheSize)
	}
	if q.Sign() < 0 {
		return resource.Quantity{}, false, apperr.Invalid("kuberuntime.errors.cache_size_negative", "value", v, "env", EnvCacheSize)
	}
	if q.IsZero() {
		return resource.Quantity{}, false, nil
	}
	return q, true, nil
}

var ErrQuotaFull error = apperr.Unavailable("kuberuntime.errors.quota_full")

func IsQuotaFull(err error) bool { return errors.Is(err, ErrQuotaFull) }

type RunningPod struct {
	Name        string
	Namespace   string
	TaskID      string
	WorkspaceID string
	Stage       string
	InstanceID  string
	AgentType   string
	Running     bool
	ExitCode    *int
	StartedAt   time.Time
	FinishedAt  time.Time
}
