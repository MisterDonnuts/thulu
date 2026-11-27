package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

// Definition is what the installed launchd agent or systemd unit runs, read back from its file:
// someone may have edited it since `thulu runner service install` wrote it.
type Definition struct {
	File   string
	Binary string
	Env    map[string]string
}

// Installed reads the service file. ok is false when there is none.
func Installed(ctx context.Context) (def Definition, ok bool, err error) {
	switch goos {
	case "darwin":
		path, perr := plistPath()
		if perr != nil {
			return Definition{}, false, perr
		}
		b, rerr := readIfThere(path)
		if b == nil || rerr != nil {
			return Definition{}, false, rerr
		}
		// plutil turns a plist someone saved as binary back into XML.
		if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("<")) {
			if b, err = command(ctx, "plutil", "-convert", "xml1", "-o", "-", path); err != nil {
				return Definition{}, true, err
			}
		}
		def, err = parsePlist(b)
		def.File = path
		return def, true, err
	case "linux":
		path, perr := unitPath()
		if perr != nil {
			return Definition{}, false, perr
		}
		b, rerr := readIfThere(path)
		if b == nil || rerr != nil {
			return Definition{}, false, rerr
		}
		def = parseUnit(string(b))
		def.File = path
		return def, true, nil
	}
	return Definition{}, false, apperr.Precondition("service.errors.unsupported_os", "os", goos)
}

func readIfThere(path string) ([]byte, error) {
	// A symlinked service file is read where it points, inside that directory.
	resolved := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		resolved = r
	}
	b, err := safepath.ReadFile(filepath.Dir(resolved), filepath.Base(resolved))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "service.errors.read", "path", path)
	}
	return b, nil
}

// plistReader walks a launchd plist. Only the top dict's keys count: depth 1 is <plist>, 2 its
// <dict>, 3 its keys and values, 4 the items of an array or dict value.
type plistReader struct {
	def                      Definition
	depth                    int
	lastKey, section, envKey string
	text                     strings.Builder
}

// parsePlist takes ProgramArguments' first string and the EnvironmentVariables dict from a plist.
func parsePlist(b []byte) (Definition, error) {
	r := &plistReader{def: Definition{Env: map[string]string{}}}
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return r.def, nil
		}
		if err != nil {
			return r.def, apperr.Wrap(err, "service.errors.parse")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			r.depth++
			r.text.Reset()
			if r.depth == 3 && t.Name.Local != "key" {
				r.section = r.lastKey
			}
		case xml.CharData:
			r.text.Write(t)
		case xml.EndElement:
			r.end(t.Name.Local)
		}
	}
}

func (r *plistReader) end(elem string) {
	v := r.text.String()
	r.text.Reset()
	depth := r.depth
	r.depth--
	switch {
	case depth == 3 && elem == "key":
		r.lastKey = v
	case depth == 3:
		r.section = ""
	case depth != 4:
	case r.section == "ProgramArguments" && elem == "string" && r.def.Binary == "":
		r.def.Binary = v
	case r.section == "EnvironmentVariables" && elem == "key":
		r.envKey = v
	case r.section == "EnvironmentVariables" && elem == "string" && r.envKey != "":
		r.def.Env[r.envKey] = v
		r.envKey = ""
	}
}

// parseUnit reads ExecStart's binary and every Environment= assignment, undoing unitEscaped.
func parseUnit(body string) Definition {
	def := Definition{Env: map[string]string{}}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			words := unitWords(strings.TrimPrefix(line, "ExecStart="))
			if len(words) > 0 {
				def.Binary = strings.ReplaceAll(words[0], "$$", "$")
			}
		case strings.HasPrefix(line, "Environment="):
			for _, w := range unitWords(strings.TrimPrefix(line, "Environment=")) {
				if k, v, ok := strings.Cut(w, "="); ok {
					def.Env[k] = v
				}
			}
		}
	}
	return def
}

// unitWords splits a systemd value into words, honoring double quotes and their escapes.
func unitWords(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			have = true
		case c == '"':
			inQuote = !inQuote
			have = true
		case c == '%' && i+1 < len(s) && s[i+1] == '%':
			i++
			cur.WriteByte('%')
			have = true
		case (c == ' ' || c == '\t') && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// Merged is the environment an install writes, and what it took from the file already there.
type Merged struct {
	Path string
	Env  map[string]string
	// Variables the old file had that the new one keeps, PATH included when it kept entries.
	Kept []string
}

// MergeEnv builds the new service's environment. The old file's variables stay; its PATH keeps its
// entries in its order and gains the shell's missing ones; set (runner.json's service_env) wins
// over both.
func MergeEnv(old map[string]string, shellPath string, set map[string]string) Merged {
	m := Merged{Env: map[string]string{}}
	for k, v := range old {
		if k == "PATH" {
			continue
		}
		m.Env[k] = v
		if _, over := set[k]; !over {
			m.Kept = append(m.Kept, k)
		}
	}
	m.Path = shellPath
	if p, ok := old["PATH"]; ok && p != "" {
		m.Path = mergePath(p, shellPath)
		if _, over := set["PATH"]; !over && m.Path != shellPath {
			m.Kept = append(m.Kept, "PATH")
		}
	}
	for k, v := range set {
		if k == "PATH" {
			m.Path = v
			continue
		}
		m.Env[k] = v
	}
	sort.Strings(m.Kept)
	return m
}

func mergePath(first, then string) string {
	var out []string
	for _, list := range []string{first, then} {
		for _, d := range filepath.SplitList(list) {
			if d != "" && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return strings.Join(out, string(filepath.ListSeparator))
}

// Restart stops the running runner and starts it again from its file, picking up a new binary.
func Restart(ctx context.Context) error {
	switch goos {
	case "darwin":
		_, err := command(ctx, "launchctl", "kickstart", "-k", launchdDomain()+"/"+Label)
		return err
	case "linux":
		_, err := command(ctx, "systemctl", "--user", "restart", unitName)
		return err
	}
	return apperr.Precondition("service.errors.unsupported_os", "os", goos)
}
