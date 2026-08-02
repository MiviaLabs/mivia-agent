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
	cases := []struct {
		name          string
		max           int
		writes        []string
		want          string
		wantTruncated bool
		wantWritten   int64
	}{
		{name: "empty write", max: 4, writes: []string{""}, wantWritten: 0},
		{name: "unlimited", max: 0, writes: []string{"abcdef"}, want: "abcdef", wantWritten: 6},
		{name: "exact capacity then overflow", max: 4, writes: []string{"abcd", "e"}, want: "abcd", wantTruncated: true, wantWritten: 5},
		{name: "single write crosses capacity", max: 4, writes: []string{"abcdef"}, want: "abcd", wantTruncated: true, wantWritten: 6},
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
			if got := string(buffer.Bytes()); got != tc.want {
				t.Errorf("retained bytes = %q, want %q", got, tc.want)
			}
			if got := buffer.Truncated(); got != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v", got, tc.wantTruncated)
			}
			if got := buffer.Written(); got != tc.wantWritten {
				t.Errorf("written = %d, want %d", got, tc.wantWritten)
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
