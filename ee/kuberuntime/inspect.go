// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func (r *Runtime) ControlPlaneNamespace() string { return r.cfg.Isolation.ControlPlaneNamespace }

func (r *Runtime) RuntimeClassName() string { return r.cfg.RuntimeClassName }

func SanitizeLabel(s string) string { return sanitizeDNS1123(s) }

func (r *Runtime) MetricsGet(ctx context.Context, absPath string) ([]byte, error) {
	b, err := r.client.CoreV1().RESTClient().Get().AbsPath(absPath).DoRaw(ctx)
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.metrics_get", "path", absPath)
	}
	return b, nil
}
