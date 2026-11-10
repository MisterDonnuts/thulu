package engine

import (
	"bufio"
	"os"
	"strings"
)

func AuthFromEnvFile(envFile string, keys []string, alias map[string]string) []string {
	f, err := os.Open(envFile) //nolint:gosec // G304: envFile comes from daemon config, not user input
	if err != nil {
		return nil
	}
	defer f.Close()

	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[k] = true
	}

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !wanted[key] {
			continue
		}
		val := unquote(strings.TrimSpace(line[eq+1:]))
		if to, ok := alias[key]; ok {
			key = to
		}
		out = append(out, key+"="+val)
	}
	return out
}

func unquote(v string) string {
	if len(v) < 2 {
		return v
	}
	first, last := v[0], v[len(v)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
