package cliorchestrate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// handlerFunc implements runtime.Handler for test subagents.
type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

// Invoke dispatches to the underlying func.
func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

// testAgentRegistry creates an AgentRegistry with the named agents registered.
func testAgentRegistry(t *testing.T, names ...string) *agents.AgentRegistry {
	t.Helper()
	reg := agents.NewRegistry()
	for _, name := range names {
		if err := reg.Publish(agents.ResolvedAgent{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// itoa converts a small integer to a string without importing fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	neg := n < 0
	if neg {
		n = -n
	}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
