// Package runtime contains the shared invocation boundary for model-directed work.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	Tool     Kind = "tool"
	Skill    Kind = "skill"
	Subagent Kind = "subagent"
)

type Request struct {
	ID, ParentID, TurnID, Name, Scope string
	Kind                              Kind
	Input                             json.RawMessage
	Timeout                           time.Duration
	Budget                            int
	Permission                        string
	Depth                             int
	Retry                             int
}
type Result struct {
	ID, Name string
	Kind     Kind
	Output   json.RawMessage
	Err      error
	Attempts int
	Metadata Metadata
}
type Metadata struct {
	ID, ParentID, TurnID, Name, Kind, Status, Scope, InputHash, OutputHash string
	Duration                                                               time.Duration
	RedactedInput, RedactedOutput                                          string
}
type Event struct {
	Type     string
	Metadata Metadata
}
type Handler interface {
	Invoke(context.Context, Request) (json.RawMessage, error)
}
type Policy struct {
	MaxDepth, MaxRetries, MaxInputBytes, MaxOutputBytes int
	Allow                                               map[Kind]map[string]bool
	Sink                                                func(Event)
}
type Dispatcher struct {
	mu       sync.Mutex
	handlers map[Kind]map[string]Handler
	active   map[string]struct{}
	policy   Policy
}

func New(policy Policy) *Dispatcher {
	if policy.MaxDepth <= 0 {
		policy.MaxDepth = 8
	}
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	if policy.MaxInputBytes <= 0 {
		policy.MaxInputBytes = 64 << 10
	}
	if policy.MaxOutputBytes <= 0 {
		policy.MaxOutputBytes = 256 << 10
	}
	return &Dispatcher{handlers: map[Kind]map[string]Handler{Tool: {}, Skill: {}, Subagent: {}}, active: map[string]struct{}{}, policy: policy}
}
func (d *Dispatcher) Register(k Kind, name string, h Handler) error {
	if h == nil || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid handler")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.handlers[k]; !ok {
		return fmt.Errorf("invalid kind %q", k)
	}
	if _, ok := d.handlers[k][name]; ok {
		return fmt.Errorf("duplicate handler %q", name)
	}
	d.handlers[k][name] = h
	return nil
}
func (d *Dispatcher) Invoke(ctx context.Context, req Request) Result {
	started := time.Now()
	if req.ID == "" {
		sum := sha256.Sum256(append([]byte(req.Name), req.Input...))
		req.ID = hex.EncodeToString(sum[:8])
	}
	meta := Metadata{ID: req.ID, ParentID: req.ParentID, TurnID: req.TurnID, Name: req.Name, Kind: string(req.Kind), Scope: req.Scope, InputHash: hash(req.Input)}
	fail := func(err error) Result {
		meta.Status = "failed"
		meta.Duration = time.Since(started)
		d.emit(meta)
		return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: err, Metadata: meta}
	}
	if len(req.Input) > d.policy.MaxInputBytes {
		return fail(fmt.Errorf("input budget exceeded"))
	}
	if req.Depth > d.policy.MaxDepth {
		return fail(fmt.Errorf("invocation depth exceeded"))
	}
	d.mu.Lock()
	h := d.handlers[req.Kind][req.Name]
	allowed := d.policy.Allow == nil || d.policy.Allow[req.Kind] == nil || d.policy.Allow[req.Kind][req.Name]
	_, dup := d.active[req.ID]
	if h != nil && !dup {
		d.active[req.ID] = struct{}{}
	}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.active, req.ID); d.mu.Unlock() }()
	if h == nil {
		return fail(fmt.Errorf("unknown %s %q", req.Kind, req.Name))
	}
	if !allowed {
		return fail(fmt.Errorf("permission denied for %q", req.Name))
	}
	if dup {
		return fail(fmt.Errorf("duplicate or recursive invocation %q", req.ID))
	}
	callCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	var out []byte
	var err error
	attempts := req.Retry + 1
	if attempts > d.policy.MaxRetries+1 {
		attempts = d.policy.MaxRetries + 1
	}
	for i := 0; i < attempts; i++ {
		out, err = h.Invoke(callCtx, req)
		if err == nil {
			break
		}
		if callCtx.Err() != nil {
			break
		}
	}
	if err != nil {
		return fail(err)
	}
	if len(out) > d.policy.MaxOutputBytes {
		return fail(fmt.Errorf("output budget exceeded"))
	}
	meta.Status = "completed"
	meta.OutputHash = hash(out)
	meta.Duration = time.Since(started)
	meta.RedactedInput = redact(req.Input)
	meta.RedactedOutput = redact(out)
	d.emit(meta)
	return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Output: out, Attempts: attempts, Metadata: meta}
}
func (d *Dispatcher) emit(m Metadata) {
	if d.policy.Sink != nil {
		d.policy.Sink(Event{Type: m.Status, Metadata: m})
	}
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func redact(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return "[redacted]"
	}
	scrub(v)
	x, _ := json.Marshal(v)
	if len(x) > 256 {
		x = x[:256]
	}
	return string(x)
}
func scrub(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			l := strings.ToLower(k)
			if strings.Contains(l, "secret") || strings.Contains(l, "token") || strings.Contains(l, "password") || strings.Contains(l, "authorization") {
				x[k] = "[redacted]"
			} else {
				scrub(val)
			}
		}
	case []any:
		for _, val := range x {
			scrub(val)
		}
	}
}
