package cliworkflow

import "testing"

func TestParseWorkflowInputValueEnforcesDeclaredType(t *testing.T) {
	tests := []struct {
		name  string
		value string
		typ   string
		want  bool
	}{
		{name: "boolean", value: "true", typ: "boolean", want: true},
		{name: "integer", value: "12", typ: "integer", want: true},
		{name: "number", value: "1.25", typ: "number", want: true},
		{name: "object", value: `{"ok":true}`, typ: "object", want: true},
		{name: "array", value: `[1,2]`, typ: "array", want: true},
		{name: "boolean mismatch", value: "12", typ: "boolean"},
		{name: "integer fraction", value: "1.2", typ: "integer"},
		{name: "invalid JSON", value: "{", typ: "object"},
		{name: "trailing JSON", value: "true false", typ: "boolean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseWorkflowInputValue(test.value, test.typ)
			if (err == nil) != test.want {
				t.Fatalf("error = %v, want success %v", err, test.want)
			}
		})
	}
}
