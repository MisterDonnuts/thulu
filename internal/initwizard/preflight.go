package initwizard

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (iv *Interviewer) preflight() {
	iv.P.narrate("initwizard.preflight.intro")
	docker := i18n.Text("initwizard.preflight.docker")
	if err := DockerReachable(iv.Ctx); err != nil {
		iv.P.Check(CheckFail, docker, i18n.Text("initwizard.preflight.docker_down", "err", firstLine(apperr.Text(err))))
	} else {
		iv.P.Check(CheckOK, docker, i18n.Text("initwizard.preflight.docker_up"))
		iv.dockerChecks()
	}
	if kctx, err := KubeContext(iv.Ctx); err != nil {
		iv.P.Check(CheckWarn, "kubectl", i18n.Text("initwizard.preflight.kubectl_none"))
	} else {
		iv.P.Check(CheckOK, "kubectl", i18n.Text("initwizard.preflight.kubectl_context", "context", kctx))
	}
	iv.P.Printf("")
}

func (iv *Interviewer) dockerChecks() {
	iv.archChecked = true
	compose := i18n.Text("initwizard.preflight.compose")
	if v := composeVersion(iv.Ctx); v != "" {
		iv.P.Check(CheckOK, compose, v)
	} else {
		iv.P.Check(CheckFail, compose, i18n.Text("initwizard.preflight.compose_missing"))
	}
	if iv.HostArch != nil {
		label := i18n.Text("initwizard.preflight.arch")
		switch arch := iv.HostArch(); {
		case arch == "":
		case !imagePublishedFor(arch):
			iv.P.Check(CheckWarn, label, i18n.Text("initwizard.preflight.arch_emulated", "arch", arch, "image_arch", publishedImageArchList()))
		default:
			iv.P.Check(CheckOK, label, arch)
		}
	}
	for _, port := range []int{8585, defaultPGHostPort} {
		label := i18n.Text("initwizard.preflight.port", "port", port)
		switch {
		case portFree(iv.Ctx, port):
			iv.P.Check(CheckOK, label, i18n.Text("initwizard.preflight.port_free"))
		case port == defaultPGHostPort && !iv.hasBundledPG():
			iv.P.Check(CheckOK, label, i18n.Text("initwizard.preflight.pg_port_busy"))
		default:
			iv.P.Check(CheckWarn, label, i18n.Text("initwizard.preflight.port_busy"))
		}
	}
}

func composeVersion(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "compose", "version", "--short").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// macOS lets a listener bind 127.0.0.1:N while another holds *:N, so a successful Listen alone
// reports a busy port as free; a connection that goes through settles it first.
func portFree(ctx context.Context, port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	if c, err := d.DialContext(ctx, "tcp", addr); err == nil && c.Close() == nil {
		return false
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	return l.Close() == nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
