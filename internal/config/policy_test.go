package config

import "testing"

func TestResolvedAllowsModel(t *testing.T) {
	tests := []struct {
		name   string
		models []string
		input  string
		want   bool
	}{
		{name: "unrestricted allows safe name", input: "anything", want: true},
		{name: "managed allows member", models: []string{"A", "B"}, input: " B ", want: true},
		{name: "managed rejects nonmember", models: []string{"A", "B"}, input: "C", want: false},
		{name: "rejects terminal control", input: "A\x1b]52;c;x", want: false},
		{name: "rejects empty", input: " ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &Resolved{Models: tt.models}
			if got := res.AllowsModel(tt.input); got != tt.want {
				t.Fatalf("AllowsModel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestModelChoices(t *testing.T) {
	if got := (&Resolved{Models: []string{"A", "B"}}).ModelChoices(); got != "A, B" {
		t.Fatalf("ModelChoices() = %q, want %q", got, "A, B")
	}
	if got := (&Resolved{}).ModelChoices(); got != "" {
		t.Fatalf("unrestricted choices = %q, want empty", got)
	}
}
