package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func TestEmittedWebhookEventsAreInTheCatalog(t *testing.T) {
	whEventArg := map[string]int{"emit": 2, "emitSystem": 3}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			i, ok := whEventArg[sel.Sel.Name]
			if !ok || len(call.Args) <= i {
				return true
			}
			lit, ok := call.Args[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			ev, _ := strconv.Unquote(lit.Value)
			checked++
			if ev != "" && !slices.Contains(webhook.Events(), webhook.EventType(ev)) {
				t.Errorf("%s: emits webhook event %q, which webhook.Events() does not list",
					fset.Position(lit.Pos()), ev)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no emit call with a literal event; the scan no longer matches the code")
	}
}
