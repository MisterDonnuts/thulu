// dockergc-bases lists, and with -apply removes, the base images this
// repository's Dockerfiles used to build from: older tags and digests of the
// repositories named in their FROM lines, never an image a container uses.
// With -kind it also takes kindest/node images, but only while no kind cluster
// exists. `make docker-gc` runs it after `thulu gc`.
//
// Usage:
//
//	go run ./cmd/dockergc-bases [-apply] [-kind] [-root .]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

func main() {
	configureLocale()
	apply := flag.Bool("apply", false, i18n.Text("dockergcbases.cli.flag_apply"))
	kind := flag.Bool("kind", false, i18n.Text("dockergcbases.cli.flag_kind"))
	root := flag.String("root", ".", i18n.Text("dockergcbases.cli.flag_root"))
	flag.Parse()
	if err := run(*root, *apply, *kind); err != nil {
		fmt.Fprintln(os.Stderr, i18n.Text("dockergcbases.cli.error", "err", apperr.Text(err)))
		os.Exit(1)
	}
}

func configureLocale() {
	for _, env := range []string{"OCTOPUS_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}

func run(root string, apply, kind bool) error {
	from, err := repoFromLines(root)
	if err != nil {
		return err
	}
	if kind && kindClusters() {
		fmt.Print("\n" + i18n.Text("dockergcbases.cli.kind_cluster_exists") + "\n")
		kind = false
	}
	api, err := updater.OpenDocker()
	if err != nil {
		return err
	}
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stale, err := dockergc.PlanStaleBases(ctx, api, dockergc.BaseOptions{From: from, Kind: kind})
	if err != nil {
		return err
	}
	plan := &dockergc.Plan{Images: stale}
	header := i18n.Text("dockergcbases.cli.header_empty")
	if len(stale) > 0 {
		header = i18n.Text("dockergcbases.cli.header", "count", len(stale), "size", dockergc.HumanBytes(plan.ImageBytes()))
	}
	fmt.Print("\n" + header + "\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, img := range stale {
		fmt.Fprintf(w, "   %s\t%s\t%s\n", strings.Join(img.Refs, ", "), dockergc.HumanBytes(img.Size), img.Reason)
	}
	w.Flush()
	if len(stale) == 0 {
		return nil
	}
	if !apply {
		again := "make docker-gc APPLY=1" //i18n:ignore shell command, not a message
		if kind {
			again += " KIND=1"
		}
		fmt.Println(i18n.Text("dockergcbases.cli.nothing_removed", "command", again))
		return nil
	}
	res := dockergc.Apply(ctx, api, plan)
	fmt.Println(i18n.Text("dockergcbases.cli.removed", "count", res.Images, "size", dockergc.HumanBytes(res.ImageBytes)))
	return res.Err()
}

func repoFromLines(root string) ([]string, error) {
	skip := map[string]bool{".git": true, "node_modules": true, ".claude": true, "dist": true}
	fsys := os.DirFS(root)
	var from []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !isDockerfile(d.Name()) {
			return nil
		}
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		from = append(from, dockergc.ParseFromLines(body)...)
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, "dockergcbases.errors.read_dockerfiles")
	}
	if len(from) == 0 {
		return nil, apperr.NotFound("dockergcbases.errors.no_from_lines")
	}
	return dockergc.SortedUnique(from), nil
}

func isDockerfile(name string) bool {
	//i18n:ignore file names, not a message
	return name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") || strings.HasSuffix(name, ".Dockerfile")
}

func kindClusters() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kind", "get", "clusters").Output()
	if err != nil {
		var exitErr *exec.ExitError
		// kind exists but failed: assume a cluster rather than remove its node image.
		return errors.As(err, &exitErr)
	}
	return strings.TrimSpace(string(out)) != ""
}
