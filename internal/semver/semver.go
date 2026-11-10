package semver

import (
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Version struct {
	Major, Minor, Patch int
	Pre                 []string
	Build               string
}

func Parse(s string) (Version, bool) {
	var v Version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build = s[i+1:]
		s = s[:i]
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre := s[i+1:]
		if pre == "" {
			return Version{}, false
		}
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return Version{}, false
			}
			v.Pre = append(v.Pre, id)
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		*nums[i] = n
	}
	return v, true
}

func MustParse(s string) Version {
	v, ok := Parse(s)
	if !ok {
		panic(apperr.Invalid("semver.errors.invalid_version", "version", s))
	}
	return v
}

func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

func (v Version) Prerelease() bool { return len(v.Pre) > 0 }

func (v Version) MinorLine() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
}

func Compare(a, b Version) int {
	for _, d := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			return cmpInt(d[0], d[1])
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdent(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Pre), len(b.Pre))
}

func compareIdent(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func Less(a, b string) bool {
	va, okA := Parse(a)
	vb, okB := Parse(b)
	return okA && okB && Compare(va, vb) < 0
}

func Newer(a, b string) bool { return Less(b, a) }

func Satisfies(v Version, constraint string) bool {
	for _, c := range strings.FieldsFunc(constraint, func(r rune) bool { return r == ' ' || r == ',' }) {
		rest := strings.TrimLeft(c, "<>=")
		op := c[:len(c)-len(rest)]
		want, ok := Parse(rest)
		if !ok {
			return false
		}
		cmp := Compare(v, want)
		var hold bool
		switch op {
		case ">=":
			hold = cmp >= 0
		case ">":
			hold = cmp > 0
		case "<=":
			hold = cmp <= 0
		case "<":
			hold = cmp < 0
		case "=", "":
			hold = cmp == 0
		default:
			return false
		}
		if !hold {
			return false
		}
	}
	return true
}

func ValidConstraint(constraint string) error {
	parts := strings.FieldsFunc(constraint, func(r rune) bool { return r == ' ' || r == ',' })
	if len(parts) == 0 {
		return apperr.Invalid("semver.errors.empty_constraint")
	}
	for _, c := range parts {
		rest := strings.TrimLeft(c, "<>=")
		switch op := c[:len(c)-len(rest)]; op {
		case ">=", ">", "<=", "<", "=", "":
		default:
			return apperr.Invalid("semver.errors.unknown_operator", "constraint", c, "op", op)
		}
		if _, ok := Parse(rest); !ok {
			return apperr.Invalid("semver.errors.not_a_version", "constraint", c, "version", rest)
		}
	}
	return nil
}
