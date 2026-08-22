package cliworkflow

// test_completer_helpers_test.go duplicates cli's bindingProbeCompleter test
// stub (test_helpers_moved_test.go): it records the (provider, model) each
// turn actually ran against.

import (
	"context"
	"io"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type bindingProbeCompleter struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (c *bindingProbeCompleter) Name() string { return c.name }

func (c *bindingProbeCompleter) record(req provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, c.name+"/"+req.Model)
}

func (c *bindingProbeCompleter) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *bindingProbeCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatStream(_ context.Context, req provider.Request, _ io.Writer) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.record(req)
	return &provider.Response{}, nil
}
