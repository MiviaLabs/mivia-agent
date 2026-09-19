package agenttools

import (
	"context"
	"strings"
	"testing"
)

func TestNilServiceReadMethodsRefuseMissingRepository(t *testing.T) {
	var service *Service
	cases := []struct {
		name string
		call func() error
	}{
		{"status", func() error { _, err := service.Status(context.Background(), "run"); return err }},
		{"events", func() error { _, err := service.Events(context.Background(), "run", 1, 0); return err }},
		{"inspect", func() error { _, err := service.Inspect(context.Background(), "run", "step", 1, 0, 1); return err }},
		{"list", func() error { _, err := service.ListRuns(context.Background(), "", 1, 0); return err }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), "repository factory") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
