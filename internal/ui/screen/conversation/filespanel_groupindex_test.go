package conversation

import "testing"

// TestPanelGroupToPickerIdxOutOfRange pins the trailing -1 arm: a group
// index past the last agent row (a click on padding below the list) maps
// to no picker item, same as the header rows.
func TestPanelGroupToPickerIdxOutOfRange(t *testing.T) {
	const files, agents = 2, 3
	cases := []struct {
		name string
		gIdx int
		want int
	}{
		{name: "model header", gIdx: 0, want: -1},
		{name: "model row", gIdx: 1, want: 0},
		{name: "files header", gIdx: 2, want: -1},
		{name: "first file", gIdx: 3, want: 1},
		{name: "last file", gIdx: 3 + files - 1, want: files},
		{name: "subagents header", gIdx: 3 + files, want: -1},
		{name: "first agent", gIdx: 4 + files, want: 1 + files},
		{name: "last agent", gIdx: 3 + files + agents, want: files + agents},
		{name: "past the last agent", gIdx: 4 + files + agents, want: -1},
		{name: "far past the list", gIdx: 100, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelGroupToPickerIdx(tc.gIdx, files, agents); got != tc.want {
				t.Errorf("panelGroupToPickerIdx(%d, %d, %d) = %d, want %d", tc.gIdx, files, agents, got, tc.want)
			}
		})
	}
}

// TestPanelSelGroupBounds pins both -1 arms of the inverse map: a negative
// picker index (no selection) and an index past the last agent row map to
// no group.
func TestPanelSelGroupBounds(t *testing.T) {
	const files, agents = 2, 3
	cases := []struct {
		name   string
		selIdx int
		want   int
	}{
		{name: "negative selection", selIdx: -1, want: -1},
		{name: "model row", selIdx: 0, want: 1},
		{name: "first file", selIdx: 1, want: 3},
		{name: "last file", selIdx: files, want: 3 + files - 1},
		{name: "first agent", selIdx: 1 + files, want: 4 + files},
		{name: "last agent", selIdx: files + agents, want: 3 + files + agents},
		{name: "past the last agent", selIdx: 1 + files + agents, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelSelGroup(tc.selIdx, files, agents); got != tc.want {
				t.Errorf("panelSelGroup(%d, %d, %d) = %d, want %d", tc.selIdx, files, agents, got, tc.want)
			}
		})
	}
}
