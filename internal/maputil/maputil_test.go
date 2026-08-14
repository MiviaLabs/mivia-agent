package maputil

import (
	"reflect"
	"testing"
)

// TestSortedKeys checks that keys come back in ascending lexical order for
// already-sorted, unsorted, single-key, and multi-byte key sets.
func TestSortedKeys(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]string
		want []string
	}{
		{
			name: "already sorted",
			m:    map[string]string{"alpha": "a", "beta": "b", "gamma": "g"},
			want: []string{"alpha", "beta", "gamma"},
		},
		{
			name: "unsorted input",
			m:    map[string]string{"zulu": "z", "alpha": "a", "mike": "m"},
			want: []string{"alpha", "mike", "zulu"},
		},
		{
			name: "single key",
			m:    map[string]string{"only": "v"},
			want: []string{"only"},
		},
		{
			name: "lexical byte order",
			m:    map[string]string{"banana": "", "Apple": "", "apple": "", "éclair": ""},
			want: []string{"Apple", "apple", "banana", "éclair"},
		},
	}
	for _, c := range cases {
		if got := SortedKeys(c.m); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SortedKeys(%v) = %v, want %v", c.m, got, c.want)
		}
	}
}

// TestSortedKeysEmpty checks that nil and empty maps both return nil.
func TestSortedKeysEmpty(t *testing.T) {
	if got := SortedKeys(nil); got != nil {
		t.Errorf("SortedKeys(nil) = %v, want nil", got)
	}
	if got := SortedKeys(map[string]string{}); got != nil {
		t.Errorf("SortedKeys(empty map) = %v, want nil", got)
	}
}

// TestSortedKeysResultIsCopy checks that the returned slice is independent of
// the map: later map mutations must not change the result.
func TestSortedKeysResultIsCopy(t *testing.T) {
	m := map[string]string{"a": "1", "b": "2"}
	got := SortedKeys(m)
	delete(m, "a")
	m["c"] = "3"
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result changed with the map: %v, want %v", got, want)
	}
}

// TestSortedKeysDeterministic checks that repeated calls return identical
// results regardless of map iteration order.
func TestSortedKeysDeterministic(t *testing.T) {
	m := map[string]string{"x": "1", "a": "2", "m": "3", "q": "4"}
	first := SortedKeys(m)
	for i := 0; i < 50; i++ {
		if got := SortedKeys(m); !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d returned %v, want %v", i, got, first)
		}
	}
}
