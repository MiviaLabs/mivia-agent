package agenttools

import (
	"context"
	"strings"
	"testing"
)

func TestServiceReadMethodsValidateArguments(t *testing.T) {
	service := &Service{}
	cases := []struct {
		name string
		call func() error
	}{
		{"status run", func() error { _, err := service.Status(context.Background(), " "); return err }},
		{"events run", func() error { _, err := service.Events(context.Background(), "", 1, 0); return err }},
		{"events page", func() error { _, err := service.Events(context.Background(), "run", -1, 0); return err }},
		{"inspect run", func() error { _, err := service.Inspect(context.Background(), "", "step", 1, 0, 1); return err }},
		{"inspect step", func() error { _, err := service.Inspect(context.Background(), "run", "", 1, 0, 1); return err }},
		{"inspect attempt", func() error { _, err := service.Inspect(context.Background(), "run", "step", 0, 0, 1); return err }},
		{"inspect page", func() error { _, err := service.Inspect(context.Background(), "run", "step", 1, -1, 1); return err }},
		{"list page", func() error { _, err := service.ListRuns(context.Background(), "", -1, 0); return err }},
		{"list status", func() error { _, err := service.ListRuns(context.Background(), "unknown", 1, 0); return err }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), "required") && !strings.Contains(err.Error(), ">= 0") && !strings.Contains(err.Error(), "unknown status") && !strings.Contains(err.Error(), ">= 1") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
