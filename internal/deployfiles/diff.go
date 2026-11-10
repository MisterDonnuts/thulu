package deployfiles

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Bounds the quadratic LCS diff; past this the diff only says the files differ.
const maxDiffLines = 4000

const contextLines = 3

func Diff(name string, a, b []byte) string {
	if bytes.Equal(a, b) {
		return ""
	}
	al, bl := splitLines(string(a)), splitLines(string(b))
	if len(al) > maxDiffLines || len(bl) > maxDiffLines {
		return i18n.Text("deployfiles.labels.diff_header", "name", name) + i18n.Text("deployfiles.labels.diff_too_long")
	}
	ops := lineOps(al, bl)

	var out strings.Builder
	out.WriteString(i18n.Text("deployfiles.labels.diff_header", "name", name))
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start, stop := hunkBounds(ops, i)
		writeHunk(&out, ops[start:stop])
		i = stop
	}
	return out.String()
}

func hunkBounds(ops []lineOp, i int) (start, stop int) {
	end := i
	for j := i; j < len(ops); j++ {
		if ops[j].kind != ' ' {
			end = j
			continue
		}
		if j-end > 2*contextLines {
			break
		}
	}
	return max(i-contextLines, 0), min(end+contextLines+1, len(ops))
}

func writeHunk(out *strings.Builder, ops []lineOp) {
	aCount, bCount := 0, 0
	var body strings.Builder
	for _, op := range ops {
		body.WriteByte(op.kind)
		body.WriteString(op.text)
		body.WriteByte('\n')
		if op.kind != '+' {
			aCount++
		}
		if op.kind != '-' {
			bCount++
		}
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n%s", ops[0].a+1, aCount, ops[0].b+1, bCount, body.String())
}

type lineOp struct {
	kind byte
	text string
	a, b int
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lineOps(a, b []string) []lineOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []lineOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, lineOp{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, lineOp{'+', b[j], i, j})
			j++
		}
	}
	return ops
}
