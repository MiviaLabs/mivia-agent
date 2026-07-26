// Package envfile loads KEY=VALUE dotenv files without printing secrets.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Load reads path and returns a map of keys to values.
// Lines that are empty or start with # are ignored.
// Values may be optionally single- or double-quoted.
// Does not modify the process environment.
func Load(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		val = strings.TrimSpace(val)
		val = unquote(val)
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// Lookup returns the value for key preferring process environment over file map.
func Lookup(key string, file map[string]string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v, true
	}
	if file != nil {
		if v, ok := file[key]; ok && v != "" {
			return v, true
		}
	}
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	return "", false
}
