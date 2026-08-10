package cli

// FuzzFlagValueNoSwallow sweeps the hardened shared entrypoint flag parsers
// flagValue/flagVar (internal/cli/root.go) after the DC-9 (silent failure /
// fail-open guard) fix. A successful parse must never return a dash-prefixed
// value unless the explicit name=value form is present in the input
// (invariant a), and must never leave a flag name in rest (invariant b). The
// old swallow bug violated both on inputs such as "-p --plain" (prompt became
// "--plain", the --plain flag was dropped) and "-p" (bare trailing flag left
// as a positional).

import (
	"strings"
	"testing"
)

func FuzzFlagValueNoSwallow(f *testing.F) {
	flagNames := []string{"-p", "--prompt"}
	// Seed corpus from the approved test plan: valid space form, "=" form with
	// a dash value, repeatable "=" values, bare trailing flag, flag-like
	// value, empty input, and a well-formed mixed invocation.
	f.Add("--prompt hello --plain")
	f.Add("--prompt=--plain")
	f.Add("--prompt=--value --prompt=--value2")
	f.Add("-p")
	f.Add("-p --plain")
	f.Add("")
	f.Add("--prompt hello -p --plain")

	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Fields(joined)

		val, rest, found, err := flagValue(args, flagNames...)
		if err != nil {
			if !strings.Contains(err.Error(), "requires a value") {
				t.Fatalf("flagValue(%q, %v) unexpected error %q", joined, flagNames, err)
			}
			// The refused parse must not be a swallowed one: at least one
			// space-form flag with a missing or flag-like value is present.
			swallow := false
			for i, a := range args {
				for _, n := range flagNames {
					if a == n && (i+1 >= len(args) || strings.HasPrefix(args[i+1], "-")) {
						swallow = true
					}
				}
			}
			if !swallow {
				t.Fatalf("flagValue(%q, %v) errored without a missing or flag-like value", joined, flagNames)
			}
			return
		}
		// Invariant (a): a dash-prefixed value must come from the "=" form.
		if found && strings.HasPrefix(val, "-") {
			hasEquals := false
			for _, n := range flagNames {
				if strings.Contains(joined, n+"="+val) {
					hasEquals = true
					break
				}
			}
			if !hasEquals {
				t.Fatalf("flagValue(%q, %v) returned dash value %q without an = form", joined, flagNames, val)
			}
		}
		// Invariant (b): a flag name must never survive in rest.
		for _, r := range rest {
			for _, n := range flagNames {
				if r == n {
					t.Fatalf("flagValue(%q, %v) left flag name %q in rest %q", joined, flagNames, n, rest)
				}
			}
		}

		vals, rest, found, err := flagVar(args, "--input")
		if err != nil {
			if !strings.Contains(err.Error(), "--input requires a value") {
				t.Fatalf("flagVar(%q, --input) unexpected error %q", joined, err)
			}
			return
		}
		// Same invariants for the repeatable parser.
		if found {
			for _, v := range vals {
				if strings.HasPrefix(v, "-") && !strings.Contains(joined, "--input="+v) {
					t.Fatalf("flagVar(%q, --input) returned dash value %q without an = form", joined, v)
				}
			}
		}
		for _, r := range rest {
			if r == "--input" {
				t.Fatalf("flagVar(%q, --input) left flag name in rest %q", joined, rest)
			}
		}
	})
}
