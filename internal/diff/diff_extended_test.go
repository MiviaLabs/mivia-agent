package diff

import "testing"

func TestTrimContext(t *testing.T) {
	tests := []struct {
		name    string
		ops     []Op
		context int
		want    int
	}{
		{"empty", nil, 3, 0},
		{"all equal", []Op{{Kind: Equal, Lines: []string{"a", "b"}}}, 3, 2},
		{"single delete", []Op{{Kind: Equal, Lines: []string{"a"}}, {Kind: Delete, Lines: []string{"b"}}, {Kind: Equal, Lines: []string{"c"}}}, 1, 3},
		{"zero context", []Op{{Kind: Equal, Lines: []string{"a"}}, {Kind: Delete, Lines: []string{"b"}}}, 0, 1},
		{"trailing empty equal trimmed", []Op{{Kind: Delete, Lines: []string{"b"}}, {Kind: Equal, Lines: []string{""}}}, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimContext(tt.ops, tt.context)
			if len(got) != tt.want {
				t.Errorf("trimContext() got %d ops, want %d", len(got), tt.want)
			}
		})
	}
}

func TestMinInt(t *testing.T) {
	tests := []struct{ a, b, want int }{
		{1, 2, 1}, {2, 1, 1}, {0, 0, 0}, {-1, 1, -1},
	}
	for _, tt := range tests {
		if got := minInt(tt.a, tt.b); got != tt.want {
			t.Errorf("minInt(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
