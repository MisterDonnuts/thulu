package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

var hostcliForbidden = []string{
	"github.com/MisterDonnuts/thulu/internal/daemon",
	"github.com/MisterDonnuts/thulu/web",
	"k8s.io/client-go",
	"go.opentelemetry.io/proto/otlp",
	"google.golang.org/grpc",
	"github.com/prometheus/client_golang",
}

func TestHostBuildLeavesTheServerOut(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			imports := hostcliImports(t, goos)
			for _, bad := range hostcliForbidden {
				if chain := importChain(imports, "github.com/MisterDonnuts/thulu", bad); chain != nil {
					t.Errorf("the hostcli build links %s:\n  %s", bad, strings.Join(chain, "\n  -> "))
				}
			}
		})
	}
}

func hostcliImports(t *testing.T, goos string) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-tags", "hostcli",
		"-f", `{{.ImportPath}} {{join .Imports " "}}`, ".")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	imports := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		imports[fields[0]] = fields[1:]
	}
	return imports
}

func importChain(imports map[string][]string, root, prefix string) []string {
	parent := map[string]string{root: ""}
	queue := []string{root}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if pkg == prefix || strings.HasPrefix(pkg, prefix+"/") {
			var chain []string
			for p := pkg; p != ""; p = parent[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		for _, next := range imports[pkg] {
			if _, seen := parent[next]; !seen {
				parent[next] = pkg
				queue = append(queue, next)
			}
		}
	}
	return nil
}
