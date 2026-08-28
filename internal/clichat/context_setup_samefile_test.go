package clichat

import "testing"

// TestSameFilePath pins the pure path comparison behind the ad-hoc
// orchestration-store hardening gate: both spellings normalize (backslashes
// to slashes, dot-dot and double-slash segments resolve away), then compare
// case-folded on Windows and byte-exact elsewhere. Empty paths never match.
func TestSameFilePath(t *testing.T) {
	cases := []struct {
		name  string
		goos  string
		a     string
		b     string
		match bool
	}{
		{name: "identical", goos: "linux", a: "/t/x/orchestration.db", b: "/t/x/orchestration.db", match: true},
		{name: "dot-dot collapses", goos: "linux", a: "/t/x/../x/orchestration.db", b: "/t/x/orchestration.db", match: true},
		{name: "double slash collapses", goos: "linux", a: "/t//x/orchestration.db", b: "/t/x/orchestration.db", match: true},
		{name: "case differs on linux", goos: "linux", a: "/T/x/orchestration.db", b: "/t/x/orchestration.db", match: false},
		{name: "case folds on windows", goos: "windows", a: `C:\T\X\orchestration.db`, b: "C:/t/x/orchestration.db", match: true},
		{name: "trailing separator folds on windows", goos: "windows", a: `C:\T\X\orchestration.db`, b: `C:\T\X\orchestration.db\`, match: true},
		{name: "empty never matches", goos: "linux", a: "", b: "/t/x/orchestration.db", match: false},
		{name: "both empty never matches", goos: "windows", a: "", b: "", match: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameFilePath(tc.goos, tc.a, tc.b); got != tc.match {
				t.Errorf("sameFilePath(%q, %q, %q) = %v, want %v", tc.goos, tc.a, tc.b, got, tc.match)
			}
		})
	}
}
