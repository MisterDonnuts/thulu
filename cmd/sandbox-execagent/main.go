// Command sandbox-execagent is the in-pod exec server that runs inside every
// Kubernetes-mode sandbox pod. The daemon's sandbox.Kube runtime dials it over
// the pod's Service to run the tenant's build/test commands, instead of using
// kube pods/exec (see internal/sandbox/execagent for the rationale).
//
// Config (env):
//
//	OCTOPUS_EXEC_TOKEN  required — bearer token the daemon must present
//	OCTOPUS_EXEC_ADDR   listen address (default ":8587")
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
)

func main() {
	i18n.SetDaemonLocale(os.Getenv("OCTOPUS_LOCALE"))
	token := os.Getenv("OCTOPUS_EXEC_TOKEN")
	if token == "" {
		log.Fatal(i18n.Log("execagent.log.token_required"))
	}
	// Commands inherit this environment; they must not hold the bearer that authorizes them.
	if err := os.Unsetenv("OCTOPUS_EXEC_TOKEN"); err != nil {
		log.Fatal(i18n.Log("execagent.log.unset_token_failed", "err", err))
	}
	addr := os.Getenv("OCTOPUS_EXEC_ADDR")
	if addr == "" {
		addr = ":8587"
	}

	mux := http.NewServeMux()
	mux.Handle("/exec", execagent.NewServer(token))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	//nolint:gosec // G706: addr is operator-set env (OCTOPUS_EXEC_ADDR), not per-request user input.
	log.Print(i18n.Log("execagent.log.listening", "addr", addr))
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(i18n.Log("execagent.log.serve_failed", "err", err))
	}
}
