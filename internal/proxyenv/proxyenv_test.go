package proxyenv

import (
	"reflect"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvFillsTheOtherSpelling(t *testing.T) {
	got := FromEnv(envOf(map[string]string{
		"HTTPS_PROXY": "http://proxy:3128",
		"no_proxy":    "localhost,.svc",
	}))
	want := map[string]string{
		"HTTPS_PROXY": "http://proxy:3128", "https_proxy": "http://proxy:3128",
		"NO_PROXY": "localhost,.svc", "no_proxy": "localhost,.svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromEnv = %v, want %v", got, want)
	}
}

func TestFromEnvKeepsDistinctSpellings(t *testing.T) {
	got := FromEnv(envOf(map[string]string{"HTTP_PROXY": "http://a", "http_proxy": "http://b"}))
	if got["HTTP_PROXY"] != "http://a" || got["http_proxy"] != "http://b" {
		t.Errorf("both spellings set must pass through unchanged, got %v", got)
	}
}

func TestListIsEmptyWithoutProxyAndOrdered(t *testing.T) {
	if l := List(envOf(nil)); len(l) != 0 {
		t.Errorf("no proxy configured must forward nothing, got %v", l)
	}
	got := List(envOf(map[string]string{"NO_PROXY": "x", "HTTP_PROXY": "http://p"}))
	want := []string{"HTTP_PROXY=http://p", "NO_PROXY=x", "http_proxy=http://p", "no_proxy=x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}
