package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

func TestDeliveryFailure(t *testing.T) {
	const endpoint = "https://ci.example.com/hooks/octopus"
	host := "ci.example.com"
	cases := []struct {
		name string
		err  error
		key  string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}, "webhook.delivery.no_such_host"},
		{"refused", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "webhook.delivery.refused"},
		{"timeout", fmt.Errorf("post: %w", context.DeadlineExceeded), "webhook.delivery.timeout"},
		{"unresolved by the egress guard", apperr.Wrap(netguard.ErrUnresolved, "netguard.errors.resolve_failed", "host", host, "err", "no such host"), "webhook.delivery.no_such_host"},
		{"blocked", apperr.Wrap(netguard.ErrBlocked, "netguard.errors.private_address", "ip", "10.0.0.1"), "webhook.delivery.blocked"},
		{"resolver unreachable", &url.Error{Op: "Post", URL: endpoint, Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
			Err: "dial udp [2001:db8::53]:53: connect: network is unreachable", Name: host, Server: "[2001:db8::53]:53",
			UnwrapErr: os.NewSyscallError("connect", syscall.ENETUNREACH),
		}}}, "webhook.delivery.dns_failed"},
		{"resolver timeout", &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}, "webhook.delivery.dns_failed"},
		{"network unreachable", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, "webhook.delivery.unreachable"},
		{"host unreachable", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, "webhook.delivery.unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, want := deliveryFailure(endpoint, c.err), i18n.Text(c.key, "host", host); got != want {
				t.Errorf("deliveryFailure = %q, want %q", got, want)
			}
		})
	}
	other := errors.New("boom")
	if got, want := deliveryFailure(endpoint, other), i18n.Text("webhook.delivery.failed", "host", host, "err", "boom"); got != want {
		t.Errorf("unknown failure = %q, want %q", got, want)
	}
	wrapped := &url.Error{Op: "Post", URL: endpoint, Err: other}
	if got, want := deliveryFailure(endpoint, wrapped), i18n.Text("webhook.delivery.failed", "host", host, "err", "boom"); got != want {
		t.Errorf("wrapped unknown failure = %q, want %q", got, want)
	}
}

func TestFailureText(t *testing.T) {
	recorded := i18n.T("en", "webhook.delivery.no_such_host", "host", "ci.example.com")
	if got, want := FailureText(recorded, "es"), i18n.T("es", "webhook.delivery.no_such_host", "host", "ci.example.com"); got != want {
		t.Errorf("FailureText = %q, want %q", got, want)
	}
	if got := FailureText("HTTP 502", "es"); got != "HTTP 502" {
		t.Errorf("a status line changed: %q", got)
	}
}
