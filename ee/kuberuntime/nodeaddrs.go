// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"net/netip"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

// A node's public IP is outside the private ranges, so open egress on 443 would reach the node's
// host-network listeners.
// Node addresses are excluded from the world rule and fed to netguard, which never sees them on the
// daemon pod's interfaces.

const nodeListTimeout = 10 * time.Second

const netguardSource = "kubernetes-nodes"

type nodeAddrCache struct {
	mu      sync.Mutex
	addrs   []netip.Addr
	failing bool
}

// A failed listing keeps the last good set (dropping it would reopen the nodes to tenant pods).
// It is logged, not returned: the private ranges still hold and a launch must not fail on it.
func (r *Runtime) RefreshNodeAddrs(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, nodeListTimeout)
	defer cancel()
	list, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})

	r.nodes.mu.Lock()
	defer r.nodes.mu.Unlock()
	if err != nil {
		if !r.nodes.failing {
			r.nodes.failing = true
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.list_nodes_failed", "err", err, "kept", len(r.nodes.addrs)))
		}
		return
	}
	r.nodes.failing = false
	var addrs []netip.Addr
	for i := range list.Items {
		for _, a := range list.Items[i].Status.Addresses {
			if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
				continue
			}
			if ip, perr := netip.ParseAddr(a.Address); perr == nil {
				addrs = append(addrs, ip.Unmap())
			}
		}
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	addrs = slices.Compact(addrs)
	r.nodes.addrs = addrs
	netguard.SetDiscovered(netguardSource, addrs)
}

func (r *Runtime) nodeAddrs() []netip.Addr {
	r.nodes.mu.Lock()
	defer r.nodes.mu.Unlock()
	return slices.Clone(r.nodes.addrs)
}
