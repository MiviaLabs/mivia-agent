package cli

// read_output's refusal, argument-validation and page-fitting paths. The tool
// is model-facing: every bad input must come back as a bounded answer or a
// plain error, never a panic or a half-written page.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// faultyContentStore stores fine and fails every read with an error that is
// none of the remainder sentinels.
type faultyContentStore struct{ stored map[string][]byte }

var errReadFault = errors.New("content store fault")

func (f *faultyContentStore) StoreContent(_ context.Context, ref string, data []byte) error {
	if f.stored == nil {
		f.stored = map[string][]byte{}
	}
	f.stored[ref] = data
	return nil
}

func (f *faultyContentStore) LoadContent(context.Context, string) ([]byte, error) {
	return nil, errReadFault
}

// impostorTool answers to read_output without being the host's reader.
type impostorTool struct{}

func (impostorTool) Name() string               { return "read_output" }
func (impostorTool) Description() string        { return "impostor" }
func (impostorTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (impostorTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func TestRemainderSpoolFromRegistryWithoutTheTool(t *testing.T) {
	if spool := RemainderSpoolFromRegistry(nil); spool != nil {
		t.Fatal("a nil registry produced a spool")
	}
	if spool := RemainderSpoolFromRegistry(tools.NewRegistry()); spool != nil {
		t.Fatal("a registry without read_output produced a spool")
	}
	reg := tools.NewRegistry()
	reg.Register(impostorTool{})
	if spool := RemainderSpoolFromRegistry(reg); spool != nil {
		t.Fatal("a foreign tool named read_output produced a spool")
	}
}

func TestReadOutputRefusesMalformedRequests(t *testing.T) {
	spool := remainder.NewSpool(remainder.NewMemoryStore())
	tool := &readOutputTool{spool: spool}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})

	if _, err := tool.Execute(ctx, json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed arguments accepted")
	}
	out, err := tool.Execute(ctx, json.RawMessage(`{"ref":""}`))
	if err != nil || !strings.Contains(out, "ref is required") {
		t.Fatalf("empty ref = %q, %v", out, err)
	}
	out, err = tool.Execute(ctx, json.RawMessage(`{"ref":"not-a-ref"}`))
	if err != nil || !strings.Contains(out, "malformed reference") {
		t.Fatalf("malformed ref = %q, %v", out, err)
	}
	errorRef := contentref.Reference(contentref.KindError, []byte("body"))
	if errorRef == "" {
		t.Fatal("fixture reference not minted")
	}
	raw, _ := json.Marshal(map[string]any{"ref": errorRef})
	out, err = tool.Execute(ctx, raw)
	if err != nil || !strings.Contains(out, "only ref:output references") {
		t.Fatalf("non-output ref = %q, %v", out, err)
	}
}

func TestReadOutputRequiresASpoolAndACaller(t *testing.T) {
	ref := contentref.Reference(contentref.KindOutput, []byte("body"))
	raw, _ := json.Marshal(map[string]any{"ref": ref})

	spoolless := &readOutputTool{}
	if _, err := spoolless.Execute(context.Background(), raw); err == nil {
		t.Fatal("a tool with no spool answered")
	}

	tool := &readOutputTool{spool: remainder.NewSpool(remainder.NewMemoryStore())}
	out, err := tool.Execute(context.Background(), raw)
	if err != nil || !strings.Contains(out, "caller principal required") {
		t.Fatalf("uncredentialed call = %q, %v", out, err)
	}
	anonymous := runtime.ContextWithCaller(context.Background(), runtime.Caller{})
	out, err = tool.Execute(anonymous, raw)
	if err != nil || !strings.Contains(out, "caller principal required") {
		t.Fatalf("anonymous caller = %q, %v", out, err)
	}
}

func TestReadOutputSurfacesAStoreFault(t *testing.T) {
	store := &faultyContentStore{}
	spool := remainder.NewSpool(store)
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	ref := spool.Spool(ctx, "owner", []byte("body"))
	if ref == "" {
		t.Fatal("spool minted no ref")
	}
	tool := &readOutputTool{spool: spool}
	raw, _ := json.Marshal(map[string]any{"ref": ref})

	out, err := tool.Execute(ctx, raw)
	if err == nil {
		t.Fatalf("a store fault was reported as a page: %s", out)
	}
	if !strings.Contains(err.Error(), "read_output") {
		t.Fatalf("err = %v, want it attributed to read_output", err)
	}
}

func TestReadOutputRefusesBadOffsets(t *testing.T) {
	spool := remainder.NewSpool(remainder.NewMemoryStore())
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	body := "héllo wörld"
	ref := spool.Spool(ctx, "owner", []byte(body))
	tool := &readOutputTool{spool: spool}

	raw, _ := json.Marshal(map[string]any{"ref": ref, "offset": len(body) + 1})
	if _, err := tool.Execute(ctx, raw); err == nil || !strings.Contains(err.Error(), "exceeds content length") {
		t.Fatalf("past-the-end offset = %v", err)
	}
	// Offset 2 lands inside the two-byte 'é'.
	raw, _ = json.Marshal(map[string]any{"ref": ref, "offset": 2})
	if _, err := tool.Execute(ctx, raw); err == nil || !strings.Contains(err.Error(), "UTF-8 boundary") {
		t.Fatalf("mid-rune offset = %v", err)
	}
}

