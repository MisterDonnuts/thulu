package dockergc_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
)

func TestParseFromLines(t *testing.T) {
	df := `# syntax=docker/dockerfile:1
ARG BASE=ghcr.io/misterdonnuts/octopus-agent-base:latest
FROM node:22-alpine@sha256:aaa AS web-builder
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine@sha256:bbb AS go-builder
from ${BASE}
FROM web-builder AS again
FROM scratch
FROM alpine:3.24@sha256:ccc
`
	got := dockergc.ParseFromLines([]byte(df))
	want := []string{"node:22-alpine@sha256:aaa", "golang:1.26.6-alpine@sha256:bbb", "alpine:3.24@sha256:ccc"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("FROM = %v, want %v", got, want)
	}
}

// A FROM that loses its digest would make every other tag of that line look stale to
// PlanStaleBases.
func TestRepositoryDockerfilesPinTheirBases(t *testing.T) {
	root := filepath.Join("..", "..")
	var froms []string
	for _, rel := range []string{"deploy/Dockerfile", "images/docker/Dockerfile", "tools/thulu-browser/Dockerfile"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		froms = append(froms, dockergc.ParseFromLines(body)...)
	}
	if len(froms) == 0 {
		t.Fatal("no FROM lines found")
	}
	for _, f := range froms {
		if !strings.Contains(f, "@sha256:") {
			t.Errorf("%s is not pinned by digest", f)
		}
	}
}

func TestPlanStaleBases(t *testing.T) {
	d := &dockergctest.Docker{}
	img := func(id string, tags []string, digests ...string) {
		i := dockergctest.Image(id, 100, tags...)
		i.RepoDigests = digests
		d.Images = append(d.Images, i)
	}
	img("sha256:node22alpine", []string{"node:22-alpine@sha256:pinnednode"}, "node:22-alpine@sha256:pinnednode")
	img("sha256:golang126", []string{"golang:1.26.6-alpine"}, "golang@sha256:pinnedgo")
	img("sha256:alpine324", []string{"alpine:3.24"}, "alpine@sha256:pinnedalpine")
	img("sha256:node22old", []string{"node:22-alpine"}, "node@sha256:oldnode")
	img("sha256:golang1263", []string{"golang:1.26.3-alpine"}, "golang@sha256:g3")
	img("sha256:alpine321", []string{"alpine:3.21"}, "alpine@sha256:a21")
	img("sha256:pw149", []string{"mcr.microsoft.com/playwright:v1.49.0-noble"})
	img("sha256:node20", []string{"node:20-alpine"})
	img("sha256:nodebookworm", []string{"node:22-bookworm"})
	img("sha256:golangbookworm", []string{"golang:1.26-bookworm"})
	img("sha256:alpinelatest", []string{"alpine:latest"})
	img("sha256:python", []string{"python:3.12-slim"})
	img("sha256:golang1265", []string{"golang:1.26.5-alpine"})
	img("sha256:kind1", []string{"kindest/node:v1.30.13"})
	img("sha256:kind2", nil, "kindest/node@sha256:k2")
	d.Containers = []types.Container{
		dockergctest.Container("c1", "old-build", "golang:1.26.5-alpine", "sha256:golang1265", "exited", nil),
	}

	from := []string{
		"node:22-alpine@sha256:pinnednode", "golang:1.26.6-alpine@sha256:pinnedgo",
		"alpine:3.24@sha256:pinnedalpine", "mcr.microsoft.com/playwright:v1.63.0-noble@sha256:pw",
	}
	items, err := dockergc.PlanStaleBases(context.Background(), d, dockergc.BaseOptions{From: from})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpine:3.21", "golang:1.26.3-alpine", "mcr.microsoft.com/playwright:v1.49.0-noble", "node:22-alpine"}
	if got := names(items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stale = %v, want %v", got, want)
	}

	items, err = dockergc.PlanStaleBases(context.Background(), d, dockergc.BaseOptions{From: from, Kind: true})
	if err != nil {
		t.Fatal(err)
	}
	got := names(items)
	if !contains(got, "kindest/node:v1.30.13") || !contains(got, "kind2") {
		t.Errorf("kind images missing from %v", got)
	}
}

func names(items []dockergc.ImageItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name())
	}
	sort.Strings(out)
	return out
}
