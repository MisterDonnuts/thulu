package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoFromLinesSkipsDependencyAndWorktreeDirs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"deploy/Dockerfile":                "FROM golang:1.26.6-alpine AS build\nFROM alpine:3.22\n",
		"images/runtime/Dockerfile":        "ARG BASE=debian:13-slim\nFROM ${BASE}\nFROM node:22-alpine\n",
		"tools/x/test.Dockerfile":          "FROM python:3.13-slim\n",
		"web/node_modules/pkg/Dockerfile":  "FROM node:12\n",
		".claude/worktrees/old/Dockerfile": "FROM golang:1.20\n",
		"README.md":                        "FROM nothing\n",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	from, err := repoFromLines(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(from, " ")
	if got != "alpine:3.22 golang:1.26.6-alpine node:22-alpine python:3.13-slim" {
		t.Errorf("FROM lines = %s", got)
	}

	if _, err := repoFromLines(t.TempDir()); err == nil {
		t.Error("a tree with no Dockerfile must be an error, not an empty pin list that marks every base stale")
	}
}
