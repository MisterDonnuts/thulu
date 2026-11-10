package main

import (
	"testing"
	"time"
)

func TestParseGCArgs(t *testing.T) {
	opts, err := parseGCArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Apply || opts.Unclaimed || opts.MinAge != time.Hour {
		t.Errorf("defaults = %+v, want a dry run sparing the last hour", opts)
	}
	opts, err = parseGCArgs([]string{"--apply", "-min-age", "0s", "-unclaimed", "-root", "/srv/octopus"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Apply || !opts.Unclaimed || opts.MinAge != 0 || opts.Root != "/srv/octopus" {
		t.Errorf("parsed = %+v", opts)
	}
}

func TestParseUninstallArgs(t *testing.T) {
	opts, err := parseUninstallArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Yes || opts.PurgeData {
		t.Errorf("defaults = %+v, want a dry run that keeps the data", opts)
	}
	opts, err = parseUninstallArgs([]string{"--yes", "--purge-data"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Yes || !opts.PurgeData {
		t.Errorf("parsed = %+v", opts)
	}
}
