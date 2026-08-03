package subagents_test

// F5 regression: the sub-agent schema-retry budget must be charged for REAL
// loop steps only, never for wall-clock heartbeats. Pre-b3, the model-thinking
// heartbeat (a "working" EventStep every 2s while a provider request is in
// flight) inflated stepCount in multi_step.go, so a slow first request could
// consume the whole MaxSteps budget and starve the corrective schema re-entry
// with "no step budget remaining for schema retry". b3 moved that cadence to
// EventHeartbeat, which multi_step.go ignores, so step_count now reflects real
// steps only.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// slowFirstSchemaCompleter delays only the FIRST provider request so the
// model-thinking heartbeat (default 2s in internal/agent; the interval var is
// unexported, so the test drives the sleep instead) fires while it is in
// flight. Later turns return instantly, so the corrective re-entry is fast.
type slowFirstSchemaCompleter struct {
	replies []string
	delay   time.Duration
	i       int
}

func (c *slowFirstSchemaCompleter) Name() string { return "slow-first-schema" }

func (c *slowFirstSchemaCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}

func (c *slowFirstSchemaCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}

func (c *slowFirstSchemaCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	if c.i == 0 && c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.i >= len(c.replies) {
		return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
	}
	r := c.replies[c.i]
	c.i++
	return &provider.Response{Content: r, FinishReason: "stop"}, nil
}

func TestSchemaRetryStepBudgetNotStarvedByHeartbeats(t *testing.T) {
	// The model-thinking heartbeat interval is 2s and is unexported in
	// internal/agent, so make the first provider request outlive it (~3s) to
	// guarantee at least one heartbeat while it is in flight. MaxSteps=2 is
	// exactly one real step plus one corrective re-entry; with the F5 bug a
	// single phantom heartbeat EventStep consumes the budget and the re-entry
	// dies with "no step budget remaining for schema retry".
	const firstRequestDelay = 3 * time.Second

	reg := tools.NewRegistry()
	h := &subagents.MultiStepHandler{
		Completer: &slowFirstSchemaCompleter{
			replies: []string{`not json`, `{"ok":true}`},
			delay:   firstRequestDelay,
		},
		FullRegistry:   reg,
		Model:          "m",
		MaxSteps:       2,
		SchemaRetryMax: 2,
		OutputSchema:   schemaObject(),
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-heartbeat", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: schemaObject(),
	})
	if err != nil {
		t.Fatalf("schema retry must not be starved by wall-clock heartbeats: %v", err)
	}
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	if payload["schema"] != "ok" || payload["status"] != "completed" {
		t.Fatalf("want completed schema-ok result after corrective re-entry, got %#v", payload)
	}
	// step_count must reflect REAL steps (1 first attempt + 1 corrective
	// re-entry), not heartbeat ticks.
	if got, want := payload["step_count"], float64(2); got != want {
		t.Fatalf("step_count = %v, want %v (real steps, not heartbeat counts)", got, want)
	}
}
