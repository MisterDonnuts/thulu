//go:build !foss

// Package kubert names the Kubernetes runtime for code in the core that passes it along. With
// ee/ it is ee/kuberuntime.Runtime; the foss build has an empty type that is always nil.
package kubert

import "github.com/MisterDonnuts/thulu/ee/kuberuntime"

type Runtime = kuberuntime.Runtime