func TestReadOutputCapabilityAndLimits(t *testing.T) {
	plain := &readOutputTool{}
	if got := plain.Capability(nil).MaxResultBytes; got != defaultReadOutputResultBytes {
		t.Fatalf("default result cap = %d, want %d", got, defaultReadOutputResultBytes)
	}
	if got := plain.pageLimit(); got != defaultReadOutputMaxBytes {
		t.Fatalf("default page limit = %d", got)
	}
	tight := &readOutputTool{maxBytes: 64, resultCapBytes: 4096}
	if got := tight.Capability(nil).MaxResultBytes; got != 4096 {
		t.Fatalf("configured result cap = %d, want 4096", got)
	}
	if got := tight.pageLimit(); got != 64 {
		t.Fatalf("configured page limit = %d, want 64", got)
	}
	oversized := &readOutputTool{maxBytes: 1 << 20, resultCapBytes: 1 << 20}
	if got := oversized.resultLimit(); got != defaultReadOutputResultBytes {
		t.Fatalf("oversized result cap = %d, want the default ceiling", got)
	}
}

func TestDecodeReadOutputParamsRejectsEveryMalformedShape(t *testing.T) {
	bad := []string{
		``,
		`[]`,
		`{`,
		`{"ref"}`,
		`{"ref":"a","ref":"b"}`,
		`{"ref":null}`,
		`{"ref":1}`,
		`{"offset":null}`,
		`{"offset":"x"}`,
		`{"offset":-1}`,
		`{"limit":null}`,
		`{"limit":"x"}`,
		`{"limit":1}`,
		`{"unknown":1}`,
		`{"ref":"a"} trailing`,
		`{"ref":"a"}{"ref":"b"}`,
		`{"ref":"a",`,
	}
	for _, args := range bad {
		if _, err := decodeReadOutputParams(json.RawMessage(args)); !errors.Is(err, errInvalidReadOutputArguments) {
			t.Errorf("decodeReadOutputParams(%q) = %v, want an invalid-arguments refusal", args, err)
		}
	}
	params, err := decodeReadOutputParams(json.RawMessage(`{"ref":"r","offset":2,"limit":8}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.Ref != "r" || params.Offset != 2 || params.Limit != 8 || !params.HasLimit {
		t.Fatalf("params = %+v", params)
	}
}

func TestPageEndStopsOnARuneBoundary(t *testing.T) {
	content := "aé" // 1 + 2 bytes
	if got := readOutputPageEnd(content, 0, 2); got != 1 {
		t.Fatalf("page end = %d, want 1 (the split rune is excluded)", got)
	}
	if got := readOutputPageEnd(content, 0, 3); got != 3 {
		t.Fatalf("page end = %d, want the whole content", got)
	}
}

func TestPageResponseRefusesWhatItCannotFit(t *testing.T) {
	tool := &readOutputTool{}
	content := "ééééééé"
	if _, err := tool.pageResponse("ref:output:x", len(content), 0, 1, content); err == nil ||
		!strings.Contains(err.Error(), "limit cannot include the next character") {
		t.Fatalf("sub-rune limit = %v", err)
	}

	tiny := &readOutputTool{resultCapBytes: 8}
	if _, err := tiny.pageResponse("ref:output:x", len(content), 0, 64, content); err == nil ||
		!strings.Contains(err.Error(), "too small for response framing") {
		t.Fatalf("sub-framing cap = %v", err)
	}

	// A cap that fits the framing exactly leaves no room for a character.
	empty, err := marshalReadOutputPayload(readOutputPagePayload("ref:output:x", len(content), 0, 64, 0, content))
	if err != nil {
		t.Fatal(err)
	}
	exact := &readOutputTool{resultCapBytes: len(empty)}
	if _, err := exact.pageResponse("ref:output:x", len(content), 0, 64, content); err == nil ||
		!strings.Contains(err.Error(), "cannot include the next character") {
		t.Fatalf("framing-only cap = %v", err)
	}

	// One page's worth of room: the single-pass walk must land below the cap.
	roomy := &readOutputTool{resultCapBytes: len(empty) + 4}
	out, err := roomy.pageResponse("ref:output:x", len(content), 0, 64, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > len(empty)+4 {
		t.Fatalf("page of %d bytes exceeded its %d cap", len(out), len(empty)+4)
	}
}

func TestPageResponseReportsAMarshalFailure(t *testing.T) {
	content := strings.Repeat("x", 64)
	tool := &readOutputTool{}
	restore := marshalPayloadJSON

	marshalPayloadJSON = func(any) ([]byte, error) { return nil, errors.New("boom") }
	_, err := tool.pageResponse("ref:output:x", len(content), 0, 64, content)
	marshalPayloadJSON = restore
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("first-marshal failure = %v", err)
	}

	// Fail only on the final marshal, so the framing probe succeeds and a
	// later candidate does not.
	calls := 0
	marshalPayloadJSON = func(v any) ([]byte, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("late boom")
		}
		return json.Marshal(v)
	}
	_, err = tool.pageResponse("ref:output:x", len(content), 0, 64, content)
	marshalPayloadJSON = restore
	if err == nil || !strings.Contains(err.Error(), "late boom") {
		t.Fatalf("mid-search marshal failure = %v", err)
	}
}
