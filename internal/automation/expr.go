package automation

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Recipes are user-edited text: evaluation must never reach the filesystem, network, clock or
// arbitrary Go. Only the map/slice tree the engine builds is readable.

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokString
	tokIdent
	tokOp
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokComma
	tokDot
)

type token struct {
	kind tokenKind
	text string
	num  float64
	pos  int
}

var punctuation = map[byte]tokenKind{
	'(': tokLParen, ')': tokRParen,
	'[': tokLBracket, ']': tokRBracket,
	',': tokComma, '.': tokDot,
}

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9':
			tok, next, err := lexNumber(src, i)
			if err != nil {
				return nil, err
			}
			out, i = append(out, tok), next
		case c == '"' || c == '\'':
			tok, next, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			out, i = append(out, tok), next
		case isIdentStart(rune(c)):
			tok, next := lexIdent(src, i)
			out, i = append(out, tok), next
		default:
			tok, next, err := lexSymbol(src, i)
			if err != nil {
				return nil, err
			}
			out, i = append(out, tok), next
		}
	}
	out = append(out, token{kind: tokEOF, pos: len(src)})
	return out, nil
}

func lexIdent(src string, i int) (token, int) {
	j := i
	for j < len(src) && isIdentPart(rune(src[j])) {
		j++
	}
	return token{kind: tokIdent, text: src[i:j], pos: i}, j
}

func lexSymbol(src string, i int) (token, int, error) {
	if kind, ok := punctuation[src[i]]; ok {
		return token{kind: kind, text: string(src[i]), pos: i}, i + 1, nil
	}
	op, n := lexOperator(src[i:])
	if n == 0 {
		return token{}, 0, apperr.Invalid("automation.errors.expr_unexpected_char", "char", string(src[i]), "pos", i)
	}
	return token{kind: tokOp, text: op, pos: i}, i + n, nil
}

func lexNumber(src string, i int) (token, int, error) {
	j := i
	for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
		j++
	}
	n, err := strconv.ParseFloat(src[i:j], 64)
	if err != nil {
		return token{}, 0, apperr.Invalid("automation.errors.expr_bad_number", "text", src[i:j], "pos", i)
	}
	return token{kind: tokNumber, text: src[i:j], num: n, pos: i}, j, nil
}

func lexString(src string, i int) (token, int, error) {
	quote := src[i]
	j := i + 1
	var sb strings.Builder
	for j < len(src) && src[j] != quote {
		if src[j] == '\\' && j+1 < len(src) {
			j++
			switch src[j] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte(src[j])
			}
		} else {
			sb.WriteByte(src[j])
		}
		j++
	}
	if j >= len(src) {
		return token{}, 0, apperr.Invalid("automation.errors.expr_unterminated_string", "pos", i)
	}
	return token{kind: tokString, text: sb.String(), pos: i}, j + 1, nil
}

