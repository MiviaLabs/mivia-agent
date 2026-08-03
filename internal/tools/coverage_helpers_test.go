package tools

import (
	"strings"
	"testing"
)

func TestWebSearchCapabilityIsExternal(t *testing.T) {
	capability := (&webSearchTool{}).Capability(nil)
	if capability.Class != ExecutionExternal {
		t.Fatalf("web search capability class = %v, want %v", capability.Class, ExecutionExternal)
	}
}

func TestCappedBufferWriteBoundaries(t *testing.T) {
	// max=4 → headQuota=1, tailQuota=3
	cases := []struct {
		name          string
		max           int
		writes        []string
		wantExact     string // when set, full equality
		wantHead      string // when set, prefix of body before elision
		wantTail      string // when set, suffix after elision
		wantTruncated bool
		wantWritten   int64
	}{
		{name: "empty write", max: 4, writes: []string{""}, wantExact: "", wantWritten: 0},
		{name: "unlimited", max: 0, writes: []string{"abcdef"}, wantExact: "abcdef", wantWritten: 6},
		// head "a" + tail last 3 of "bcde" after head took "a" from "abcd" then "e" → tail "cde"
		{name: "exact capacity then overflow", max: 4, writes: []string{"abcd", "e"}, wantHead: "a", wantTail: "cde", wantTruncated: true, wantWritten: 5},
		// "abcdef": head "a", tail "def"
		{name: "single write crosses capacity", max: 4, writes: []string{"abcdef"}, wantHead: "a", wantTail: "def", wantTruncated: true, wantWritten: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buffer := newCappedBuffer(tc.max)
			for _, write := range tc.writes {
				n, err := buffer.Write([]byte(write))
				if err != nil || n != len(write) {
					t.Fatalf("Write(%q) = %d, %v", write, n, err)
				}
			}
			got := string(buffer.Bytes())
			if tc.wantExact != "" || (tc.wantHead == "" && tc.wantTail == "" && !tc.wantTruncated) {
				if got != tc.wantExact {
					t.Errorf("retained bytes = %q, want %q", got, tc.wantExact)
				}
			} else {
				if tc.wantHead != "" && !strings.HasPrefix(got, tc.wantHead) {
					t.Errorf("retained = %q, want head prefix %q", got, tc.wantHead)
				}
				if tc.wantTail != "" && !strings.HasSuffix(got, tc.wantTail) {
					t.Errorf("retained = %q, want tail suffix %q", got, tc.wantTail)
				}
				if tc.wantTruncated && !strings.Contains(got, captureElisionMarker) {
					t.Errorf("retained = %q, missing elision marker", got)
				}
			}
			if gotT := buffer.Truncated(); gotT != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v", gotT, tc.wantTruncated)
			}
			if gotW := buffer.Written(); gotW != tc.wantWritten {
				t.Errorf("written = %d, want %d", gotW, tc.wantWritten)
			}
		})
	}
}

func TestWriteEntity(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{code: "amp", want: "&"},
		{code: "lt", want: "<"},
		{code: "gt", want: ">"},
		{code: "quot", want: "\""},
		{code: "nbsp", want: " "},
		{code: "#169", want: "©"},
		{code: "#0", want: ""},
		{code: "unknown", want: "&unknown;"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			var out strings.Builder
			writeEntity(&out, tc.code)
			if got := out.String(); got != tc.want {
				t.Errorf("writeEntity(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}
