package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// newRequest builds one /chat/completions HTTP request. It never mutates req.
func (c *OpenAICompat) newRequest(ctx context.Context, req Request) (*http.Request, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("%s: model is required", c.name)
	}
	if err := c.checkReservedExtras(); err != nil {
		return nil, err
	}
	raw, err := c.marshalBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq, req, raw)
	return httpReq, nil
}

// checkReservedExtras refuses operator-supplied extras that would overwrite
// fields this client owns.
func (c *OpenAICompat) checkReservedExtras() error {
	for key := range c.extraHeaders {
		for _, reserved := range []string{"Authorization", "Content-Type", "Accept", "Idempotency-Key"} {
			if strings.EqualFold(key, reserved) {
				return fmt.Errorf("%s: extra header %q is reserved", c.name, key)
			}
		}
	}
	for key := range c.extraBody {
		switch key {
		case "model", "messages", "stream":
			return fmt.Errorf("%s: extra body field %q is reserved", c.name, key)
		}
	}
	return nil
}

// marshalBody serializes the request body. The typed payload is round-tripped
// through a map so operator extras and reasoning fields can be merged by key.
//
// Merge order is the contract: typed payload, then ExtraBody, then reasoning.
// An active model-scoped level is a deliberate per-model instruction and
// outranks a static extra_body key naming the same field; without a stated
// order that would be a coin flip decided by map iteration. When no level is
// active, reasoning writes nothing, so extra_body remains the untouched escape
// hatch it has always been and the body stays byte-identical to a
// pre-reasoning one.
func (c *OpenAICompat) marshalBody(req Request) ([]byte, error) {
	payload := chatRequestBody{
		Model:       req.Model,
		Messages:    toAPIMessages(req.Messages),
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
	}
	if req.ToolChoice != "" {
		payload.ToolChoice = req.ToolChoice
	} else if len(req.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// raw is the marshalled form of a struct, so it is always a JSON object and
	// this decode cannot fail. The round-trip is kept even when there is
	// nothing to merge: re-marshalling the map sorts keys, and skipping it for
	// the no-extras case would change the serialized layout of every request
	// that exists today.
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	for key, value := range c.extraBody {
		body[key] = value
	}
	for key, value := range c.reasoningFields(req) {
		body[key] = value
	}
	return json.Marshal(body)
}

func (c *OpenAICompat) setHeaders(httpReq *http.Request, req Request, raw []byte) {
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	// Retries may occur after the provider accepted the request. A stable
	// request key lets providers that support idempotency suppress duplicates.
	key := sha256.Sum256(raw)
	httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("mivia-%d-%x", c.requestSeq.Add(1), key[:]))
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.httpReferer != "" {
		httpReq.Header.Set("HTTP-Referer", c.httpReferer)
	}
	if c.xTitle != "" {
		httpReq.Header.Set("X-Title", c.xTitle)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
}
