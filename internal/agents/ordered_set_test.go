package agents

import (
	"slices"
	"testing"
)

func TestOrderedSetIgnoresBlankAndDuplicateNames(t *testing.T) {
	set := newOrderedSet([]string{"read_file"})
	set.add("   ")
	set.add("")
	set.add("read_file")
	set.add(" grep ")
	if got := set.slice(); !slices.Equal(got, []string{"read_file", "grep"}) {
		t.Fatalf("set = %v, want [read_file grep]", got)
	}
}

func TestOrderedSetRemoveIsANoopForAbsentNames(t *testing.T) {
	set := newOrderedSet([]string{"read_file", "grep"})
	set.remove("never_added")
	set.remove("  ")
	if got := set.slice(); !slices.Equal(got, []string{"read_file", "grep"}) {
		t.Fatalf("set = %v, want the original order preserved", got)
	}
	set.remove(" read_file ")
	if got := set.slice(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("set = %v, want [grep] after removal", got)
	}
}