func lexOperator(s string) (string, int) {
	for _, op := range []string{"==", "!=", "<=", ">=", "&&", "||"} {
		if strings.HasPrefix(s, op) {
			return op, 2
		}
	}
	for _, op := range []string{"<", ">", "!", "+", "-", "*", "/", "%"} {
		if strings.HasPrefix(s, op) {
			return op, 1
		}
	}
	return "", 0
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

type node interface{}

type litNode struct{ val any }
type pathNode struct {
	root  string
	steps []node
}
type callNode struct {
	name string
	args []node
}
type listNode struct{ items []node }
type unaryNode struct {
	op  string
	arg node
}
type binNode struct {
	op          string
	left, right node
}

type Expression struct {
	src  string
	root node
}

func (e *Expression) Source() string { return e.src }

func Compile(src string) (*Expression, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, apperr.Invalid("automation.errors.expr_trailing_input", "pos", p.peek().pos, "src", src)
	}
	return &Expression{src: src, root: root}, nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

var precedences = []map[string]bool{
	{"||": true},
	{"&&": true},
	{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true, "in": true},
	{"+": true, "-": true},
	{"*": true, "/": true, "%": true},
}

func (p *parser) parseExpr(level int) (node, error) {
	if level >= len(precedences) {
		return p.parseUnary()
	}
	left, err := p.parseExpr(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		op := t.text
		isOp := t.kind == tokOp || (t.kind == tokIdent && op == "in")
		if !isOp || !precedences[level][op] {
			return left, nil
		}
		p.next()
		right, err := p.parseExpr(level + 1)
		if err != nil {
			return nil, err
		}
		left = &binNode{op: op, left: left, right: right}
	}
}

func (p *parser) parseUnary() (node, error) {
	t := p.peek()
	if t.kind == tokOp && (t.text == "!" || t.text == "-") {
		p.next()
		arg, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryNode{op: t.text, arg: arg}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	t := p.next()
	switch t.kind {
	case tokNumber:
		return &litNode{val: t.num}, nil
	case tokString:
		return &litNode{val: t.text}, nil
	case tokLBracket:
		return p.parseList()
	case tokLParen:
		inner, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		if p.next().kind != tokRParen {
			return nil, apperr.Invalid("automation.errors.expr_missing_paren", "pos", t.pos)
		}
		return inner, nil
	case tokIdent:
		switch t.text {
		case "true":
			return &litNode{val: true}, nil
		case "false":
			return &litNode{val: false}, nil
		case "null", "nil":
			return &litNode{val: nil}, nil
		}
		if p.peek().kind == tokLParen {
			return p.parseCall(t.text)
		}
		return p.parsePath(t.text)
	default:
		return nil, apperr.Invalid("automation.errors.expr_unexpected_token", "token", t.text, "pos", t.pos)
	}
}

func (p *parser) parseCall(name string) (node, error) {
	p.next()
	call := &callNode{name: name}
	if p.peek().kind == tokRParen {
		p.next()
		return call, nil
	}
	for {
		arg, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		call.args = append(call.args, arg)
		switch p.next().kind {
		case tokComma:
			continue
		case tokRParen:
			return call, nil
		default:
			return nil, apperr.Invalid("automation.errors.expr_bad_args", "name", name)
		}
	}
}

func (p *parser) parseList() (node, error) {
	list := &listNode{}
	if p.peek().kind == tokRBracket {
		p.next()
		return list, nil
	}
	for {
		item, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		list.items = append(list.items, item)
		switch p.next().kind {
		case tokComma:
			continue
		case tokRBracket:
			return list, nil
		default:
			return nil, apperr.Invalid("automation.errors.expr_unterminated_list")
		}
	}
}

func (p *parser) parsePath(root string) (node, error) {
	path := &pathNode{root: root}
	for {
		switch p.peek().kind {
		case tokDot:
			p.next()
			field := p.next()
			if field.kind != tokIdent && field.kind != tokNumber {
				return nil, apperr.Invalid("automation.errors.expr_bad_field", "pos", field.pos)
			}
			path.steps = append(path.steps, field.text)
		case tokLBracket:
			p.next()
			idx, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			if p.next().kind != tokRBracket {
				return nil, apperr.Invalid("automation.errors.expr_missing_bracket")
			}
			path.steps = append(path.steps, idx)
		default:
			return path, nil
		}
	}
}

func (e *Expression) Paths() [][]string {
	var out [][]string
	var walk func(n node)
	walk = func(n node) {
		switch t := n.(type) {
		case *pathNode:
			p := []string{t.root}
			for _, step := range t.steps {
				name, ok := step.(string)
				if !ok {
					walk(step)
					break
				}
				p = append(p, name)
			}
			out = append(out, p)
		case *unaryNode:
			walk(t.arg)
		case *binNode:
			walk(t.left)
			walk(t.right)
		case *callNode:
			for _, a := range t.args {
				walk(a)
			}
		case *listNode:
			for _, item := range t.items {
				walk(item)
			}
		}
	}
	walk(e.root)
	return out
}

func (e *Expression) Eval(ctx map[string]any) (any, error) {
	return evalNode(e.root, ctx)
}

func (e *Expression) Bool(ctx map[string]any) (bool, error) {
	v, err := e.Eval(ctx)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func evalNode(n node, ctx map[string]any) (any, error) {
	switch t := n.(type) {
	case *litNode:
		return t.val, nil
	case *pathNode:
		return evalPath(t, ctx)
	case *unaryNode:
		return evalUnary(t, ctx)
	case *binNode:
		return evalBin(t, ctx)
	case *callNode:
		return evalCall(t, ctx)
	case *listNode:
		items := make([]any, 0, len(t.items))
		for _, item := range t.items {
			v, err := evalNode(item, ctx)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	default:
		return nil, apperr.Internal("automation.errors.expr_unknown_node", "node", n)
	}
}

func evalPath(p *pathNode, ctx map[string]any) (any, error) {
	cur, ok := ctx[p.root]
	if !ok {
		return nil, nil
	}
	for _, step := range p.steps {
		if cur == nil {
			return nil, nil
		}
		var key any
		if s, isStr := step.(string); isStr {
			key = s
		} else {
			v, err := evalNode(step, ctx)
			if err != nil {
				return nil, err
			}
			key = v
		}
		cur = index(cur, key)
	}
	return cur, nil
}

func index(container, key any) any {
	switch c := container.(type) {
	case map[string]any:
		return c[toString(key)]
	case []any:
		i, ok := toIndex(key)
		if !ok || i < 0 || i >= len(c) {
			return nil
		}
		return c[i]
	case []string:
		i, ok := toIndex(key)
		if !ok || i < 0 || i >= len(c) {
			return nil
		}
		return c[i]
	case map[string]string:
		return c[toString(key)]
	default:
		return nil
	}
}

func toIndex(key any) (int, bool) {
	switch k := key.(type) {
	case float64:
		return int(k), true
	case int:
		return k, true
	case string:
		i, err := strconv.Atoi(k)
		return i, err == nil
	}
	return 0, false
}

func evalUnary(u *unaryNode, ctx map[string]any) (any, error) {
	v, err := evalNode(u.arg, ctx)
	if err != nil {
		return nil, err
	}
	switch u.op {
	case "!":
		return !truthy(v), nil
	case "-":
		n, ok := toNumber(v)
		if !ok {
			return nil, apperr.Unprocessable("automation.errors.expr_cannot_negate", "value", v)
		}
		return -n, nil
	}
	return nil, apperr.Internal("automation.errors.expr_unknown_unary", "op", u.op)
}

func evalBin(b *binNode, ctx map[string]any) (any, error) {
	if b.op == "&&" || b.op == "||" {
		left, err := evalNode(b.left, ctx)
		if err != nil {
			return nil, err
		}
		lt := truthy(left)
		if (b.op == "&&" && !lt) || (b.op == "||" && lt) {
			return lt, nil
		}
		right, err := evalNode(b.right, ctx)
		if err != nil {
			return nil, err
		}
		return truthy(right), nil
	}

	left, err := evalNode(b.left, ctx)
	if err != nil {
		return nil, err
	}
	right, err := evalNode(b.right, ctx)
	if err != nil {
		return nil, err
	}

	switch b.op {
	case "==":
		return equals(left, right), nil
	case "!=":
		return !equals(left, right), nil
	case "in":
		return contains(right, left), nil
	case "+":
		if isString(left) || isString(right) {
			return toString(left) + toString(right), nil
		}
		return arith(b.op, left, right)
	case "-", "*", "/", "%":
		return arith(b.op, left, right)
	case "<", "<=", ">", ">=":
		return compare(b.op, left, right)
	}
	return nil, apperr.Internal("automation.errors.expr_unknown_operator", "op", b.op)
}

func arith(op string, l, r any) (any, error) {
	a, aok := toNumber(l)
	b, bok := toNumber(r)
	if !aok || !bok {
		return nil, apperr.Unprocessable("automation.errors.expr_needs_numbers", "op", op, "left", l, "right", r)
	}
	switch op {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		if b == 0 {
			return nil, apperr.Unprocessable("automation.errors.expr_division_by_zero")
		}
		return a / b, nil
	case "%":
		if b == 0 {
			return nil, apperr.Unprocessable("automation.errors.expr_modulo_by_zero")
		}
		return math.Mod(a, b), nil
	}
	return nil, apperr.Internal("automation.errors.expr_unknown_arithmetic", "op", op)
}

func compare(op string, l, r any) (any, error) {
	// nil compares false on purpose so `steps.x.json.count > 0` on a step that never ran is false,
	// not an error.
	if l == nil || r == nil {
		return false, nil
	}
	if isString(l) && isString(r) {
		a, b := toString(l), toString(r)
		switch op {
		case "<":
			return a < b, nil
		case "<=":
			return a <= b, nil
		case ">":
			return a > b, nil
		case ">=":
			return a >= b, nil
		}
	}
	a, aok := toNumber(l)
	b, bok := toNumber(r)
	if !aok || !bok {
		return nil, apperr.Unprocessable("automation.errors.expr_cannot_compare", "left", l, "right", r)
	}
	switch op {
	case "<":
		return a < b, nil
	case "<=":
		return a <= b, nil
	case ">":
		return a > b, nil
	case ">=":
		return a >= b, nil
	}
	return nil, apperr.Internal("automation.errors.expr_unknown_comparison", "op", op)
}

func evalCall(c *callNode, ctx map[string]any) (any, error) {
	args := make([]any, 0, len(c.args))
	if c.name == "exists" {
		if len(c.args) != 1 {
			return nil, apperr.Invalid("automation.errors.expr_exists_arity")
		}
		v, err := evalNode(c.args[0], ctx)
		if err != nil {
			return nil, err
		}
		return v != nil, nil
	}
	for _, a := range c.args {
		v, err := evalNode(a, ctx)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	return callFunction(c.name, args)
}

type exprFunc struct {
	arity int
	call  func(args []any) (any, error)
}

var exprFuncs = map[string]exprFunc{
	"size": {1, func(a []any) (any, error) { return float64(sizeOf(a[0])), nil }},
	"len":  {1, func(a []any) (any, error) { return float64(sizeOf(a[0])), nil }},
	"contains": {2, func(a []any) (any, error) {
		return contains(a[0], a[1]), nil
	}},
	"lower": {1, func(a []any) (any, error) { return strings.ToLower(toString(a[0])), nil }},
	"upper": {1, func(a []any) (any, error) { return strings.ToUpper(toString(a[0])), nil }},
	"trim":  {1, func(a []any) (any, error) { return strings.TrimSpace(toString(a[0])), nil }},
	"default": {2, func(a []any) (any, error) {
		if truthy(a[0]) {
			return a[0], nil
		}
		return a[1], nil
	}},
	"int": {1, func(a []any) (any, error) {
		n, ok := toNumber(a[0])
		if !ok {
			return nil, apperr.Unprocessable("automation.errors.expr_int_convert", "value", a[0])
		}
		return math.Trunc(n), nil
	}},
	"string": {1, func(a []any) (any, error) { return toString(a[0]), nil }},
	"matches": {2, func(a []any) (any, error) {
		re, err := regexp.Compile(toString(a[1]))
		if err != nil {
			return nil, invalidWrap(err, "automation.errors.expr_matches")
		}
		return re.MatchString(toString(a[0])), nil
	}},
	"slice": {2, func(a []any) (any, error) {
		n, _ := toNumber(a[1])
		items, ok := a[0].([]any)
		if !ok {
			return a[0], nil
		}
		if int(n) < len(items) {
			return items[:int(n)], nil
		}
		return items, nil
	}},
	"join": {2, func(a []any) (any, error) {
		items, ok := a[0].([]any)
		if !ok {
			return toString(a[0]), nil
		}
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = toString(it)
		}
		return strings.Join(parts, toString(a[1])), nil
	}},
}

func callFunction(name string, args []any) (any, error) {
	fn, ok := exprFuncs[name]
	if !ok {
		return nil, apperr.Invalid("automation.errors.expr_unknown_function", "name", name)
	}
	if len(args) != fn.arity {
		return nil, apperr.Invalid("automation.errors.expr_arity", "name", name, "arity", fn.arity, "got", len(args))
	}
	return fn.call(args)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

func sizeOf(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		return len(t)
	case []any:
		return len(t)
	case map[string]any:
		return len(t)
	case []string:
		return len(t)
	default:
		return 0
	}
}

func contains(haystack, needle any) bool {
	switch h := haystack.(type) {
	case string:
		return strings.Contains(h, toString(needle))
	case []any:
		for _, item := range h {
			if equals(item, needle) {
				return true
			}
		}
	case []string:
		for _, item := range h {
			if item == toString(needle) {
				return true
			}
		}
	case map[string]any:
		_, ok := h[toString(needle)]
		return ok
	}
	return false
}

func equals(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if isString(a) && isString(b) {
		return toString(a) == toString(b)
	}
	if an, aok := toNumber(a); aok {
		if bn, bok := toNumber(b); bok {
			return an == bn
		}
	}
	if ab, aok := a.(bool); aok {
		if bb, bok := b.(bool); bok {
			return ab == bb
		}
	}
	return false
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func toNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return n, err == nil
	}
	return 0, false
}

func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

var templatePattern = regexp.MustCompile(`\{\{([^}]*)\}\}`)

func Render(s string, ctx map[string]any) (string, error) {
	var firstErr error
	out := templatePattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := strings.TrimSpace(match[2 : len(match)-2])
		if inner == "" {
			return ""
		}
		e, err := Compile(inner)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return ""
		}
		v, err := e.Eval(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return ""
		}
		return toString(v)
	})
	return out, firstErr
}

func ValidateTemplate(s string) error {
	for _, m := range templatePattern.FindAllStringSubmatch(s, -1) {
		inner := strings.TrimSpace(m[1])
		if inner == "" {
			continue
		}
		if _, err := Compile(inner); err != nil {
			return err
		}
	}
	return nil
}
