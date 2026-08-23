package config

import "os"

// Lookup returns the value for key, preferring the process environment over
// the file map. A blank process environment value falls through to the file;
// an unset process environment returns the file value; a blank file value
// falls back to a set-but-empty process environment value. The double
// os.LookupEnv call keeps the historical contract: a key set to "" in the
// process environment and absent from file resolves to ("", true), so callers
// can distinguish unset from set-but-blank when that distinction matters.
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
