package webhook

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"testing"
)

func TestEventsListsEveryConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "webhook.go", nil, 0)
	if err != nil {
		t.Fatalf("parse webhook.go: %v", err)
	}
	var declared []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "EventType" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok {
					continue
				}
				s, _ := strconv.Unquote(lit.Value)
				declared = append(declared, s)
			}
		}
	}
	listed := make([]string, 0, len(Events()))
	for _, e := range Events() {
		listed = append(listed, string(e))
	}
	sort.Strings(declared)
	sort.Strings(listed)
	if !slices.Equal(declared, listed) {
		t.Errorf("Events() = %v, constants declare %v", listed, declared)
	}
}

func TestUnknownEventPatterns(t *testing.T) {
	ok := []string{"*", "task.created", "task.*", "chat.message", "task_types.changed"}
	if got := UnknownEventPatterns(ok); len(got) != 0 {
		t.Errorf("UnknownEventPatterns(%v) = %v, want none", ok, got)
	}
	bad := []string{"task.updated", "agent.*", "flag.created", ""}
	if got := UnknownEventPatterns(append([]string{"task.created"}, bad...)); !slices.Equal(got, bad) {
		t.Errorf("UnknownEventPatterns = %v, want %v", got, bad)
	}
}
