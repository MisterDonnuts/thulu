package automation

import (
	"encoding/json"
	"strings"
	"testing"
)

func ctxFrom(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad context json: %v", err)
	}
	return m
}

func evalBool(t *testing.T, src string, ctx map[string]any) bool {
	t.Helper()
	e, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile(%q): %v", src, err)
	}
	v, err := e.Bool(ctx)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return v
}

func TestExpr_Conditions(t *testing.T) {
	ctx := ctxFrom(t, `{
		"steps": {
			"errs": {"status": "ok", "exit_code": 0,
			         "json": {"groups": [{"service":"api","count":12},{"service":"web","count":3}],
			                  "total": 15, "healthy": false}},
			"probe": {"status": "ok", "stdout": "DEGRADED: p95 1200ms\n", "exit_code": 2}
		},
		"trigger": {"kind": "schedule", "payload": {"action": "created"}},
		"vars": {"threshold": 10}
	}`)

	cases := []struct {
		src  string
		want bool
	}{
		{`size(steps.errs.json.groups) > 0`, true},
		{`size(steps.errs.json.groups) > 5`, false},
		{`steps.errs.json.total > vars.threshold`, true},
		{`steps.errs.json.groups[0].service == "api"`, true},
		{`steps.errs.json.groups[1].count >= 3 && steps.errs.json.total == 15`, true},
		{`steps.errs.json.healthy`, false},
		{`!steps.errs.json.healthy`, true},
		{`contains(steps.probe.stdout, "DEGRADED")`, true},
		{`steps.probe.exit_code != 0`, true},
		{`trigger.payload.action in ["created", "edited"]`, true},
		{`"api" in ["api","web"]`, true},
		{`"db" in ["api","web"]`, false},
		{`steps.errs.json.groups[0].service in ["api","web"]`, true},
		{`lower("API") == "api"`, true},
		{`matches(steps.probe.stdout, "p95 [0-9]+ms")`, true},
		{`default(steps.missing.json.count, 0) == 0`, true},
		{`exists(steps.errs)`, true},
		{`exists(steps.nope)`, false},
		{`steps.nope.json.count > 0`, false},
		{`steps.errs.json.total * 2 == 30`, true},
		{`(steps.errs.json.total - 5) / 2 == 5`, true},
		{`size(steps.errs.json.groups) > 0 && steps.errs.status == "ok"`, true},
		{`steps.errs.status == "failed" || steps.probe.exit_code == 2`, true},
	}
	for _, c := range cases {
		if got := evalBool(t, c.src, ctx); got != c.want {
			t.Errorf("%s = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestExpr_ShortCircuit(t *testing.T) {
	ctx := ctxFrom(t, `{"steps": {}}`)
	if evalBool(t, `exists(steps.gone) && steps.gone.count > 1`, ctx) {
		t.Error("guard idiom returned true on a missing path")
	}
	e, err := Compile(`exists(steps.gone) && steps.gone.name == "x"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bool(ctx); err != nil {
		t.Errorf("short-circuit still evaluated the right side: %v", err)
	}
}

func TestExpr_TypeMismatchIsAnError(t *testing.T) {
	ctx := ctxFrom(t, `{"a": {"name": "x"}}`)
	e, err := Compile(`a.name > 5`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bool(ctx); err == nil {
		t.Error("string > number was accepted")
	}
}

func TestExpr_RejectsGarbage(t *testing.T) {
	bad := []string{
		`a ==`, `(a`, `a)`, `"unterminated`, `a.`, `nope(1)`,
		`size()`, `size(1,2)`, `a @ b`, `1 / 0`,
	}
	for _, src := range bad {
		e, err := Compile(src)
		if err != nil {
			continue
		}
		if _, err := e.Eval(map[string]any{"a": 1.0}); err == nil {
			t.Errorf("%q was accepted and evaluated", src)
		}
	}
}

func TestRender_Templates(t *testing.T) {
	ctx := ctxFrom(t, `{
		"item": {"service": "api", "signature": "TypeError: x is undefined", "count": 12},
		"trigger": {"fired_at": "2026-09-17T10:15:00Z"},
		"steps": {"errs": {"json": {"total": 15}}}
	}`)
	cases := []struct{ in, want string }{
		{`Errores en {{item.service}}`, `Errores en api`},
		{`{{item.count}} eventos de {{item.signature}}`, `12 eventos de TypeError: x is undefined`},
		{`err:{{lower(item.service)}}:{{item.signature}}`, `err:api:TypeError: x is undefined`},
		{`total={{steps.errs.json.total}}`, `total=15`},
		{`{{missing.path}}sin nada`, `sin nada`},
		{`sin plantilla`, `sin plantilla`},
		{`{{ item.count * 2 }}`, `24`},
	}
	for _, c := range cases {
		got, err := Render(c.in, ctx)
		if err != nil {
			t.Fatalf("Render(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("Render(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRender_BrokenTemplateIsAnError(t *testing.T) {
	if _, err := Render(`{{ item. }}`, map[string]any{}); err == nil {
		t.Error("malformed template rendered without complaint")
	}
	if err := ValidateTemplate(`hola {{ item.name }}`); err != nil {
		t.Errorf("valid template rejected: %v", err)
	}
	if err := ValidateTemplate(`hola {{ item..name }}`); err == nil {
		t.Error("invalid template accepted at save time")
	}
}

func TestExpr_NoHostAccess(t *testing.T) {
	for _, src := range []string{
		`os.Getenv("HOME")`, `exec("rm -rf /")`, `import("os")`,
	} {
		e, err := Compile(src)
		if err != nil {
			continue
		}
		if _, err := e.Eval(map[string]any{}); err == nil {
			t.Errorf("%q evaluated instead of failing as an unknown function", src)
		}
	}
	v, err := Compile(`os.Getenv`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Eval(map[string]any{})
	if err != nil || got != nil {
		t.Errorf("path lookup = %v, %v; want nil, nil", got, err)
	}
}

func TestExpr_StringConcat(t *testing.T) {
	ctx := ctxFrom(t, `{"item": {"a": "x", "n": 3}}`)
	e, _ := Compile(`item.a + "-" + item.n`)
	got, err := e.Eval(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "x-3" {
		t.Errorf("concat = %v, want x-3", got)
	}
}

func TestExpr_SourceRoundTrips(t *testing.T) {
	src := `size(steps.a.json.items) > 0`
	e, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Source(), "size(") {
		t.Errorf("Source() = %q", e.Source())
	}
}

func TestExpressionPaths(t *testing.T) {
	e, err := Compile(`exists(trigger.payload.task_id) && trigger.payload.pr.number > 10 || "x" in steps.a.json.tags[0]`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range e.Paths() {
		got[strings.Join(p, ".")] = true
	}
	for _, want := range []string{"trigger.payload.task_id", "trigger.payload.pr.number", "steps.a.json.tags"} {
		if !got[want] {
			t.Errorf("paths %v miss %s", got, want)
		}
	}
}
