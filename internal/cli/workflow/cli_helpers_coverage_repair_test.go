package workflow

import (
	"bytes"
	"strings"
	"testing"
)

// These tests cover the changed-but-unexercised helper branches flagged by
// the diff-coverage gate after the stack-driver move: LogMCPWarnings' nil
// guards and FlagVar's "=" form.

func TestLogMCPWarningsNilGuards(t *testing.T) {
	LogMCPWarnings(nil, nil)
	var buf bytes.Buffer
	LogMCPWarnings(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("nil res wrote %q", buf.String())
	}
}

func TestFlagVarEqualsFormCollectsValues(t *testing.T) {
	vals, rest, found, err := FlagVar([]string{"--tag=a", "positional", "--tag=b"}, "--tag")
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(vals) != 2 || vals[0] != "a" || vals[1] != "b" {
		t.Fatalf("FlagVar = (%v, %v, %v)", vals, rest, found)
	}
	if strings.Join(rest, ",") != "positional" {
		t.Fatalf("rest = %v", rest)
	}
	if _, _, _, err := FlagVar([]string{"--tag"}, "--tag"); err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("FlagVar missing-value error = %v", err)
	}
}
