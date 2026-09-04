package cliagents

import "testing"

func TestSameFilePath(t *testing.T) {
	cases := []struct {
		goos, a, b string
		want       bool
	}{
		{"linux", "/tmp/mivia/m.db", "/tmp/mivia/m.db", true},
		{"windows", `C:\Temp\M.DB`, `c:\temp\m.db`, true},
		{"linux", "/tmp/M.DB", "/tmp/m.db", false},
		{"linux", "/tmp/x/../m.db", "/tmp/m.db", true},
		{"linux", "", "/tmp/m.db", false},
	}
	for _, tc := range cases {
		if got := SameFilePath(tc.goos, tc.a, tc.b); got != tc.want {
			t.Errorf("SameFilePath(%q,%q,%q)=%v, want %v", tc.goos, tc.a, tc.b, got, tc.want)
		}
	}
}
