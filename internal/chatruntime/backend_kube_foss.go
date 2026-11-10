//go:build foss

package chatruntime

import "github.com/MisterDonnuts/thulu/internal/kubert"

func newKubeBackend(*kubert.Runtime, string) backend { return unavailableBackend{} }
