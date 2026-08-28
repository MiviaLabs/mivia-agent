package cliworkflow

import "testing"

// TestSameFilePath pins the test-wiring copy of the pure path comparison
// (the production gate lives in clichat.openContextStore): normalization is
// host-independent, and only the Windows arm folds case.
func TestSameFilePath(t *testing.T) {
	cases := []struct {
		name  string
		goos  string
		a     string
		b     string
		match bool
	}{
		{name: "identical", goos: "linux", a: "/t/x/orchestration.db", b: "/t/x/orchestration.db", match: true},
		{name: "unclean spelling collapses", goos: "linux", a: "/t/./x//orchestration.db", b: "/t/x/orchestration.db", match: true},
		{name: "case differs on linux", goos: "linux", a: "/T/x/orchestration.db", b: "/t/x/orchestration.db", match: false},
		{name: "case folds on windows", goos: "windows", a: `C:\T\X\ORCH.DB`, b: "c:/t/x/orch.db", match: true},
		{name: "empty never matches", goos: "windows", a: "", b: `C:\T\X\ORCH.DB`, match: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameFilePath(tc.goos, tc.a, tc.b); got != tc.match {
				t.Errorf("sameFilePath(%q, %q, %q) = %v, want %v", tc.goos, tc.a, tc.b, got, tc.match)
			}
		})
	}
}
