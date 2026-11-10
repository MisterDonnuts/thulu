//go:build !foss

package sandbox

import (
	"context"
	"errors"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Create returns before the Service routes to the pod, so the first exec can be refused.
// A refused dial never reached the exec agent, so retrying cannot run the command twice.
const (
	execStartGrace     = 3 * time.Minute
	execDialRetryFirst = 250 * time.Millisecond
	execDialRetryMax   = 2 * time.Second
)

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func (k *Kube) podStarting(ctx context.Context, ns, name string) bool {
	pod, err := k.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	return time.Since(pod.CreationTimestamp.Time) < execStartGrace
}

func (k *Kube) retryDial(ctx context.Context, ns, name string, attempt func() error) error {
	wait := execDialRetryFirst
	for {
		err := attempt()
		if err == nil || !isDialError(err) || !k.podStarting(ctx, ns, name) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, execDialRetryMax)
	}
}
